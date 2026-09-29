//go:build linux

package cmddaemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/integration/lifecycle"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var (
	errUnexpectedDaemonTrigger  = errors.New("unexpected daemon scheduler trigger")
	errDaemonLifecycleUncertain = errors.New("daemon lifecycle outcome is uncertain")
	errTestUnrecordedStatus     = errors.New("unrecorded status failure")
)

type daemonLifecycleRuntime struct {
	events         chan lifecycle.Event
	released       atomic.Int64
	rearmed        atomic.Int64
	cancel         context.CancelFunc
	onRelease      func()
	holdAfterRearm time.Duration
}

func (r *daemonLifecycleRuntime) Next(ctx context.Context) (lifecycle.Event, error) {
	if r.rearmed.Load() > 0 {
		time.Sleep(r.holdAfterRearm)
		r.cancel()

		return lifecycle.Event{}, fmt.Errorf("finish daemon lifecycle test: %w", context.Cause(ctx))
	}

	select {
	case event := <-r.events:
		return event, nil
	case <-ctx.Done():
		return lifecycle.Event{}, fmt.Errorf("wait for daemon lifecycle event: %w", context.Cause(ctx))
	}
}

func (r *daemonLifecycleRuntime) Release() error {
	r.released.Add(1)

	if r.onRelease != nil {
		r.onRelease()
	}

	return nil
}

func (r *daemonLifecycleRuntime) Rearm(context.Context) error {
	r.rearmed.Add(1)

	return nil
}

func TestDaemonLifecycleStopsForFinalSaveAndRestartsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	runtime := &daemonLifecycleRuntime{
		events: make(chan lifecycle.Event, 2),
		cancel: cancel,
	}

	runtime.events <- lifecycle.Event{
		Kind:     lifecycle.EventShutdown,
		Cycle:    1,
		Deadline: time.Now().Add(time.Second),
	}

	runtime.events <- lifecycle.Event{
		Kind:     lifecycle.EventShutdown,
		Cycle:    1,
		Canceled: true,
	}

	shutdownAttempts := new(atomic.Int64)
	schedulerCreations := new(atomic.Int64)

	newScheduler := func() (*automation.Scheduler, error) {
		schedulerCreations.Add(1)

		return automation.NewScheduler(automation.SchedulerConfig{
			Interval:       time.Hour,
			AttemptTimeout: time.Second,
			DrainGrace:     100 * time.Millisecond,
			Attempt: func(_ context.Context, trigger automation.Trigger) (automation.AttemptResult, error) {
				if trigger != automation.TriggerShutdown {
					return automation.AttemptResult{}, errUnexpectedDaemonTrigger
				}

				shutdownAttempts.Add(1)

				return automation.AttemptResult{
					Generation: "partial-generation",
					Status:     "partial",
					Recorded:   true,
				}, errors.Join(errDaemonFinalRecorded, errAutosaveIncomplete)
			},
		})
	}

	scheduler, err := newScheduler()
	if err != nil {
		t.Fatalf("newScheduler failed: %v", err)
	}

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := runDaemonLifecycle(ctx, scheduler, newScheduler, runtime, time.Second, nil); err != nil {
		t.Fatalf("runDaemonLifecycle failed: %v", err)
	}

	if got := shutdownAttempts.Load(); got != 1 {
		t.Fatalf("shutdown attempts = %d, want 1", got)
	}

	if got := schedulerCreations.Load(); got != 2 {
		t.Fatalf("scheduler creations = %d, want initial and replacement", got)
	}

	if got := runtime.released.Load(); got != 1 {
		t.Fatalf("inhibitor releases = %d, want 1", got)
	}

	if got := runtime.rearmed.Load(); got != 1 {
		t.Fatalf("inhibitor rearms = %d, want 1", got)
	}
}

func TestDaemonLifecycleNeverRunsPeriodicSaves(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	runtime := &daemonLifecycleRuntime{
		events:         make(chan lifecycle.Event, 2),
		cancel:         cancel,
		holdAfterRearm: 100 * time.Millisecond,
	}

	runtime.events <- lifecycle.Event{Kind: lifecycle.EventSleep, Cycle: 1, Deadline: time.Now().Add(time.Second)}

	runtime.events <- lifecycle.Event{Kind: lifecycle.EventSleep, Cycle: 1, Canceled: true}

	scheduledAttempts := new(atomic.Int64)

	newScheduler := func() (*automation.Scheduler, error) {
		return automation.NewScheduler(automation.SchedulerConfig{
			Interval:       time.Millisecond,
			AttemptTimeout: time.Second,
			DrainGrace:     100 * time.Millisecond,
			Attempt: func(_ context.Context, trigger automation.Trigger) (automation.AttemptResult, error) {
				if trigger != automation.TriggerShutdown {
					scheduledAttempts.Add(1)
				}

				return automation.AttemptResult{Recorded: true}, errDaemonFinalRecorded
			},
		})
	}

	scheduler, err := newScheduler()
	if err != nil {
		t.Fatalf("newScheduler failed: %v", err)
	}

	if err := runDaemonLifecycle(ctx, scheduler, newScheduler, runtime, time.Second, nil); err != nil {
		t.Fatalf("runDaemonLifecycle failed: %v", err)
	}

	if got := scheduledAttempts.Load(); got != 0 {
		t.Fatalf("periodic attempts = %d, want none: periodic saves belong to the autosave timer", got)
	}
}

