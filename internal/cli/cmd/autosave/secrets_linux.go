//go:build linux

package autosave

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/integration/lifecycle"
	"github.com/zigai/sesh/internal/logging"
)

const secretServiceStartWait = 20 * time.Second

func awaitDesktopKeyring(ctx context.Context) {
	started := time.Now()

	state, err := lifecycle.WaitForSecretService(ctx, secretServiceStartWait)
	logger := zerolog.Ctx(ctx)

	switch {
	case err != nil:
		logger.Warn().Str("component", "snapshot").Str("operation", "await_secret_service").
			Str("status", statusDegraded).Func(logging.Cause(err)).
			Msg("startup restore could not check the desktop keyring")
	case state == lifecycle.SecretServiceTimedOut:
		logger.Warn().Str("component", "snapshot").Str("operation", "await_secret_service").
			Str("status", statusDegraded).Dur("waited", time.Since(started)).
			Msg("startup restore continues without the desktop keyring")
	default:
		logger.Info().Str("component", "snapshot").Str("operation", "await_secret_service").
			Str("secret_service", string(state)).Dur("waited", time.Since(started)).
			Msg("startup restore checked the desktop keyring")
	}
}
