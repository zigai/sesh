package automation_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zigai/sesh/internal/automation"
)

type countingAttempt struct {
	calls    atomic.Int64
	inFlight atomic.Int64
	overlaps atomic.Int64
	sleep    time.Duration
}

func (a *countingAttempt) run(_ context.Context, trigger automation.Trigger) (automation.AttemptResult, error) {
	if trigger != automation.TriggerScheduled {
		return automation.AttemptResult{}, errUnexpectedTrigger
	}

	if a.inFlight.Add(1) != 1 {
		a.overlaps.Add(1)
	}

	time.Sleep(a.sleep)
	a.inFlight.Add(-1)
	a.calls.Add(1)

	return automation.AttemptResult{Attempt: "attempt", Generation: "generation", Status: "partial", Recorded: true}, nil
}

type blockingAttempt struct {
	inFlight atomic.Int64
	overlaps atomic.Int64
	started  chan struct{}
	sleep    time.Duration
}

func (a *blockingAttempt) run(_ context.Context, trigger automation.Trigger) (automation.AttemptResult, error) {
	if a.inFlight.Add(1) != 1 {
		a.overlaps.Add(1)
	}

	defer a.inFlight.Add(-1)

	if trigger == automation.TriggerScheduled {
		a.started <- struct{}{}

		time.Sleep(a.sleep)
	}

	return automation.AttemptResult{}, nil
}

type orderedAttempt struct {
	gate    chan struct{}
	started chan struct{}
	release chan struct{}
	order   []automation.Trigger
}

func newOrderedAttempt() *orderedAttempt {
	attempt := &orderedAttempt{
		gate:    make(chan struct{}, 1),
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
		order:   nil,
	}
	attempt.gate <- struct{}{}

	return attempt
}

func (a *orderedAttempt) run(_ context.Context, trigger automation.Trigger) (automation.AttemptResult, error) {
	<-a.gate

	a.order = append(a.order, trigger)

	a.gate <- struct{}{}

	if trigger == automation.TriggerScheduled {
		a.started <- struct{}{}

		<-a.release
	}

	return automation.AttemptResult{Attempt: string(trigger), Generation: "generation", Status: "partial", Recorded: true}, nil
}

func (a *orderedAttempt) recorded() []automation.Trigger {
	<-a.gate

	order := append([]automation.Trigger(nil), a.order...)

	a.gate <- struct{}{}

	return order
}

type policyRecorder struct {
	attempts atomic.Int64
	facts    chan automation.PolicyFacts
	attached int
}

func (r *policyRecorder) probe(context.Context) (int, error) {
	return r.attached, nil
}

func (r *policyRecorder) policy(input automation.PolicyFacts) automation.PolicyDecision {
	r.facts <- input

	if input.Attached > 0 {
		return automation.PolicyDecision{Admit: false, Reason: automation.ReasonAttachedClients}
	}

	return automation.PolicyDecision{Admit: true, Reason: automation.ReasonAdmitted}
}

func (r *policyRecorder) attempt(_ context.Context, _ automation.Trigger) (automation.AttemptResult, error) {
	r.attempts.Add(1)

	return automation.AttemptResult{}, nil
}

