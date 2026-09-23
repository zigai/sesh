//go:build linux

package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	process "github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	maxEditorFiles       = 256
	maxEditorOutputBytes = 1 << 20
	maxProcUnixBytes     = 4 << 20
	maxProcessFDs        = 4096
	maxEditorTasks       = 1024
	maxEditorChildren    = 64
	maxEditorChildrenRaw = 64 << 10
	maxEditorStatBytes   = 4096
	minEditorStatFields  = 2
	editorQueryTimeout   = 2 * time.Second
	procUnixInodeField   = 6
	procUnixFieldCount   = 7

	nvimBufferExpression = `json_encode({"pid":getpid(),"files":map(filter(getbufinfo(), 'v:val.listed && !empty(v:val.name) && getbufvar(v:val.bufnr, "&buftype") == ""'), 'fnamemodify(v:val.name, ":p")')})`
)

var errProcFileTooLarge = errors.New("proc file exceeds configured bound")

type nvimBufferResponse struct {
	PID   int      `json:"pid"`
	Files []string `json:"files"`
}

type editorProcess struct {
	pid         int
	birth       process.Identity
	observation *process.Observation
}

func editorFilesFromProcess(ctx context.Context, observed model.Launch, pid int) ([]string, error) {
	ui, err := observeEditorProcess(ctx, observed, pid)
	if err != nil {
		return nil, err
	}

	core, endpoint, err := resolveEditorEndpoint(ctx, ui, observed.Arguments)
	if err != nil {
		return nil, err
	}

	response, err := queryNvimBuffers(
		ctx,
		string(core.observation.Executable),
		string(core.observation.Directory),
		endpoint,
	)
	if err != nil {
		return nil, err
	}

	if response.PID != core.pid {
		return nil, ErrEditorIdentityChanged
	}

	files, err := validateEditorFiles(response.Files)
	if err != nil {
		return nil, err
	}

	if err := verifyEditorProcess(ctx, observed, pid); err != nil {
		return nil, err
	}

	if owned, err := processOwnsSocket(core.pid, endpoint); err != nil || !owned {
		return nil, errors.Join(ErrEditorIdentityChanged, err)
	}

	if err := verifyResolvedEditorCore(ctx, ui.pid, core); err != nil {
		return nil, err
	}

	return files, nil
}

func resolveEditorEndpoint(
	ctx context.Context,
	ui editorProcess,
	arguments []model.Bytes,
) (editorProcess, string, error) {
	endpoint, err := editorEndpoint(ui.pid, arguments)
	if err == nil {
		return ui, endpoint, nil
	}

	if !errors.Is(err, ErrEditorEndpointMissing) {
		return emptyEditorProcess(), "", err
	}

	core, err := resolveEditorCore(ctx, ui)
	if err != nil {
		return emptyEditorProcess(), "", err
	}

	endpoint, err = editorEndpoint(core.pid, arguments)
	if err != nil {
		return emptyEditorProcess(), "", err
	}

	return core, endpoint, nil
}

func emptyEditorProcess() editorProcess {
	return editorProcess{
		pid: 0,
		birth: process.Identity{
			BootID:    "",
			StartTime: 0,
		},
		observation: nil,
	}
}

func observeEditorProcess(ctx context.Context, observed model.Launch, pid int) (editorProcess, error) {
	birth, err := process.ParseIdentity(string(observed.Birth))
	if err != nil || pid <= 0 {
		return emptyEditorProcess(), errors.Join(ErrEditorIdentityChanged, err)
	}

	current, err := process.Observe(ctx, process.Request{PID: pid, ExpectedBirth: birth, EnvironmentKeys: nil})
	if err != nil {
		return emptyEditorProcess(), errors.Join(ErrEditorIdentityChanged, err)
	}

	if !editorObservationMatchesLaunch(current, observed) {
		return emptyEditorProcess(), ErrEditorIdentityChanged
	}

	return editorProcess{pid: pid, birth: birth, observation: current}, nil
}

func verifyEditorProcess(ctx context.Context, observed model.Launch, pid int) error {
	_, err := observeEditorProcess(ctx, observed, pid)

	return err
}

func editorObservationMatchesLaunch(current *process.Observation, observed model.Launch) bool {
	return bytes.Equal(current.Executable, observed.Executable) && bytes.Equal(current.Directory, observed.Directory) &&
		equalArguments(current.Arguments, observed.Arguments) && !current.ExecutableDeleted && !current.DirectoryDeleted
}

