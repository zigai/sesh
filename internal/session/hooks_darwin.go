//go:build darwin

package session

import (
	"context"
	"os/exec"
	"time"
)

func checkPlatformSupervision() error {
	return ErrHookSupervisionUnsupported
}

func runSupervisedCommand(
	_ context.Context,
	_ *exec.Cmd,
	_ time.Duration,
	_ func() time.Time,
) (HookStatus, time.Duration, error) {
	return HookStatusFailed, 0, ErrHookSupervisionUnsupported
}
