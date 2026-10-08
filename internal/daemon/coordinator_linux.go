//go:build linux

package daemon

import (
	"context"
	"errors"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/integration/lifecycle"
)

var (
	ErrFinalRecorded = errors.New("daemon final save outcome was durably recorded")
	ErrFinalSaveNil  = errors.New("cannot run lifecycle coordinator: FinalSave is nil")
)

type CoordinatorConfig struct {
	Runtime  lifecycle.LogindRuntime
	Reporter *RuntimeReporter
	// FinalSave runs one bounded save for an announced sleep or shutdown. A failure it joins with
	// ErrFinalRecorded was durably recorded and does not stop the coordinator.
	FinalSave func(context.Context) error
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

	saveErr := c.cfg.FinalSave(eventCtx)
	if errors.Is(saveErr, ErrFinalRecorded) {
		zerolog.Ctx(eventCtx).Warn().Str("component", "daemon").Str("operation", "run_lifecycle").
			Str("status", "degraded").Str("reason", "final_save_incomplete").
			Bool("recorded", true).Msg("lifecycle final save failure recorded")

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