func startScheduler(t *testing.T, config automation.SchedulerConfig) *automation.Scheduler {
	t.Helper()

	scheduler, err := automation.NewScheduler(config)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	if err := scheduler.Start(t.Context()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	return scheduler
}

func TestNewSchedulerRejectsIncompleteConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		config automation.SchedulerConfig
		want   error
	}{
		{
			name:   "missing interval",
			config: automation.SchedulerConfig{AttemptTimeout: time.Second, Attempt: attemptStub},
			want:   automation.ErrSchedulerInterval,
		},
		{
			name:   "missing attempt timeout",
			config: automation.SchedulerConfig{Interval: time.Second, Attempt: attemptStub},
			want:   automation.ErrSchedulerAttemptTimeout,
		},
		{
			name:   "missing attempt",
			config: automation.SchedulerConfig{Interval: time.Second, AttemptTimeout: time.Second},
			want:   automation.ErrSchedulerAttemptRequired,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if _, err := automation.NewScheduler(testCase.config); !errors.Is(err, testCase.want) {
				t.Fatalf("NewScheduler error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func attemptStub(_ context.Context, _ automation.Trigger) (automation.AttemptResult, error) {
	return automation.AttemptResult{}, nil
}

func TestSchedulerRunsSingleFlightAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := &countingAttempt{sleep: 100 * time.Millisecond}

		scheduler := startScheduler(t, automation.SchedulerConfig{
			Interval:       time.Second,
			AttemptTimeout: 500 * time.Millisecond,
			Attempt:        recorder.run,
		})

		if err := scheduler.Start(t.Context()); !errors.Is(err, automation.ErrSchedulerStarted) {
			t.Fatalf("second Start error = %v, want ErrSchedulerStarted", err)
		}

		time.Sleep(2500 * time.Millisecond)

		if err := scheduler.Stop(context.Background()); err != nil {
			t.Fatalf("Stop failed: %v", err)
		}

		if got := recorder.calls.Load(); got != 2 {
			t.Fatalf("attempts = %d, want 2", got)
		}

		if got := recorder.overlaps.Load(); got != 0 {
			t.Fatalf("overlapping attempts = %d, want 0", got)
		}

		stats := scheduler.Stats()
		if stats.Attempts != 2 || stats.Admitted != 2 || stats.Failures != 0 {
			t.Fatalf("stats = %+v, want 2 attempts/2 admitted/0 failures", stats)
		}

		last, lastErr := scheduler.LastOutcome()
		if lastErr != nil {
			t.Fatalf("last error = %v, want nil", lastErr)
		}

		if last.Result.Generation != "generation" || last.Trigger != automation.TriggerScheduled {
			t.Fatalf("last outcome = %+v, want the scheduled generation", last)
		}
	})
}

func TestSchedulerCoalescesTicksWhileAttempting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64

		scheduler := startScheduler(t, automation.SchedulerConfig{
			Interval:       time.Second,
			AttemptTimeout: 30 * time.Second,
			Attempt: func(_ context.Context, _ automation.Trigger) (automation.AttemptResult, error) {
				calls.Add(1)
				time.Sleep(5 * time.Second)

				return automation.AttemptResult{Attempt: "attempt", Generation: "generation", Status: "partial", Recorded: true}, nil
			},
		})

		time.Sleep(5500 * time.Millisecond)

		if err := scheduler.Stop(context.Background()); err != nil {
			t.Fatalf("Stop failed: %v", err)
		}

		if got := calls.Load(); got != 1 {
			t.Fatalf("attempts = %d, want 1 coalesced attempt", got)
		}
	})
}

func TestSchedulerPolicySkipsAttachedServers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := &policyRecorder{facts: make(chan automation.PolicyFacts, 4), attached: 1}

		scheduler := startScheduler(t, automation.SchedulerConfig{
			Interval:       time.Second,
			AttemptTimeout: time.Second,
			Probe:          recorder.probe,
			Policy:         recorder.policy,
			Attempt:        recorder.attempt,
		})

		time.Sleep(1500 * time.Millisecond)

		if err := scheduler.Stop(context.Background()); err != nil {
			t.Fatalf("Stop failed: %v", err)
		}

		if got := recorder.attempts.Load(); got != 0 {
			t.Fatalf("attempts = %d, want 0 while a client is attached", got)
		}

		select {
		case input := <-recorder.facts:
			if !input.AttachedKnown || input.Attached != 1 {
				t.Fatalf("policy facts = %+v, want a known attached count of 1", input)
			}

			if input.Draining {
				t.Fatal("policy facts reported draining before shutdown")
			}
		default:
			t.Fatal("policy was never evaluated")
		}

		stats := scheduler.Stats()
		if stats.Skipped != 1 || stats.Admitted != 0 {
			t.Fatalf("stats = %+v, want 1 skip/0 admitted", stats)
		}
	})
}

func TestSchedulerShutdownDrainsInflightAndRunsFinalAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		attempt := newOrderedAttempt()

		scheduler := startScheduler(t, automation.SchedulerConfig{
			Interval:       time.Second,
			AttemptTimeout: 30 * time.Second,
			DrainGrace:     5 * time.Second,
			Attempt:        attempt.run,
		})

		<-attempt.started

		outcomes := make(chan automation.AttemptOutcome, 1)
		errs := make(chan error, 1)

		go func() {
			outcome, err := scheduler.Shutdown(context.Background())

			outcomes <- outcome

			errs <- err
		}()

		synctest.Wait()

		close(attempt.release)

		outcome, err := <-outcomes, <-errs
		if err != nil {
			t.Fatalf("Shutdown failed: %v", err)
		}

		if !outcome.Admitted || outcome.Trigger != automation.TriggerShutdown {
			t.Fatalf("shutdown outcome = %+v, want an admitted shutdown attempt", outcome)
		}

		order := attempt.recorded()
		if len(order) != 2 || order[0] != automation.TriggerScheduled || order[1] != automation.TriggerShutdown {
			t.Fatalf("attempt order = %v, want [scheduled shutdown]", order)
		}

		if stats := scheduler.Stats(); stats.DrainTimeouts != 0 {
			t.Fatalf("drain timeouts = %d, want 0 when the in-flight attempt completes", stats.DrainTimeouts)
		}
	})
}

