//go:build linux || darwin

package session

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const hookPollInterval = 10 * time.Millisecond

func runSupervisedCommand(
	ctx context.Context,
	cmd *exec.Cmd,
	timeout time.Duration,
	now func() time.Time,
) (HookStatus, time.Duration, error) {
	var sysProcAttr syscall.SysProcAttr

	sysProcAttr.Setpgid = true
	cmd.SysProcAttr = &sysProcAttr

	cmd.Stdin = nil
	cmd.Stdout = nil

	if err := ctx.Err(); err != nil {
		return HookStatusCanceled, 0, fmt.Errorf("canceled before start: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return HookStatusFailed, 0, fmt.Errorf("start leader: %w", err)
	}

	pid := cmd.Process.Pid
	pgid := pid
	start := now()

	probe, err := watchExit(pid)
	if err != nil {
		terminateErr := terminateProcessGroup(pgid)
		waitErr := cmd.Wait()

		return HookStatusFailed, now().Sub(start), errors.Join(err, terminateErr, waitErr)
	}

	defer probe.close()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	ticker := time.NewTicker(hookPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			terminateErr := terminateProcessGroup(pgid)
			waitErr := cmd.Wait()
			duration := now().Sub(start)

			return HookStatusCanceled, duration, errors.Join(ctx.Err(), waitErr, terminateErr)

		case <-timer.C:
			terminateErr := terminateProcessGroup(pgid)
			waitErr := cmd.Wait()
			duration := now().Sub(start)

			return HookStatusTimedOut, duration, errors.Join(context.DeadlineExceeded, waitErr, terminateErr)

		case <-ticker.C:
			done, status, duration, err := pollProcessState(probe, pgid, cmd, start, now)
			if done {
				return status, duration, err
			}
		}
	}
}

// The leader stays unreaped until its group is killed, so the process group ID cannot be reused
// by an unrelated group in between.
func pollProcessState(
	probe *exitProbe,
	pgid int,
	cmd *exec.Cmd,
	start time.Time,
	now func() time.Time,
) (bool, HookStatus, time.Duration, error) {
	exited, err := probe.exited()
	if err != nil {
		if errors.Is(err, unix.ECHILD) {
			waitErr := cmd.Wait()
			duration := now().Sub(start)

			return true, HookStatusFailed, duration, errors.Join(err, waitErr)
		}

		killErr := cmd.Process.Kill()
		waitErr := cmd.Wait()
		duration := now().Sub(start)

		return true, HookStatusFailed, duration, errors.Join(err, killErr, waitErr)
	}

	if !exited {
		return false, HookStatusCompleted, 0, nil
	}

	cleanErr := terminateProcessGroup(pgid)
	waitErr := cmd.Wait()
	duration := now().Sub(start)

	if waitErr != nil || cleanErr != nil {
		return true, HookStatusFailed, duration, errors.Join(waitErr, cleanErr)
	}

	return true, HookStatusCompleted, duration, nil
}

func terminateProcessGroup(pgid int) error {
	if pgid <= 1 {
		return nil
	}

	err := syscall.Kill(-pgid, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}

	return fmt.Errorf("kill process group: %w", err)
}