func TestDaemonRepositoryGateRefusesUnresolvedSave(t *testing.T) {
	repositoryPath := filepath.Join(t.TempDir(), "repository")

	repository, err := store.Open(t.Context(), repositoryPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	attempt, err := repository.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	if err := repository.Fail(t.Context(), attempt.ID); err != nil {
		t.Fatalf("Fail failed: %v", err)
	}

	if err := repository.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	cmd := newDaemonRunCommand()
	if err := cmd.Flags().Set("repository", repositoryPath); err != nil {
		t.Fatalf("set repository flag: %v", err)
	}

	err = ensureDaemonRepositoryReady(t.Context(), cmd, autosaveEnv{})
	if !errors.Is(err, errDaemonRecoveryBarrier) {
		t.Fatalf("ensureDaemonRepositoryReady error = %v, want recovery barrier", err)
	}
}

func TestDaemonCancellationNormalizationPreservesJoinedFailure(t *testing.T) {
	pureCancellation := fmt.Errorf("receive lifecycle event: %w", context.Canceled)
	if normalized := normalizeDaemonCancellation(pureCancellation); normalized != nil {
		t.Fatalf("pure cancellation normalized to %v, want nil", normalized)
	}

	joined := errors.Join(pureCancellation, errDaemonLifecycleUncertain)

	normalized := normalizeDaemonCancellation(joined)
	if !errors.Is(normalized, context.Canceled) || !errors.Is(normalized, errDaemonLifecycleUncertain) {
		t.Fatalf("joined lifecycle error normalized to %v, want cancellation and uncertainty", normalized)
	}
}

func TestDaemonLifecycleFailedFinalSaveDurablyRecordedReleasesInhibitor(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runtime := &daemonLifecycleRuntime{
		events:    make(chan lifecycle.Event, 1),
		cancel:    cancel,
		onRelease: cancel,
	}

	runtime.events <- lifecycle.Event{
		Kind:     lifecycle.EventShutdown,
		Cycle:    1,
		Deadline: time.Now().Add(time.Second),
	}

	shutdownAttempts := new(atomic.Int64)
	newScheduler := func() (*automation.Scheduler, error) {
		return automation.NewScheduler(automation.SchedulerConfig{
			Interval:       time.Hour,
			AttemptTimeout: time.Second,
			DrainGrace:     100 * time.Millisecond,
			Attempt: func(_ context.Context, trigger automation.Trigger) (automation.AttemptResult, error) {
				if trigger != automation.TriggerShutdown {
					return automation.AttemptResult{}, errUnexpectedDaemonTrigger
				}

				shutdownAttempts.Add(1)

				return automation.AttemptResult{
					Generation: "",
					Status:     "incomplete",
					Recorded:   true,
				}, errors.Join(errDaemonFinalRecorded, errAutosaveIncomplete)
			},
		})
	}

	scheduler, err := newScheduler()
	if err != nil {
		t.Fatalf("newScheduler failed: %v", err)
	}

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := runDaemonLifecycle(ctx, scheduler, newScheduler, runtime, time.Second, nil); err != nil {
		t.Fatalf("runDaemonLifecycle error = %v, want nil for durably recorded final save failure", err)
	}

	if got := shutdownAttempts.Load(); got != 1 {
		t.Fatalf("shutdown attempts = %d, want 1", got)
	}

	if got := runtime.released.Load(); got != 1 {
		t.Fatalf("inhibitor releases = %d, want 1", got)
	}
}

func TestDaemonLifecycleFailedFinalSaveUnrecordedReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runtime := &daemonLifecycleRuntime{
		events: make(chan lifecycle.Event, 1),
		cancel: cancel,
	}

	runtime.events <- lifecycle.Event{
		Kind:     lifecycle.EventShutdown,
		Cycle:    1,
		Deadline: time.Now().Add(time.Second),
	}

	newScheduler := func() (*automation.Scheduler, error) {
		return automation.NewScheduler(automation.SchedulerConfig{
			Interval:       time.Hour,
			AttemptTimeout: time.Second,
			DrainGrace:     100 * time.Millisecond,
			Attempt: func(_ context.Context, trigger automation.Trigger) (automation.AttemptResult, error) {
				if trigger != automation.TriggerShutdown {
					return automation.AttemptResult{}, errUnexpectedDaemonTrigger
				}

				return automation.AttemptResult{}, errTestUnrecordedStatus
			},
		})
	}

	scheduler, err := newScheduler()
	if err != nil {
		t.Fatalf("newScheduler failed: %v", err)
	}

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	err = runDaemonLifecycle(ctx, scheduler, newScheduler, runtime, time.Second, nil)
	if !errors.Is(err, errTestUnrecordedStatus) {
		t.Fatalf("runDaemonLifecycle error = %v, want unrecorded error preserved", err)
	}
}

func TestDaemonLifecycleDrainCancellationCleanlyHandled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	runtime := &daemonLifecycleRuntime{
		events: make(chan lifecycle.Event, 1),
		cancel: cancel,
	}

	runtime.events <- lifecycle.Event{
		Kind:     lifecycle.EventShutdown,
		Cycle:    1,
		Deadline: time.Now().Add(time.Second),
	}

	newScheduler := func() (*automation.Scheduler, error) {
		return automation.NewScheduler(automation.SchedulerConfig{
			Interval:       time.Hour,
			AttemptTimeout: time.Second,
			DrainGrace:     100 * time.Millisecond,
			Attempt: func(attemptCtx context.Context, trigger automation.Trigger) (automation.AttemptResult, error) {
				cancel()
				<-attemptCtx.Done()

				return automation.AttemptResult{}, attemptCtx.Err()
			},
		})
	}

	scheduler, err := newScheduler()
	if err != nil {
		t.Fatalf("newScheduler failed: %v", err)
	}

	if err := scheduler.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	err = runDaemonLifecycle(ctx, scheduler, newScheduler, runtime, time.Second, nil)
	if err != nil {
		t.Fatalf("runDaemonLifecycle error = %v, want nil for pure cancellation", err)
	}
}