func TestSchedulerShutdownReportsIncompleteDrainInsteadOfConcurrentSave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := &blockingAttempt{started: make(chan struct{}, 1), sleep: 10 * time.Second}

		scheduler := startScheduler(t, automation.SchedulerConfig{
			Interval:       time.Second,
			AttemptTimeout: 30 * time.Second,
			DrainGrace:     time.Second,
			Attempt:        recorder.run,
		})

		<-recorder.started

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		outcome, err := scheduler.Shutdown(ctx)
		if err == nil {
			t.Fatal("Shutdown with an incomplete drain must report an error")
		}

		if outcome.Admitted || outcome.Reason != automation.ReasonDrainIncomplete {
			t.Fatalf("outcome = %+v, want a refused drain_incomplete outcome", outcome)
		}

		if got := recorder.overlaps.Load(); got != 0 {
			t.Fatalf("overlapping attempts = %d, want 0", got)
		}

		if stats := scheduler.Stats(); stats.DrainTimeouts != 1 || stats.ShutdownAttempts != 0 {
			t.Fatalf("stats = %+v, want 1 drain timeout and no shutdown attempt", stats)
		}

		time.Sleep(10 * time.Second)

		if err := scheduler.Stop(context.Background()); err != nil {
			t.Fatalf("Stop failed: %v", err)
		}
	})
}

func TestSchedulerCoalescesConcurrentShutdownAttempts(t *testing.T) {
	var attempts atomic.Int64

	started := make(chan struct{})
	release := make(chan struct{})

	scheduler, err := automation.NewScheduler(automation.SchedulerConfig{
		Interval:       time.Hour,
		AttemptTimeout: time.Second,
		Attempt: func(_ context.Context, trigger automation.Trigger) (automation.AttemptResult, error) {
			if trigger != automation.TriggerShutdown {
				return automation.AttemptResult{}, errUnexpectedTrigger
			}

			attempts.Add(1)
			close(started)
			<-release

			return automation.AttemptResult{
				Attempt: "final-attempt", Generation: "generation", Status: "partial", Recorded: true,
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	type shutdownResult struct {
		outcome automation.AttemptOutcome
		err     error
	}

	results := make(chan shutdownResult, 2)
	shutdown := func() {
		outcome, shutdownErr := scheduler.Shutdown(t.Context())
		results <- shutdownResult{outcome: outcome, err: shutdownErr}
	}

	go shutdown()

	<-started

	go shutdown()

	close(release)

	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("Shutdown failed: %v", result.err)
		}

		if result.outcome.Result.Attempt != "final-attempt" {
			t.Fatalf("shutdown outcome = %+v", result.outcome)
		}
	}

	if got := attempts.Load(); got != 1 {
		t.Fatalf("final attempts = %d, want one", got)
	}

	if err := scheduler.Start(t.Context()); !errors.Is(err, automation.ErrSchedulerShutdown) {
		t.Fatalf("Start after Shutdown error = %v, want ErrSchedulerShutdown", err)
	}
}

func TestSchedulerStopIsBoundedAndIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scheduler, err := automation.NewScheduler(automation.SchedulerConfig{
			Interval:       time.Second,
			AttemptTimeout: time.Second,
			Attempt:        attemptStub,
		})
		if err != nil {
			t.Fatalf("NewScheduler failed: %v", err)
		}

		if err := scheduler.Stop(context.Background()); err != nil {
			t.Fatalf("Stop before Start failed: %v", err)
		}

		if err := scheduler.Start(t.Context()); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		if err := scheduler.Stop(context.Background()); err != nil {
			t.Fatalf("Stop failed: %v", err)
		}

		if err := scheduler.Stop(context.Background()); err != nil {
			t.Fatalf("second Stop failed: %v", err)
		}
	})
}
