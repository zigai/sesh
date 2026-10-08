//go:build linux

package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/integration/lifecycle"
)

var (
	errDaemonLifecycleUncertain = errors.New("daemon lifecycle outcome is uncertain")
	errTestFinalSave            = errors.New("final save failure")
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

func countingFinalSave(attempts *atomic.Int64, err error) func(context.Context) error {
	return func(context.Context) error {
		attempts.Add(1)

		return err
	}
}

func TestLifecycleSavesOnceAndResumesAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	runtime := &daemonLifecycleRuntime{
		events: make(chan lifecycle.Event, 2),
		cancel: cancel,
	}

	runtime.events <- lifecycle.Event{Kind: lifecycle.EventShutdown, Cycle: 1, Deadline: time.Now().Add(time.Second)}

	runtime.events <- lifecycle.Event{Kind: lifecycle.EventShutdown, Cycle: 1, Canceled: true}

	attempts := new(atomic.Int64)

	if err := RunLifecycle(ctx, CoordinatorConfig{
		Runtime:   runtime,
		FinalSave: countingFinalSave(attempts, errors.Join(ErrFinalRecorded, errTestFinalSave)),
	}); err != nil {
		t.Fatalf("RunLifecycle failed: %v", err)
	}

	if got := attempts.Load(); got != 1 {
		t.Fatalf("final saves = %d, want 1", got)
	}

	if got := runtime.released.Load(); got != 1 {
		t.Fatalf("inhibitor releases = %d, want 1", got)
	}

	if got := runtime.rearmed.Load(); got != 1 {
		t.Fatalf("inhibitor rearms = %d, want 1", got)
	}
}

func TestLifecycleRecordedFinalSaveFailureReleasesInhibitor(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runtime := &daemonLifecycleRuntime{
		events:    make(chan lifecycle.Event, 1),
		cancel:    cancel,
		onRelease: cancel,
	}

	runtime.events <- lifecycle.Event{Kind: lifecycle.EventShutdown, Cycle: 1, Deadline: time.Now().Add(time.Second)}

	attempts := new(atomic.Int64)

	if err := RunLifecycle(ctx, CoordinatorConfig{
		Runtime:   runtime,
		FinalSave: countingFinalSave(attempts, errors.Join(ErrFinalRecorded, errTestFinalSave)),
	}); err != nil {
		t.Fatalf("RunLifecycle error = %v, want nil for durably recorded final save failure", err)
	}

	if got := attempts.Load(); got != 1 {
		t.Fatalf("final saves = %d, want 1", got)
	}

	if got := runtime.released.Load(); got != 1 {
		t.Fatalf("inhibitor releases = %d, want 1", got)
	}
}

func TestLifecycleUnrecordedFinalSaveFailureStopsCoordinator(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	runtime := &daemonLifecycleRuntime{
		events: make(chan lifecycle.Event, 1),
		cancel: cancel,
	}

	runtime.events <- lifecycle.Event{Kind: lifecycle.EventShutdown, Cycle: 1, Deadline: time.Now().Add(time.Second)}

	err := RunLifecycle(ctx, CoordinatorConfig{
		Runtime:   runtime,
		FinalSave: countingFinalSave(new(atomic.Int64), errTestFinalSave),
	})
	if !errors.Is(err, errTestFinalSave) {
		t.Fatalf("RunLifecycle error = %v, want unrecorded error preserved", err)
	}
}

func TestLifecycleCancellationDuringFinalSaveIsClean(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	runtime := &daemonLifecycleRuntime{
		events: make(chan lifecycle.Event, 1),
		cancel: cancel,
	}

	runtime.events <- lifecycle.Event{Kind: lifecycle.EventShutdown, Cycle: 1, Deadline: time.Now().Add(time.Second)}

	err := RunLifecycle(ctx, CoordinatorConfig{
		Runtime: runtime,
		FinalSave: func(saveCtx context.Context) error {
			cancel()
			<-saveCtx.Done()

			return saveCtx.Err()
		},
	})
	if err != nil {
		t.Fatalf("RunLifecycle error = %v, want nil for pure cancellation", err)
	}
}

func TestDaemonCancellationNormalizationPreservesJoinedFailure(t *testing.T) {
	pureCancellation := fmt.Errorf("receive lifecycle event: %w", context.Canceled)
	if normalized := NormalizeDaemonCancellation(pureCancellation); normalized != nil {
		t.Fatalf("pure cancellation normalized to %v, want nil", normalized)
	}

	joined := errors.Join(pureCancellation, errDaemonLifecycleUncertain)

	normalized := NormalizeDaemonCancellation(joined)
	if !errors.Is(normalized, context.Canceled) || !errors.Is(normalized, errDaemonLifecycleUncertain) {
		t.Fatalf("joined lifecycle error normalized to %v, want cancellation and uncertainty", normalized)
	}
}
