package task

import (
	"context"
	"errors"
	"os"

	"github.com/zigai/sesh/internal/integration/process"
)

// Stalled reports whether a task is creating or finishing while the process that started that
// is gone, so nothing will complete it. A process that cannot be checked counts as running.
func Stalled(ctx context.Context, record Record) bool {
	if record.State != StateCreating && record.State != StateFinishing {
		return false
	}

	if record.Owner.PID <= 0 {
		return true
	}

	identity, err := process.ReadIdentity(ctx, record.Owner.PID)
	if errors.Is(err, process.ErrProcessVanished) || errors.Is(err, process.ErrProcessZombie) {
		return true
	}

	if err != nil {
		return false
	}

	return record.Owner.Birth != "" && identity.String() != record.Owner.Birth
}

// currentProcess identifies this sesh process. Without a birth identity it is known by PID only.
func currentProcess(ctx context.Context) Process {
	pid := os.Getpid()

	identity, err := process.ReadIdentity(ctx, pid)
	if err != nil {
		return Process{PID: pid, Birth: ""}
	}

	return Process{PID: pid, Birth: identity.String()}
}