func resolveEditorCore(ctx context.Context, ui editorProcess) (editorProcess, error) {
	children, err := directProcessChildren(ctx, ui.pid)
	if err != nil {
		return emptyEditorProcess(), errors.Join(ErrEditorIdentityChanged, err)
	}

	candidates := make([]editorProcess, 0, 1)

	for _, childPID := range children {
		candidate, ok, candidateErr := sameExecutableEditorChild(ctx, ui, childPID)
		if candidateErr != nil {
			return emptyEditorProcess(), candidateErr
		}

		if ok && isNvimEmbeddedCore(candidate.observation.Arguments) {
			candidates = append(candidates, candidate)
		}
	}

	if len(candidates) == 0 {
		return ui, nil
	}

	if len(candidates) != 1 {
		return emptyEditorProcess(), ErrEditorIdentityChanged
	}

	if err := verifyResolvedEditorCore(ctx, ui.pid, candidates[0]); err != nil {
		return emptyEditorProcess(), err
	}

	return candidates[0], nil
}

func isNvimEmbeddedCore(arguments [][]byte) bool {
	return len(arguments) > 1 && bytes.Equal(arguments[1], []byte("--embed"))
}

func sameExecutableEditorChild(ctx context.Context, ui editorProcess, pid int) (editorProcess, bool, error) {
	birth, err := process.ReadIdentity(ctx, pid)
	if err != nil {
		if errors.Is(err, process.ErrProcessVanished) {
			return emptyEditorProcess(), false, nil
		}

		return emptyEditorProcess(), false, errors.Join(ErrEditorIdentityChanged, err)
	}

	observation, err := process.Observe(ctx, process.Request{PID: pid, ExpectedBirth: birth, EnvironmentKeys: nil})
	if err != nil {
		if errors.Is(err, process.ErrProcessVanished) || errors.Is(err, process.ErrIdentityMismatch) {
			return emptyEditorProcess(), false, nil
		}

		return emptyEditorProcess(), false, errors.Join(ErrEditorIdentityChanged, err)
	}

	if !bytes.Equal(observation.Executable, ui.observation.Executable) ||
		!bytes.Equal(observation.Directory, ui.observation.Directory) ||
		observation.ExecutableDeleted || observation.DirectoryDeleted {
		return emptyEditorProcess(), false, nil
	}

	parent, err := processParentPID(pid)
	if err != nil {
		return emptyEditorProcess(), false, errors.Join(ErrEditorIdentityChanged, err)
	}

	if parent != ui.pid {
		return emptyEditorProcess(), false, nil
	}

	return editorProcess{pid: pid, birth: birth, observation: observation}, true, nil
}

func verifyResolvedEditorCore(ctx context.Context, uiPID int, core editorProcess) error {
	current, err := process.Observe(ctx, process.Request{
		PID: core.pid, ExpectedBirth: core.birth, EnvironmentKeys: nil,
	})
	if err != nil {
		return errors.Join(ErrEditorIdentityChanged, err)
	}

	if !sameEditorObservation(current, core.observation) {
		return ErrEditorIdentityChanged
	}

	if core.pid == uiPID {
		return nil
	}

	parent, err := processParentPID(core.pid)
	if err != nil || parent != uiPID {
		return errors.Join(ErrEditorIdentityChanged, err)
	}

	return nil
}

func sameEditorObservation(current, expected *process.Observation) bool {
	return current.PID == expected.PID && current.Birth == expected.Birth &&
		bytes.Equal(current.Executable, expected.Executable) &&
		bytes.Equal(current.Directory, expected.Directory) &&
		slices.EqualFunc(current.Arguments, expected.Arguments, bytes.Equal) &&
		current.ExecutableDeleted == expected.ExecutableDeleted &&
		current.DirectoryDeleted == expected.DirectoryDeleted
}

func equalArguments(current [][]byte, observed []model.Bytes) bool {
	return slices.EqualFunc(current, observed, func(left []byte, right model.Bytes) bool {
		return bytes.Equal(left, right)
	})
}

func directProcessChildren(ctx context.Context, pid int) ([]int, error) {
	taskDirectory, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "task"))
	if err != nil {
		return nil, fmt.Errorf("read editor process tasks: %w", err)
	}

	tasks, readErr := taskDirectory.Readdirnames(maxEditorTasks + 1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}

	if err := errors.Join(readErr, taskDirectory.Close()); err != nil {
		return nil, fmt.Errorf("read editor process tasks: %w", err)
	}

	if len(tasks) > maxEditorTasks {
		return nil, ErrEditorIdentityChanged
	}

	children := make([]int, 0, 1)

	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("discover editor core canceled: %w", err)
		}

		if _, err := strconv.Atoi(task); err != nil {
			continue
		}

		data, err := readEditorTaskChildren(pid, task)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return nil, err
		}

		children, err = appendEditorChildren(children, data)
		if err != nil {
			return nil, err
		}
	}

	slices.Sort(children)

	return children, nil
}

