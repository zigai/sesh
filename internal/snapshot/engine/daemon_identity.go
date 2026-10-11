//go:build linux || darwin

package engine

import (
	"context"
	"fmt"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
)

func observeRestoreDaemonIdentity(ctx context.Context, server *gotmux.Server) (restoreDaemonIdentity, error) {
	identity, err := tmuxserver.ObserveDaemonIdentity(ctx, server)
	if err != nil {
		return restoreDaemonIdentity{}, fmt.Errorf("observe daemon identity: %w", err)
	}

	birth := identity.Birth.String()
	if birth == "" {
		return restoreDaemonIdentity{}, tmuxserver.ErrDaemonIdentityUnavailable
	}

	return restoreDaemonIdentity{Server: identity.Server, Birth: birth}, nil
}

func restoreDaemonTargetAppendix(identity restoreDaemonIdentity) string {
	if identity.Birth == "" {
		return ""
	}

	return "\nbirth=" + identity.Birth
}
