package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
)

const readinessPollInterval = 20 * time.Millisecond

func WaitReady(ctx context.Context, socket string) error {
	server, err := tmuxserver.Open(socket)
	if err != nil {
		return oops.In("daemon").Code("readiness_failed").With("operation", "wait_ready").Wrap(err)
	}

	ticker := time.NewTicker(readinessPollInterval)
	defer ticker.Stop()

	for {
		if _, err := server.Probe(ctx); err == nil {
			return nil
		} else if !errors.Is(err, gotmux.ErrNoServer) {
			return oops.In("daemon").Code("readiness_failed").With("operation", "probe").Wrap(err)
		}

		select {
		case <-ctx.Done():
			cause := context.Cause(ctx)
			if cause == nil {
				cause = ctx.Err()
			}

			return oops.In("daemon").Code("readiness_failed").With("operation", "wait_ready").Wrap(cause)
		case <-ticker.C:
		}
	}
}
