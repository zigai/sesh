//go:build linux

package app

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/integration/lifecycle"
	"github.com/zigai/sesh/internal/logging"
)

// LoginEnvironment returns the current login's environment for a restore. Without a systemd user
// manager to ask, this process's own environment stands for the login.
func LoginEnvironment(ctx context.Context) map[string]string {
	environment, err := lifecycle.LoginEnvironment(ctx)
	if err == nil {
		return environment
	}

	zerolog.Ctx(ctx).Warn().Str("component", "snapshot").Str("operation", "read_login_environment").
		Str("status", "degraded").
		Str("reason", "user_manager_unavailable").Func(logging.Cause(err)).
		Msg("restore takes login variables from its own environment")

	return processEnvironment()
}
