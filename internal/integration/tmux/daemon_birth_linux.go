//go:build linux

package tmux

import (
	"context"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/integration/process"
)

func readDaemonBirth(ctx context.Context, pid int) (process.Identity, error) {
	identity, err := process.ReadIdentity(ctx, pid)
	if err != nil {
		return process.Identity{}, oops.In("tmux").Code("daemon_birth_failed").With("operation", "read_daemon_birth").Wrap(err)
	}

	return identity, nil
}
