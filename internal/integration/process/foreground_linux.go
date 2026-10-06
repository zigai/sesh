//go:build linux

package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

const (
	maxForegroundProcesses = 4096
	maxProcessTasks        = 4096
	maxForegroundTasks     = 16384
	maxChildrenBytes       = 256 << 10
	foregroundCandidateCap = 2
	foregroundStatValues   = 5
	statParentIndex        = 1
	statGroupIndex         = 2
	statSessionIndex       = 3
	statTTYIndex           = 4
	statForegroundIndex    = 5
)

type linuxForegroundProcess struct {
	pid             int
	parent          int
	group           int
	session         int
	tty             int
	foregroundGroup int
	startTime       uint64
}

type linuxForegroundSelection struct {
	pane    linuxForegroundProcess
	process linuxForegroundProcess
}

func observeForegroundPlatform(ctx context.Context, req ForegroundRequest) (*Observation, error) {
	paneBirth, err := readIdentity(ctx, defaultProcRoot, defaultBootIDPath, req.PanePID)
	if err != nil {
		return nil, err
	}

	if paneBirth != req.ExpectedPaneBirth {
		return nil, ErrIdentityMismatch
	}

	before, err := selectLinuxForeground(ctx, defaultProcRoot, req)
	if err != nil {
		return nil, err
	}

	observation, err := observeLinux(ctx, defaultProcRoot, defaultBootIDPath, Request{
		PID:             before.process.pid,
		ExpectedBirth:   Identity{BootID: req.ExpectedPaneBirth.BootID, StartTime: before.process.startTime},
		EnvironmentKeys: req.EnvironmentKeys,
	})
	if err != nil {
		return nil, errors.Join(ErrForegroundChanged, err)
	}

	after, err := selectLinuxForeground(ctx, defaultProcRoot, req)
	if err != nil {
		return nil, errors.Join(ErrForegroundChanged, err)
	}

	if !sameLinuxForegroundSelection(before, after) ||
		observation.PID != after.process.pid || observation.Birth.StartTime != after.process.startTime {
		return nil, ErrForegroundChanged
	}

	paneBirth, err = readIdentity(ctx, defaultProcRoot, defaultBootIDPath, req.PanePID)
	if err != nil {
		return nil, err
	}

	if paneBirth != req.ExpectedPaneBirth {
		return nil, ErrIdentityMismatch
	}

	return observation, nil
}

func sameLinuxForegroundSelection(first, second linuxForegroundSelection) bool {
	return first == second
}

func selectLinuxForeground(ctx context.Context, procRoot string, req ForegroundRequest) (linuxForegroundSelection, error) {
	pane, err := readLinuxForegroundProcess(ctx, procRoot, req.PanePID)
	if err != nil {
		return linuxForegroundSelection{}, err
	}

	if pane.startTime != req.ExpectedPaneBirth.StartTime {
		return linuxForegroundSelection{}, ErrIdentityMismatch
	}

	if pane.foregroundGroup <= 0 {
		return linuxForegroundSelection{}, ErrForegroundUnavailable
	}

	if pane.group == pane.foregroundGroup {
		return linuxForegroundSelection{pane: pane, process: pane}, nil
	}

	processes, err := readLinuxDescendants(ctx, procRoot, pane)
	if err != nil {
		return linuxForegroundSelection{}, err
	}

	candidate, count := uniqueForegroundCandidate(processes, pane)
	if count > 1 {
		return linuxForegroundSelection{}, ErrForegroundAmbiguous
	}

	if count == 0 {
		return linuxForegroundSelection{}, ErrForegroundUnavailable
	}

	return linuxForegroundSelection{pane: pane, process: candidate}, nil
}

func uniqueForegroundCandidate(
	processes map[int]linuxForegroundProcess,
	pane linuxForegroundProcess,
) (linuxForegroundProcess, int) {
	candidates := make([]linuxForegroundProcess, 0, foregroundCandidateCap)

	for _, process := range processes {
		if !linuxForegroundCandidate(process, pane) {
			continue
		}

		parent, parentKnown := processes[process.parent]
		if parentKnown && parent.group == process.group {
			continue
		}

		candidates = append(candidates, process)
		if len(candidates) > 1 {
			return zeroLinuxForegroundProcess(), len(candidates)
		}
	}

	if len(candidates) == 0 {
		return zeroLinuxForegroundProcess(), 0
	}

	return candidates[0], 1
}

func linuxForegroundCandidate(process, pane linuxForegroundProcess) bool {
	return process.pid != pane.pid &&
		process.group == pane.foregroundGroup &&
		process.foregroundGroup == pane.foregroundGroup &&
		process.session == pane.session &&
		process.tty == pane.tty
}

func readLinuxDescendants(
	ctx context.Context,
	procRoot string,
	pane linuxForegroundProcess,
) (map[int]linuxForegroundProcess, error) {
	processes := map[int]linuxForegroundProcess{pane.pid: pane}
	queue := []int{pane.pid}
	totalTasks := 0

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("foreground traversal canceled: %w", err)
		}

		pid := queue[0]
		queue = queue[1:]

		children, tasks, err := readLinuxProcessChildren(ctx, procRoot, pid)
		totalTasks += tasks

		if totalTasks > maxForegroundTasks {
			return nil, ErrInputTooLarge
		}

		if err != nil {
			if pid != pane.pid && isProcessGone(err) {
				continue
			}

			return nil, err
		}

		queue, err = appendLinuxDescendants(ctx, procRoot, children, processes, queue)
		if err != nil {
			return nil, err
		}
	}

	return processes, nil
}

