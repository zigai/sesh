//go:build linux

package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/integration/lifecycle"
)

var (
	ErrFinalRecorded   = errors.New("daemon final save outcome was durably recorded")
	ErrNewSchedulerNil = errors.New("cannot create replacement autosave scheduler: NewScheduler is nil")
	ErrSchedulerNil    = errors.New("cannot run shutdown autosave attempt: scheduler is nil")
)

type CoordinatorConfig struct {
	DrainGrace      time.Duration
	Runtime         lifecycle.LogindRuntime
	Reporter        *RuntimeReporter
	Scheduler       *automation.Scheduler
	NewScheduler    func() (*automation.Scheduler, error)
	IsFinalRecorded func(error) bool
}

type lifecycleCoordinator struct {
	cfg       CoordinatorConfig
	scheduler *automation.Scheduler
	draining  bool
}

func (c *lifecycleCoordinator) handleCanceled(ctx context.Context) error {
	if !c.draining {
		return nil
	}

	if c.cfg.NewScheduler == nil {
		return oops.In("daemon").Code("lifecycle_failed").With("operation", "run_lifecycle").Wrap(ErrNewSchedulerNil)
	}

	// A scheduler runs one shutdown save, so a canceled sleep or shutdown needs a fresh one.
	replacement, err := c.cfg.NewScheduler()
	if err != nil {
		return oops.In("daemon").Code("lifecycle_failed").With("operation", "run_lifecycle").Wrap(err)
	}

	c.scheduler = replacement
	c.draining = false

	if c.cfg.Reporter != nil {
		c.cfg.Reporter.Update(RuntimePhaseRunning)
	}

	zerolog.Ctx(ctx).Info().Str("component", "daemon").Str("operation", "run_lifecycle").
		Str("status", "recovered").Str("reason", "lifecycle_canceled").Msg("lifecycle coordinator resumed")

	return nil
}

func (c *lifecycleCoordinator) handleDraining(eventCtx context.Context) error {
	c.draining = true

	zerolog.Ctx(eventCtx).Info().Str("component", "daemon").Str("operation", "run_lifecycle").
		Str("status", "started").Str("phase", "final_save").Msg("lifecycle final save started")

	if c.cfg.Reporter != nil {
		c.cfg.Reporter.Update(RuntimePhaseDraining)
	}

	if c.scheduler == nil {
		return oops.In("daemon").Code("lifecycle_failed").With("operation", "run_lifecycle").Wrap(ErrSchedulerNil)
	}

	_, shutdownErr := c.scheduler.Shutdown(eventCtx)
	if errors.Is(shutdownErr, ErrFinalRecorded) || (c.cfg.IsFinalRecorded != nil && c.cfg.IsFinalRecorded(shutdownErr)) {
		zerolog.Ctx(eventCtx).Warn().Str("component", "daemon").Str("operation", "run_lifecycle").
			Str("status", "degraded").Str("reason", "final_save_incomplete").
			Bool("recorded", true).Msg("lifecycle final save failure recorded")

		return nil
	}

	if shutdownErr != nil {
		return oops.In("daemon").Code("lifecycle_failed").With("operation", "run_lifecycle").Wrap(shutdownErr)
	}

	return nil
}

func (c *lifecycleCoordinator) handleEvent(eventCtx context.Context, event lifecycle.Event) error {
	if event.Canceled {
		return c.handleCanceled(eventCtx)
	}

	return c.handleDraining(eventCtx)
}

func RunLifecycle(ctx context.Context, cfg CoordinatorConfig) error {
	c := &lifecycleCoordinator{
		cfg:       cfg,
		scheduler: cfg.Scheduler,
		draining:  false,
	}

	zerolog.Ctx(ctx).Info().Str("component", "daemon").Str("operation", "run_lifecycle").
		Str("status", "ok").Str("phase", "running").Msg("lifecycle coordinator ready")

	runErr := lifecycle.RunLogind(ctx, cfg.Runtime, func(eventCtx context.Context, event lifecycle.Event) error {
		return c.handleEvent(eventCtx, event)
	})

	var stopErr error

	if c.scheduler != nil {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.DrainGrace)
		stopErr = c.scheduler.Stop(stopCtx)

		cancel()
	}

	runErr = NormalizeDaemonCancellation(runErr)

	if err := errors.Join(runErr, stopErr); err != nil {
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
