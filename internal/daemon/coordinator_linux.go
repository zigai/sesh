//go:build linux

package daemon

import (
	"context"
	"errors"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/integration/lifecycle"
	"github.com/zigai/sesh/internal/logging"
)

var ErrFinalSaveNil = errors.New("cannot run lifecycle coordinator: FinalSave is nil")

type CoordinatorConfig struct {
	Runtime   lifecycle.LogindRuntime
	Reporter  *RuntimeReporter
	FinalSave func(context.Context) (FinalSaveOutcome, error)
	// Resumed runs when logind cancels the announced shutdown or sleep, or the system wakes.
	Resumed func(context.Context) error
}

type FinalSaveOutcome struct {
	Recorded bool
}

type lifecycleCoordinator struct {
	cfg      CoordinatorConfig
	draining bool
}

func (c *lifecycleCoordinator) handleCanceled(ctx context.Context) {
	if !c.draining {
		return
	}

	c.draining = false

	if c.cfg.Reporter != nil {
		c.cfg.Reporter.Update(RuntimePhaseRunning)
	}

	if c.cfg.Resumed != nil {
		if err := c.cfg.Resumed(ctx); err != nil {
			zerolog.Ctx(ctx).Warn().Str("component", "daemon").Str("operation", "run_lifecycle").
				Str("status", "degraded").Str("reason", "announcement_not_ended").Func(logging.Cause(err)).
				Msg("final save status still marks the canceled shutdown or sleep")
		}
	}

	zerolog.Ctx(ctx).Info().Str("component", "daemon").Str("operation", "run_lifecycle").
		Str("status", "recovered").Str("reason", "lifecycle_canceled").Msg("lifecycle coordinator resumed")
}

func (c *lifecycleCoordinator) handleFinalSave(eventCtx context.Context) error {
	c.draining = true

	zerolog.Ctx(eventCtx).Info().Str("component", "daemon").Str("operation", "run_lifecycle").
		Str("status", "started").Str("phase", "final_save").Msg("lifecycle final save started")

	if c.cfg.Reporter != nil {
		c.cfg.Reporter.Update(RuntimePhaseFinalSave)
	}

	outcome, saveErr := c.cfg.FinalSave(eventCtx)
	if saveErr != nil && outcome.Recorded {
		zerolog.Ctx(eventCtx).Warn().Str("component", "daemon").Str("operation", "run_lifecycle").
			Str("status", "degraded").Str("reason", "final_save_incomplete").
			Bool("recorded", true).Func(logging.Cause(saveErr)).Msg("lifecycle final save failure recorded")

		return nil
	}

	if saveErr != nil {
		return oops.In("daemon").Code("lifecycle_failed").With("operation", "run_lifecycle").Wrap(saveErr)
	}

	return nil
}

func RunLifecycle(ctx context.Context, cfg CoordinatorConfig) error {
	if cfg.FinalSave == nil {
		return oops.In("daemon").Code("lifecycle_failed").With("operation", "run_lifecycle").Wrap(ErrFinalSaveNil)
	}

	c := &lifecycleCoordinator{cfg: cfg, draining: false}

	zerolog.Ctx(ctx).Info().Str("component", "daemon").Str("operation", "run_lifecycle").
		Str("status", "ok").Str("phase", "running").Msg("lifecycle coordinator ready")

	runErr := lifecycle.RunLogind(ctx, cfg.Runtime, func(eventCtx context.Context, event lifecycle.Event) error {
		if event.Canceled {
			c.handleCanceled(eventCtx)

			return nil
		}

		return c.handleFinalSave(eventCtx)
	})

	if err := NormalizeDaemonCancellation(runErr); err != nil {
		return oops.In("daemon").Code("lifecycle_failed").With("operation", "run_lifecycle").Wrap(err)
	}

	return nil
}

func NormalizeDaemonCancellation(err error) error {
	if CancellationOnly(err) {
		return nil
	}

	return err
}

func CancellationOnly(err error) bool {
	if err == nil {
		return false
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}

		for _, cause := range causes {
			if !CancellationOnly(cause) {
				return false
			}
		}

		return true
	}

	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return CancellationOnly(wrapped.Unwrap())
	}

	return errors.Is(err, context.Canceled)
}
