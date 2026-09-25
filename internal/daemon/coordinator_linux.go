//go:build linux

package daemon

import (
	"context"
	"errors"
	"time"

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

	replacement, err := c.cfg.NewScheduler()
	if err != nil {
		return oops.In("daemon").Code("lifecycle_failed").With("operation", "run_lifecycle").Wrap(err)
	}

	if err = replacement.Start(ctx); err != nil {
		return oops.In("daemon").Code("lifecycle_failed").With("operation", "run_lifecycle").Wrap(err)
	}

	c.scheduler = replacement
	c.draining = false

	if c.cfg.Reporter != nil {
		c.cfg.Reporter.Update(RuntimePhaseRunning)
	}

	return nil
}

func (c *lifecycleCoordinator) handleDraining(eventCtx context.Context) error {
	c.draining = true

	if c.cfg.Reporter != nil {
		c.cfg.Reporter.Update(RuntimePhaseDraining)
	}

	if c.scheduler == nil {
		return oops.In("daemon").Code("lifecycle_failed").With("operation", "run_lifecycle").Wrap(ErrSchedulerNil)
	}

	_, shutdownErr := c.scheduler.Shutdown(eventCtx)
	if errors.Is(shutdownErr, ErrFinalRecorded) || (c.cfg.IsFinalRecorded != nil && c.cfg.IsFinalRecorded(shutdownErr)) {
		return nil
	}

	if shutdownErr != nil {
		return oops.In("daemon").Code("lifecycle_failed").With("operation", "run_lifecycle").Wrap(shutdownErr)
	}

	return nil
}

func (c *lifecycleCoordinator) handleEvent(ctx, eventCtx context.Context, event lifecycle.Event) error {
	if event.Canceled {
		return c.handleCanceled(ctx)
	}

	return c.handleDraining(eventCtx)
}

func RunLifecycle(ctx context.Context, cfg CoordinatorConfig) error {
	c := &lifecycleCoordinator{
		cfg:       cfg,
		scheduler: cfg.Scheduler,
		draining:  false,
	}

	runErr := lifecycle.RunLogind(ctx, cfg.Runtime, func(eventCtx context.Context, event lifecycle.Event) error {
		return c.handleEvent(ctx, eventCtx, event)
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
	if DaemonCancellationOnly(err) {
		return nil
	}

	return err
}

func NormalizeCancellation(err error) error {
	return NormalizeDaemonCancellation(err)
}

func DaemonCancellationOnly(err error) bool {
	if err == nil {
		return false
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}

		for _, cause := range causes {
			if !DaemonCancellationOnly(cause) {
				return false
			}
		}

		return true
	}

	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return DaemonCancellationOnly(wrapped.Unwrap())
	}

	return errors.Is(err, context.Canceled)
}
