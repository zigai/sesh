//go:build linux

package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	maxCmdlineBytes = 2 << 20
	maxEnvironBytes = 2 << 20
	deletedSuffix   = " (deleted)"
)

var errProcLinkChanged = errors.New("process link target changed during observation")

func observePlatform(ctx context.Context, req Request) (*Observation, error) {
	return observeLinux(ctx, defaultProcRoot, defaultBootIDPath, req)
}

func observeLinux(ctx context.Context, procRoot, bootIDPath string, req Request) (*Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("observe canceled: %w", err)
	}

	initialIdentity, err := readIdentity(ctx, procRoot, bootIDPath, req.PID)
	if err != nil {
		return nil, err
	}

	if initialIdentity != req.ExpectedBirth {
		return nil, ErrIdentityMismatch
	}

	exeBytes, exeDeleted, err := readExecutable(ctx, procRoot, bootIDPath, req.PID)
	if err != nil {
		return nil, err
	}

	args, err := readArguments(ctx, procRoot, req.PID)
	if err != nil {
		return nil, err
	}

	dirBytes, dirDeleted, err := readDirectory(ctx, procRoot, bootIDPath, req.PID)
	if err != nil {
		return nil, err
	}

	envVars, err := readEnvironment(ctx, procRoot, req.PID, req.EnvironmentKeys)
	if err != nil {
		return nil, err
	}

	finalIdentity, err := readIdentity(ctx, procRoot, bootIDPath, req.PID)
	if err != nil {
		return nil, err
	}

	if finalIdentity != req.ExpectedBirth {
		return nil, ErrIdentityMismatch
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("observe canceled: %w", err)
	}

	return &Observation{
		PID:               req.PID,
		Birth:             finalIdentity,
		Executable:        exeBytes,
		ExecutableDeleted: exeDeleted,
		Arguments:         args,
		Directory:         dirBytes,
		DirectoryDeleted:  dirDeleted,
		Environment:       envVars,
	}, nil
}

func readExecutable(ctx context.Context, procRoot, bootIDPath string, pid int) ([]byte, bool, error) {
	exePath := filepath.Join(procRoot, strconv.Itoa(pid), "exe")

	exeBytes, exeDeleted, err := readProcLink(ctx, exePath)
	if err != nil {
		return nil, false, checkLinkError(ctx, procRoot, bootIDPath, pid, err)
	}

	return exeBytes, exeDeleted, nil
}

func readDirectory(ctx context.Context, procRoot, bootIDPath string, pid int) ([]byte, bool, error) {
	cwdPath := filepath.Join(procRoot, strconv.Itoa(pid), "cwd")

	dirBytes, dirDeleted, err := readProcLink(ctx, cwdPath)
	if err != nil {
		return nil, false, checkLinkError(ctx, procRoot, bootIDPath, pid, err)
	}

	return dirBytes, dirDeleted, nil
}

func readArguments(ctx context.Context, procRoot string, pid int) ([][]byte, error) {
	cmdlinePath := filepath.Join(procRoot, strconv.Itoa(pid), "cmdline")

	data, err := readBoundedFile(ctx, cmdlinePath, maxCmdlineBytes)
	if err != nil {
		if isProcessGone(err) {
			return nil, ErrProcessVanished
		}

		return nil, err
	}

	if len(data) == 0 {
		return nil, ErrEmptyCmdline
	}

	if data[len(data)-1] != 0 {
		return nil, ErrMalformedCmdline
	}

	rawArgs := bytes.Split(data[:len(data)-1], []byte{0})

	args := make([][]byte, len(rawArgs))
	for i, a := range rawArgs {
		args[i] = bytes.Clone(a)
	}

	return args, nil
}

