//go:build linux

package engine

import (
	"context"
	"fmt"

	gotmux "github.com/zigai/gotmux/tmux"

	integration "github.com/zigai/sesh/internal/integration/tmux"
)

func observeRestoreDaemonIdentity(ctx context.Context, server *gotmux.Server) (restoreDaemonIdentity, error) {
	identity, err := integration.ObserveDaemonIdentity(ctx, server)
	if err != nil {
		return restoreDaemonIdentity{}, fmt.Errorf("observe daemon identity: %w", err)
	}

	birth := identity.Birth.String()
	if birth == "" {
		return restoreDaemonIdentity{}, integration.ErrDaemonIdentityUnavailable
	}

	return restoreDaemonIdentity{Server: identity.Server, Birth: birth}, nil
}

func restoreDaemonTargetAppendix(identity restoreDaemonIdentity) string {
	if identity.Birth == "" {
		return ""
	}

	return "\nbirth=" + identity.Birth
}
