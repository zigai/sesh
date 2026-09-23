//go:build darwin

package process

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"golang.org/x/sys/unix"
)

const microsecondsPerSecond = 1_000_000

func ReadIdentity(ctx context.Context, pid int) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, wrapError(err, "read_identity")
	}

	if pid <= 0 {
		return Identity{}, wrapError(ErrInvalidPID, "read_identity")
	}

	identity, err := readDarwinIdentity(ctx, pid)
	if err != nil {
		return Identity{}, wrapError(err, "read_identity")
	}

	if err := ctx.Err(); err != nil {
		return Identity{}, wrapError(err, "read_identity")
	}

	return identity, nil
}

func readDarwinIdentity(ctx context.Context, pid int) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, fmt.Errorf("context canceled: %w", err)
	}

	bootSession, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return Identity{}, classifyDarwinProcessError(err)
	}

	bootID := strings.ToLower(strings.TrimSpace(strings.TrimRight(bootSession, "\x00")))
	if !isCanonicalUUID(bootID) {
		return Identity{}, ErrMalformedIdentity
	}

	if err := ctx.Err(); err != nil {
		return Identity{}, fmt.Errorf("context canceled: %w", err)
	}

	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return Identity{}, classifyDarwinProcessError(err)
	}

	if info == nil || int(info.Proc.P_pid) != pid {
		return Identity{}, ErrProcessVanished
	}

	startTime, err := darwinStartTime(info.Proc.P_starttime.Sec, int64(info.Proc.P_starttime.Usec))
	if err != nil {
		return Identity{}, err
	}

	return Identity{BootID: bootID, StartTime: startTime}, nil
}

func darwinStartTime(seconds, microseconds int64) (uint64, error) {
	if seconds < 0 || microseconds < 0 || microseconds >= microsecondsPerSecond {
		return 0, ErrMalformedIdentity
	}

	if uint64(seconds) > math.MaxUint64/microsecondsPerSecond {
		return 0, ErrMalformedIdentity
	}

	secondsPart := uint64(seconds) * microsecondsPerSecond
	if secondsPart > math.MaxUint64-uint64(microseconds) {
		return 0, ErrMalformedIdentity
	}

	return secondsPart + uint64(microseconds), nil
}

func classifyDarwinProcessError(err error) error {
	switch {
	case errors.Is(err, unix.ESRCH), errors.Is(err, unix.ENOENT):
		return ErrProcessVanished
	default:
		return err
	}
}