func readEnvironment(ctx context.Context, procRoot string, pid int, keys []string) ([]model.Variable, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	requested, wanted := deduplicateKeys(keys)

	environPath := filepath.Join(procRoot, strconv.Itoa(pid), "environ")

	data, err := readBoundedFile(ctx, environPath, maxEnvironBytes)
	if err != nil {
		if isProcessGone(err) {
			return nil, ErrProcessVanished
		}

		return nil, err
	}

	values, err := parseEnvironEntries(data, wanted)
	if err != nil {
		return nil, err
	}

	result := make([]model.Variable, len(requested))
	for i, key := range requested {
		if val, ok := values[key]; ok {
			result[i] = model.Variable{
				Name:   model.Bytes(bytes.Clone([]byte(key))),
				Value:  model.Bytes(val),
				State:  "set",
				Hidden: false,
			}
		} else {
			result[i] = model.Variable{
				Name:   model.Bytes(bytes.Clone([]byte(key))),
				Value:  nil,
				State:  "absent",
				Hidden: false,
			}
		}
	}

	return result, nil
}

func deduplicateKeys(keys []string) ([]string, map[string]struct{}) {
	requested := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))

	for _, key := range keys {
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			requested = append(requested, key)
		}
	}

	return requested, seen
}

func parseEnvironEntries(data []byte, wanted map[string]struct{}) (map[string][]byte, error) {
	if len(data) == 0 {
		return make(map[string][]byte), nil
	}

	if data[len(data)-1] != 0 {
		return nil, ErrMalformedEnviron
	}

	entries := bytes.Split(data[:len(data)-1], []byte{0})
	values := make(map[string][]byte, len(wanted))

	for _, entry := range entries {
		nameBytes, valBytes, ok := bytes.Cut(entry, []byte("="))
		if !ok || len(nameBytes) == 0 {
			continue
		}

		name := string(nameBytes)
		if _, isWanted := wanted[name]; isWanted {
			if _, alreadyFound := values[name]; alreadyFound {
				return nil, ErrDuplicateEnvironmentVariable
			}

			values[name] = bytes.Clone(valBytes)
		}
	}

	return values, nil
}

func readProcLink(ctx context.Context, linkPath string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("observe canceled: %w", err)
	}

	fd, err := unix.Open(linkPath, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, false, sanitizePathError(&os.PathError{Op: "open", Path: linkPath, Err: err})
	}

	target, deleted, readErr := readOpenProcLink(ctx, fd)
	closeErr := sanitizePathError(unix.Close(fd))

	return target, deleted, errors.Join(readErr, closeErr)
}

func readOpenProcLink(ctx context.Context, fd int) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("observe canceled: %w", err)
	}

	var beforeStat unix.Stat_t
	if err := unix.Fstat(fd, &beforeStat); err != nil {
		return nil, false, fmt.Errorf("stat proc target: %w", err)
	}

	targetPath := filepath.Join(defaultProcRoot, "self", "fd", strconv.Itoa(fd))

	targetText, err := os.Readlink(targetPath)
	if err != nil {
		return nil, false, sanitizePathError(err)
	}

	var afterStat unix.Stat_t
	if err := unix.Fstat(fd, &afterStat); err != nil {
		return nil, false, fmt.Errorf("recheck proc target: %w", err)
	}

	return classifyProcLink([]byte(targetText), beforeStat.Nlink, afterStat.Nlink)
}

// classifyProcLink uses the target's link count only as deletion evidence: zero links mean the
// target was deleted. A live directory gains and loses links as subdirectories come and go, so
// only a change in deletion state between the two stats counts as a changed target.
func classifyProcLink[T ~uint32 | ~uint64](targetBytes []byte, beforeLinks, afterLinks T) ([]byte, bool, error) {
	if (beforeLinks == 0) != (afterLinks == 0) {
		return nil, false, errProcLinkChanged
	}

	deletedBytes := []byte(deletedSuffix)

	cleanPath, hasDeletedSuffix := bytes.CutSuffix(targetBytes, deletedBytes)
	if hasDeletedSuffix {
		if afterLinks == 0 {
			return bytes.Clone(cleanPath), true, nil
		}

		return nil, false, ErrAmbiguousProcLink
	}

	return bytes.Clone(targetBytes), false, nil
}

func checkLinkError(ctx context.Context, procRoot, bootIDPath string, pid int, err error) error {
	if isProcessGone(err) {
		_, statErr := readIdentity(ctx, procRoot, bootIDPath, pid)
		if statErr != nil {
			return statErr
		}

		return ErrProcessVanished
	}

	return sanitizePathError(err)
}

func isProcessGone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH)
}
