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
	maxProcessTasks      = 4096
	maxForegroundTasks   = 16384
	maxChildrenBytes     = 256 << 10
	foregroundStatValues = 5
	statParentIndex      = 1
	statGroupIndex       = 2
	statSessionIndex     = 3
	statTTYIndex         = 4
	statForegroundIndex  = 5
)

type procForegroundSource struct {
	procRoot   string
	bootIDPath string
}

func observeForegroundPlatform(ctx context.Context, req ForegroundRequest) (*Observation, error) {
	return observeForeground(ctx, procForegroundSource{procRoot: defaultProcRoot, bootIDPath: defaultBootIDPath}, req)
}

func (s procForegroundSource) readIdentity(ctx context.Context, pid int) (Identity, error) {
	return readIdentity(ctx, s.procRoot, s.bootIDPath, pid)
}

func (s procForegroundSource) readProcess(ctx context.Context, pid int) (foregroundProcess, error) {
	return readLinuxForegroundProcess(ctx, s.procRoot, pid)
}

func (s procForegroundSource) readDescendants(
	ctx context.Context,
	pane foregroundProcess,
) (map[int]foregroundProcess, error) {
	return readLinuxDescendants(ctx, s.procRoot, pane)
}

func (s procForegroundSource) observe(ctx context.Context, req Request) (*Observation, error) {
	return observeLinux(ctx, s.procRoot, s.bootIDPath, req)
}

func readLinuxDescendants(
	ctx context.Context,
	procRoot string,
	pane foregroundProcess,
) (map[int]foregroundProcess, error) {
	processes := map[int]foregroundProcess{pane.pid: pane}
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
	processes map[int]foregroundProcess,
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

func readLinuxDescendant(ctx context.Context, procRoot string, pid int) (foregroundProcess, bool, error) {
	process, err := readLinuxForegroundProcess(ctx, procRoot, pid)
	if err != nil {
		if isProcessGone(err) || errors.Is(err, ErrProcessZombie) {
			return zeroForegroundProcess(), false, nil
		}

		return foregroundProcess{}, false, err
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

func readLinuxForegroundProcess(ctx context.Context, procRoot string, pid int) (foregroundProcess, error) {
	data, err := readBoundedFile(ctx, filepath.Join(procRoot, strconv.Itoa(pid), "stat"), maxStatBytes)
	if err != nil {
		if isProcessGone(err) {
			return foregroundProcess{}, ErrProcessVanished
		}

		return foregroundProcess{}, err
	}

	return parseLinuxForegroundStat(data)
}

func parseLinuxForegroundStat(data []byte) (foregroundProcess, error) {
	openParen := bytes.IndexByte(data, '(')
	closeParen := bytes.LastIndex(data, []byte(") "))

	if openParen <= 0 || closeParen < openParen+1 {
		return foregroundProcess{}, ErrMalformedStat
	}

	pid, ok := parsePositivePID(string(bytes.TrimSpace(data[:openParen])))
	if !ok {
		return foregroundProcess{}, ErrMalformedStat
	}

	fields := bytes.Fields(data[closeParen+2:])
	if len(fields) < statMinFields || len(fields[statStateIndex]) != 1 || !isKnownProcessState(fields[statStateIndex][0]) {
		return foregroundProcess{}, ErrMalformedStat
	}

	if fields[statStateIndex][0] == 'Z' || fields[statStateIndex][0] == 'X' || fields[statStateIndex][0] == 'x' {
		return foregroundProcess{}, ErrProcessZombie
	}

	values, err := parseLinuxForegroundStatValues(fields)
	if err != nil {
		return foregroundProcess{}, err
	}

	startTime, ok := parseCanonicalStartTime(string(fields[statStartTimeIndex]))
	if !ok {
		return foregroundProcess{}, ErrMalformedStat
	}

	return foregroundProcess{
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