func readEditorTaskChildren(pid int, task string) ([]byte, error) {
	return readBoundedFile(
		filepath.Join("/proc", strconv.Itoa(pid), "task", task, "children"),
		maxEditorChildrenRaw,
	)
}

func appendEditorChildren(children []int, data []byte) ([]int, error) {
	for childField := range bytes.FieldsSeq(data) {
		child, err := strconv.Atoi(string(childField))
		if err != nil || child <= 0 {
			return nil, ErrEditorIdentityChanged
		}

		if slices.Contains(children, child) {
			continue
		}

		children = append(children, child)
		if len(children) > maxEditorChildren {
			return nil, ErrEditorIdentityChanged
		}
	}

	return children, nil
}

func processParentPID(pid int) (int, error) {
	data, err := readBoundedFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"), maxEditorStatBytes)
	if err != nil {
		return 0, err
	}

	closeParen := bytes.LastIndex(data, []byte(") "))
	if closeParen < 0 {
		return 0, ErrEditorIdentityChanged
	}

	fields := bytes.Fields(data[closeParen+2:])
	if len(fields) < minEditorStatFields {
		return 0, ErrEditorIdentityChanged
	}

	parent, err := strconv.Atoi(string(fields[1]))
	if err != nil || parent <= 0 {
		return 0, ErrEditorIdentityChanged
	}

	return parent, nil
}

func editorEndpoint(pid int, arguments []model.Bytes) (string, error) {
	explicit, found, err := explicitNvimEndpoint(arguments)
	if err != nil {
		return "", err
	}

	paths, err := ownedUnixSocketPaths(pid)
	if err != nil {
		return "", errors.Join(ErrEditorEndpointMissing, err)
	}

	if found {
		if !slices.Contains(paths, explicit) {
			return "", ErrEditorEndpointMissing
		}

		return explicit, nil
	}

	candidates := make([]string, 0, len(paths))
	for _, path := range paths {
		if nvimSocketPath(path) {
			candidates = append(candidates, path)
		}
	}

	if len(candidates) != 1 {
		return "", ErrEditorEndpointMissing
	}

	return candidates[0], nil
}

func explicitNvimEndpoint(arguments []model.Bytes) (string, bool, error) {
	endpoint := ""

	for index := 1; index < len(arguments); index++ {
		argument := string(arguments[index])

		var candidate string

		switch {
		case argument == "--listen":
			index++
			if index >= len(arguments) {
				return "", false, ErrEditorEndpointMissing
			}

			candidate = string(arguments[index])
		case strings.HasPrefix(argument, "--listen="):
			candidate = strings.TrimPrefix(argument, "--listen=")
		default:
			continue
		}

		if endpoint != "" || !cleanAbsolute(candidate) {
			return "", false, ErrEditorEndpointMissing
		}

		endpoint = candidate
	}

	return endpoint, endpoint != "", nil
}

func ownedUnixSocketPaths(pid int) ([]string, error) {
	inodes, err := processSocketInodes(pid)
	if err != nil {
		return nil, err
	}

	data, err := readBoundedFile("/proc/net/unix", maxProcUnixBytes)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(inodes))

	for line := range strings.SplitSeq(string(data), "\n") {
		inode, path, ok := unixSocketLine(line)
		if !ok || !inodes[inode] {
			continue
		}

		if !cleanAbsolute(path) {
			continue
		}

		info, statErr := os.Lstat(path)
		if statErr == nil && info.Mode()&os.ModeSocket != 0 && !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}

	slices.Sort(paths)

	return paths, nil
}

func processSocketInodes(pid int) (map[string]bool, error) {
	directory, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
	if err != nil {
		return nil, fmt.Errorf("read process descriptors: %w", err)
	}

	entries, readErr := directory.ReadDir(maxProcessFDs + 1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}

	if err := errors.Join(readErr, directory.Close()); err != nil {
		return nil, fmt.Errorf("read process descriptors: %w", err)
	}

	if len(entries) > maxProcessFDs {
		return nil, ErrEditorEndpointMissing
	}

	inodes := make(map[string]bool)

	for _, entry := range entries {
		target, readErr := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", entry.Name()))
		if readErr != nil {
			continue
		}

		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}

	return inodes, nil
}

