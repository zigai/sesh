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
	defaultProcRoot    = "/proc"
	defaultBootIDPath  = "/proc/sys/kernel/random/boot_id"
	maxStatBytes       = 4096
	maxBootIDBytes     = 128
	statMinFields      = 20
	statStateIndex     = 0
	statStartTimeIndex = 19
)

func ReadIdentity(ctx context.Context, pid int) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{BootID: "", StartTime: 0}, wrapError(err, "read_identity")
	}

	if pid <= 0 {
		return Identity{BootID: "", StartTime: 0}, wrapError(ErrInvalidPID, "read_identity")
	}

	identity, err := readIdentity(ctx, defaultProcRoot, defaultBootIDPath, pid)
	if err != nil {
		return Identity{BootID: "", StartTime: 0}, wrapError(err, "read_identity")
	}

	if err := ctx.Err(); err != nil {
		return Identity{BootID: "", StartTime: 0}, wrapError(err, "read_identity")
	}

	return identity, nil
}

func readIdentity(ctx context.Context, procRoot, bootIDPath string, pid int) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{BootID: "", StartTime: 0}, fmt.Errorf("context canceled: %w", err)
	}

	bootID, err := readBootID(ctx, bootIDPath)
	if err != nil {
		return Identity{BootID: "", StartTime: 0}, err
	}

	if err := ctx.Err(); err != nil {
		return Identity{BootID: "", StartTime: 0}, fmt.Errorf("context canceled: %w", err)
	}

	statPath := filepath.Join(procRoot, strconv.Itoa(pid), "stat")

	statData, err := readBoundedFile(ctx, statPath, maxStatBytes)
	if err != nil {
		if isProcessGone(err) {
			return Identity{BootID: "", StartTime: 0}, ErrProcessVanished
		}

		return Identity{BootID: "", StartTime: 0}, err
	}

	if err := ctx.Err(); err != nil {
		return Identity{BootID: "", StartTime: 0}, fmt.Errorf("context canceled: %w", err)
	}

	state, startTime, err := parseStat(statData)
	if err != nil {
		return Identity{BootID: "", StartTime: 0}, err
	}

	if state == 'Z' || state == 'X' || state == 'x' {
		return Identity{BootID: "", StartTime: 0}, ErrProcessZombie
	}

	return Identity{BootID: bootID, StartTime: startTime}, nil
}

func readBootID(ctx context.Context, path string) (string, error) {
	data, err := readBoundedFile(ctx, path, maxBootIDBytes)
	if err != nil {
		return "", err
	}

	bootID := string(bytes.TrimSpace(data))
	if !isCanonicalUUID(bootID) {
		return "", ErrMalformedIdentity
	}

	return bootID, nil
}

func parseStat(data []byte) (byte, uint64, error) {
	openParen := bytes.IndexByte(data, '(')
	if openParen <= 0 {
		return 0, 0, ErrMalformedStat
	}

	pidPrefix := bytes.TrimSpace(data[:openParen])
	if !validPIDPrefix(pidPrefix) {
		return 0, 0, ErrMalformedStat
	}

	closeParen := bytes.LastIndex(data, []byte(") "))
	if closeParen < openParen+1 {
		return 0, 0, ErrMalformedStat
	}

	fields := bytes.Fields(data[closeParen+2:])
	if len(fields) < statMinFields {
		return 0, 0, ErrMalformedStat
	}

	stateField := fields[statStateIndex]
	if len(stateField) != 1 || !isKnownProcessState(stateField[0]) {
		return 0, 0, ErrMalformedStat
	}

	startTime, ok := parseCanonicalStartTime(string(fields[statStartTimeIndex]))
	if !ok {
		return 0, 0, ErrMalformedStat
	}

	return stateField[0], startTime, nil
}

func validPIDPrefix(b []byte) bool {
	if len(b) == 0 || b[0] == '0' {
		return false
	}

	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}

	pid, err := strconv.ParseUint(string(b), decimalBase, bitSize64)

	return err == nil && pid > 0
}

func isKnownProcessState(s byte) bool {
	switch s {
	case 'R', 'S', 'D', 'Z', 'T', 't', 'X', 'x', 'K', 'W', 'P', 'I':
		return true
	default:
		return false
	}
}

func readBoundedFile(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context canceled: %w", err)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, sanitizePathError(err)
	}

	if err := ctx.Err(); err != nil {
		closeErr := sanitizePathError(f.Close())

		return nil, errors.Join(fmt.Errorf("context canceled: %w", err), closeErr)
	}

	limited := io.LimitReader(f, maxBytes+1)
	readBytes, readErr := io.ReadAll(limited)
	closeErr := sanitizePathError(f.Close())

	if readErr != nil {
		return nil, errors.Join(sanitizePathError(readErr), closeErr)
	}

	if closeErr != nil {
		return nil, closeErr
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context canceled: %w", err)
	}

	if int64(len(readBytes)) > maxBytes {
		return nil, ErrInputTooLarge
	}

	return readBytes, nil
}