func appendLinuxDescendants(
	ctx context.Context,
	procRoot string,
	children []int,
	processes map[int]linuxForegroundProcess,
	queue []int,
) ([]int, error) {
	for _, childPID := range children {
		if _, seen := processes[childPID]; seen {
			continue
		}

		if len(processes) >= maxForegroundProcesses {
			return nil, ErrInputTooLarge
		}

		child, present, err := readLinuxDescendant(ctx, procRoot, childPID)
		if err != nil {
			return nil, err
		}

		if !present {
			continue
		}

		processes[childPID] = child
		queue = append(queue, childPID)
	}

	return queue, nil
}

func readLinuxDescendant(ctx context.Context, procRoot string, pid int) (linuxForegroundProcess, bool, error) {
	process, err := readLinuxForegroundProcess(ctx, procRoot, pid)
	if err != nil {
		if isProcessGone(err) || errors.Is(err, ErrProcessZombie) {
			return zeroLinuxForegroundProcess(), false, nil
		}

		return linuxForegroundProcess{}, false, err
	}

	return process, true, nil
}

func readLinuxProcessChildren(ctx context.Context, procRoot string, pid int) ([]int, int, error) {
	taskPath := filepath.Join(procRoot, strconv.Itoa(pid), "task")

	taskDir, err := os.Open(taskPath)
	if err != nil {
		return nil, 0, sanitizePathError(err)
	}

	names, readErr := taskDir.Readdirnames(maxProcessTasks + 1)
	closeErr := sanitizePathError(taskDir.Close())

	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, 0, errors.Join(sanitizePathError(readErr), closeErr)
	}

	if closeErr != nil {
		return nil, 0, closeErr
	}

	if len(names) > maxProcessTasks {
		return nil, len(names), ErrInputTooLarge
	}

	children := make([]int, 0)
	seen := make(map[int]bool)

	for _, name := range names {
		_, ok := parsePositivePID(name)
		if !ok {
			continue
		}

		taskChildren, err := readLinuxTaskChildren(ctx, filepath.Join(taskPath, name, "children"))
		if err != nil {
			if isProcessGone(err) {
				continue
			}

			return nil, len(names), err
		}

		for _, child := range taskChildren {
			if !seen[child] {
				seen[child] = true
				children = append(children, child)
			}
		}
	}

	return children, len(names), nil
}

func readLinuxTaskChildren(ctx context.Context, path string) ([]int, error) {
	data, err := readBoundedFile(ctx, path, maxChildrenBytes)
	if err != nil {
		return nil, err
	}

	children := make([]int, 0)

	for field := range bytes.FieldsSeq(data) {
		child, ok := parsePositivePID(string(field))
		if !ok {
			return nil, ErrMalformedStat
		}

		children = append(children, child)
	}

	return children, nil
}

func readLinuxForegroundProcess(ctx context.Context, procRoot string, pid int) (linuxForegroundProcess, error) {
	data, err := readBoundedFile(ctx, filepath.Join(procRoot, strconv.Itoa(pid), "stat"), maxStatBytes)
	if err != nil {
		if isProcessGone(err) {
			return linuxForegroundProcess{}, ErrProcessVanished
		}

		return linuxForegroundProcess{}, err
	}

	return parseLinuxForegroundStat(data)
}

func parseLinuxForegroundStat(data []byte) (linuxForegroundProcess, error) {
	openParen := bytes.IndexByte(data, '(')
	closeParen := bytes.LastIndex(data, []byte(") "))

	if openParen <= 0 || closeParen < openParen+1 {
		return linuxForegroundProcess{}, ErrMalformedStat
	}

	pid, ok := parsePositivePID(string(bytes.TrimSpace(data[:openParen])))
	if !ok {
		return linuxForegroundProcess{}, ErrMalformedStat
	}

	fields := bytes.Fields(data[closeParen+2:])
	if len(fields) < statMinFields || len(fields[statStateIndex]) != 1 || !isKnownProcessState(fields[statStateIndex][0]) {
		return linuxForegroundProcess{}, ErrMalformedStat
	}

	if fields[statStateIndex][0] == 'Z' || fields[statStateIndex][0] == 'X' || fields[statStateIndex][0] == 'x' {
		return linuxForegroundProcess{}, ErrProcessZombie
	}

	values, err := parseLinuxForegroundStatValues(fields)
	if err != nil {
		return linuxForegroundProcess{}, err
	}

	startTime, ok := parseCanonicalStartTime(string(fields[statStartTimeIndex]))
	if !ok {
		return linuxForegroundProcess{}, ErrMalformedStat
	}

	return linuxForegroundProcess{
		pid: pid, parent: values[0], group: values[1], session: values[2],
		tty: values[3], foregroundGroup: values[4], startTime: startTime,
	}, nil
}

func parseLinuxForegroundStatValues(fields [][]byte) ([]int, error) {
	values := make([]int, foregroundStatValues)

	for index, fieldIndex := range []int{
		statParentIndex, statGroupIndex, statSessionIndex, statTTYIndex, statForegroundIndex,
	} {
		value, err := strconv.Atoi(string(fields[fieldIndex]))
		if err != nil {
			return nil, ErrMalformedStat
		}

		values[index] = value
	}

	return values, nil
}

func parsePositivePID(value string) (int, bool) {
	parsed, err := strconv.Atoi(value)

	return parsed, err == nil && parsed > 0 && strconv.Itoa(parsed) == value
}

func zeroLinuxForegroundProcess() linuxForegroundProcess {
	return linuxForegroundProcess{
		pid: 0, parent: 0, group: 0, session: 0, tty: 0, foregroundGroup: 0, startTime: 0,
	}
}