func unixSocketLine(line string) (string, string, bool) {
	offset := 0
	inode := ""

	for field := range procUnixFieldCount {
		for offset < len(line) && procWhitespace(line[offset]) {
			offset++
		}

		start := offset
		for offset < len(line) && !procWhitespace(line[offset]) {
			offset++
		}

		if start == offset {
			return "", "", false
		}

		if field == procUnixInodeField {
			inode = line[start:offset]
		}
	}

	for offset < len(line) && procWhitespace(line[offset]) {
		offset++
	}

	if offset == len(line) {
		return "", "", false
	}

	return inode, line[offset:], true
}

func procWhitespace(value byte) bool {
	return value == ' ' || value == '\t'
}

func processOwnsSocket(pid int, path string) (bool, error) {
	paths, err := ownedUnixSocketPaths(pid)
	if err != nil {
		return false, err
	}

	return slices.Contains(paths, path), nil
}

func nvimSocketPath(path string) bool {
	for component := range strings.SplitSeq(filepath.Clean(path), string(filepath.Separator)) {
		if component == "nvim" || strings.HasPrefix(component, "nvim.") {
			return true
		}
	}

	return false
}

func queryNvimBuffers(
	ctx context.Context,
	executable string,
	directory string,
	endpoint string,
) (nvimBufferResponse, error) {
	queryCtx, cancel := context.WithTimeout(ctx, editorQueryTimeout)
	defer cancel()

	queryDirectory, err := os.MkdirTemp("", "sesh-nvim-query-")
	if err != nil {
		return nvimBufferResponse{}, errors.Join(ErrEditorQueryFailed, err)
	}

	response, queryErr := queryNvimBuffersInDirectory(queryCtx, executable, directory, endpoint, queryDirectory)

	cleanupErr := os.RemoveAll(queryDirectory)
	if cleanupErr != nil {
		cleanupErr = errors.Join(ErrEditorQueryFailed, fmt.Errorf("remove editor query environment: %w", cleanupErr))
	}

	return response, errors.Join(queryErr, cleanupErr)
}

func queryNvimBuffersInDirectory(
	queryCtx context.Context,
	executable string,
	directory string,
	endpoint string,
	queryDirectory string,
) (nvimBufferResponse, error) {
	command := exec.CommandContext(queryCtx, executable, "--clean", "--server", endpoint, "--remote-expr", nvimBufferExpression)
	command.Dir = directory
	command.Env = []string{
		"HOME=" + queryDirectory,
		"XDG_CONFIG_HOME=" + filepath.Join(queryDirectory, "config"),
		"XDG_DATA_HOME=" + filepath.Join(queryDirectory, "data"),
		"XDG_STATE_HOME=" + filepath.Join(queryDirectory, "state"),
		"LC_ALL=C",
		"LANG=C",
		"PATH=" + os.Getenv("PATH"),
	}

	stdout, err := command.StdoutPipe()
	if err != nil {
		return nvimBufferResponse{}, errors.Join(ErrEditorQueryFailed, err)
	}

	if err := command.Start(); err != nil {
		return nvimBufferResponse{}, errors.Join(ErrEditorQueryFailed, err)
	}

	output, readErr := io.ReadAll(io.LimitReader(stdout, maxEditorOutputBytes+1))

	var killErr error
	if len(output) > maxEditorOutputBytes {
		killErr = command.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
	}

	waitErr := command.Wait()
	if err := errors.Join(readErr, killErr, waitErr, queryCtx.Err()); err != nil {
		return nvimBufferResponse{}, errors.Join(ErrEditorQueryFailed, err)
	}

	if len(output) == 0 || len(output) > maxEditorOutputBytes {
		return nvimBufferResponse{}, ErrEditorResponseInvalid
	}

	var response nvimBufferResponse
	if err := json.Unmarshal(bytes.TrimSpace(output), &response); err != nil {
		return nvimBufferResponse{}, errors.Join(ErrEditorResponseInvalid, err)
	}

	return response, nil
}

func validateEditorFiles(files []string) ([]string, error) {
	if len(files) > maxEditorFiles {
		return nil, ErrEditorResponseInvalid
	}

	result := make([]string, 0, len(files))

	total := 0
	for _, path := range files {
		total += len(path)
		if !cleanAbsolute(path) || total > maxEditorOutputBytes {
			return nil, ErrEditorResponseInvalid
		}

		if !slices.Contains(result, path) {
			result = append(result, path)
		}
	}

	return result, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open bounded process source: %w", err)
	}

	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	if len(data) > int(limit) {
		readErr = fmt.Errorf("process source exceeds %d bytes: %w", limit, errProcFileTooLarge)
	}

	return data, errors.Join(readErr, file.Close())
}
