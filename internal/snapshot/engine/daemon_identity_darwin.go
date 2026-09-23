//go:build darwin

package engine

import (
	"context"
	"fmt"

	gotmux "github.com/zigai/gotmux/tmux"
)

func observeRestoreDaemonIdentity(ctx context.Context, server *gotmux.Server) (restoreDaemonIdentity, error) {
	probe, err := server.Probe(ctx)
	if err != nil {
		return restoreDaemonIdentity{}, fmt.Errorf("probe target daemon: %w", err)
	}

	return restoreDaemonIdentity{Server: probe.Identity}, nil
}

func restoreDaemonTargetAppendix(restoreDaemonIdentity) string {
	return ""
}
