//go:build linux || darwin

package automation

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/samber/oops"
)

const (
	TriggerScheduled Trigger = "scheduled"
	TriggerShutdown  Trigger = "shutdown"
)

const (
	ReasonAttachedClients = "clients_attached"
	ReasonAttachedUnknown = "attachment_unknown"
	ReasonDraining        = "draining"
	ReasonShutdownAdmit   = "shutdown_priority"
	ReasonProbeFailed     = "attachment_probe_failed"
	ReasonDrainIncomplete = "drain_incomplete"
	ReasonAdmitted        = "admitted"
)

const DefaultDrainGrace = 2 * time.Second

const (
	AttachedAllowed      AttachedPolicy = "allow"
	AttachedDetachedOnly AttachedPolicy = "detached_only"
)

var (
	ErrSchedulerStarted         = errors.New("autosave scheduler is already started")
	ErrSchedulerInterval        = errors.New("autosave scheduler interval must be positive")
	ErrSchedulerAttemptTimeout  = errors.New("autosave scheduler attempt timeout must be positive")
	ErrSchedulerAttemptRequired = errors.New("autosave scheduler attempt function is required")
	ErrSchedulerShutdown        = errors.New("autosave scheduler has already shut down")
)

type Trigger string

type AttachedPolicy string

type PolicyFacts struct {
	Trigger       Trigger
	Now           time.Time
	Attached      int
	AttachedKnown bool
	Draining      bool
}

type PolicyDecision struct {
	Admit  bool
	Reason string
}

type AttemptResult struct {
	Attempt    string
	Generation string
	Status     string
	Recorded   bool
}

type AttemptOutcome struct {
	Trigger    Trigger
	Admitted   bool
	Reason     string
	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
	Result     AttemptResult
	Err        error
}

type SchedulerStats struct {
	Attempts         int
	Admitted         int
	Skipped          int
	Failures         int
	DrainTimeouts    int
	ShutdownAttempts int
}

type SchedulerConfig struct {
	Interval       time.Duration
	AttemptTimeout time.Duration
	DrainGrace     time.Duration
	Policy         func(PolicyFacts) PolicyDecision
	Probe          func(ctx context.Context) (int, error)
	Attempt        func(ctx context.Context, trigger Trigger) (AttemptResult, error)
	Now            func() time.Time
}

type Scheduler struct {
	config SchedulerConfig

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	idle     chan struct{}
	started  bool
	draining bool

	shutdownStarted bool
	shutdownDone    chan struct{}
	shutdownOutcome AttemptOutcome
	shutdownErr     error

	stats   SchedulerStats
	last    AttemptOutcome
	lastErr error
}

func NewScheduler(config SchedulerConfig) (*Scheduler, error) {
	if config.Interval <= 0 {
		return nil, oops.In("automation").Code("invalid_configuration").With("operation", "new_scheduler").Wrap(ErrSchedulerInterval)
	}

	if config.AttemptTimeout <= 0 {
		return nil, oops.In("automation").Code("invalid_configuration").With("operation", "new_scheduler").Wrap(ErrSchedulerAttemptTimeout)
	}

	if config.Attempt == nil {
		return nil, oops.In("automation").Code("invalid_configuration").With("operation", "new_scheduler").Wrap(ErrSchedulerAttemptRequired)
	}

	if config.DrainGrace <= 0 {
		config.DrainGrace = DefaultDrainGrace
	}

	if config.Now == nil {
		config.Now = time.Now
	}

	idle := make(chan struct{})
	close(idle)

	scheduler := new(Scheduler)
	scheduler.config = config
	scheduler.idle = idle

	return scheduler, nil
}

func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.shutdownStarted {
		s.mu.Unlock()

		return oops.In("automation").Code("scheduler_shutdown").With("operation", "start_scheduler").Wrap(ErrSchedulerShutdown)
	}

	if s.started {
		s.mu.Unlock()

		return oops.In("automation").Code("scheduler_started").With("operation", "start_scheduler").Wrap(ErrSchedulerStarted)
	}

	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.cancel, s.done, s.started = cancel, done, true
	s.mu.Unlock()

	go s.run(loopCtx, done)

	return nil
}

func (s *Scheduler) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()

		return nil
	}

	s.draining = true
	cancel, done := s.cancel, s.done
	s.mu.Unlock()

	cancel()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		cause := context.Cause(ctx)

		code := "operation_canceled"
		if errors.Is(cause, context.DeadlineExceeded) {
			code = "operation_timed_out"
		}

		return oops.In("automation").Code(code).With("operation", "stop_scheduler").Wrap(cause)
	}
}

func (s *Scheduler) Shutdown(ctx context.Context) (AttemptOutcome, error) {
	s.mu.Lock()
	if s.shutdownStarted {
		done := s.shutdownDone
		s.mu.Unlock()

		select {
		case <-done:
			s.mu.Lock()
			outcome, err := s.shutdownOutcome, s.shutdownErr
			s.mu.Unlock()

			return outcome, err
		case <-ctx.Done():
			cause := context.Cause(ctx)

			code := "operation_canceled"
			if errors.Is(cause, context.DeadlineExceeded) {
				code = "operation_timed_out"
			}

			return AttemptOutcome{}, oops.In("automation").Code(code).With("operation", "wait_shutdown").Wrap(cause)
		}
	}

	s.shutdownStarted = true
	s.shutdownDone = make(chan struct{})
	s.mu.Unlock()

	outcome, err := s.shutdown(ctx)

	s.mu.Lock()
	s.shutdownOutcome = outcome
	s.shutdownErr = err
	close(s.shutdownDone)
	s.mu.Unlock()

	return outcome, err
}

func (s *Scheduler) LastOutcome() (AttemptOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.last, s.lastErr
}

func (s *Scheduler) Stats() SchedulerStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.stats
}

func (s *Scheduler) shutdown(ctx context.Context) (AttemptOutcome, error) {
	s.mu.Lock()
	s.draining = true
	cancel, done, idle := s.cancel, s.done, s.idle
	s.mu.Unlock()

	var drainErr error

	if cancel != nil {
		if !waitForIdle(ctx, idle, s.config.DrainGrace) {
			s.recordDrainTimeout()
		}

		cancel()

		select {
		case <-done:
		case <-ctx.Done():
			cause := context.Cause(ctx)

			code := "operation_canceled"
			if errors.Is(cause, context.DeadlineExceeded) {
				code = "operation_timed_out"
			}

			drainErr = oops.In("automation").Code(code).With("operation", "join_scheduler").Wrap(cause)
		}
	}

	if drainErr != nil {
		outcome := new(AttemptOutcome)
		outcome.Trigger = TriggerShutdown
		outcome.Admitted = false
		outcome.Reason = ReasonDrainIncomplete
		outcome.StartedAt = s.config.Now()
		outcome.FinishedAt = s.config.Now()

		s.record(*outcome, drainErr)

		return *outcome, drainErr
	}

	return s.executeShutdown(ctx)
}

func (s *Scheduler) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	timer := time.NewTimer(s.config.Interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.executeScheduled(ctx)
			timer.Reset(s.config.Interval)
		}
	}
}

func (s *Scheduler) executeScheduled(ctx context.Context) {
	attemptCtx, cancel := context.WithTimeout(ctx, s.config.AttemptTimeout)
	defer cancel()

	finish := s.beginAttempt()
	defer finish()

	now := s.config.Now()
	decision := s.decide(attemptCtx, TriggerScheduled, now)

	outcome := new(AttemptOutcome)
	outcome.Trigger = TriggerScheduled
	outcome.Admitted = decision.Admit
	outcome.Reason = decision.Reason
	outcome.StartedAt = now

	if !decision.Admit {
		s.record(*outcome, nil)

		return
	}

	result, err := s.config.Attempt(attemptCtx, TriggerScheduled)
	if err != nil {
		code := "attempt_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "operation_timed_out"
		} else if errors.Is(err, context.Canceled) {
			code = "operation_canceled"
		}

		err = oops.In("automation").Code(code).With("operation", "execute_scheduled").Wrap(err)
	}

	s.complete(*outcome, result, err)
}

func (s *Scheduler) executeShutdown(ctx context.Context) (AttemptOutcome, error) {
	finish := s.beginAttempt()
	defer finish()

	now := s.config.Now()

	outcome := new(AttemptOutcome)
	outcome.Trigger = TriggerShutdown
	outcome.Admitted = true
	outcome.Reason = ReasonShutdownAdmit
	outcome.StartedAt = now

	s.mu.Lock()
	s.stats.ShutdownAttempts++
	s.mu.Unlock()

	result, err := s.config.Attempt(ctx, TriggerShutdown)
	if err != nil {
		code := "attempt_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "operation_timed_out"
		} else if errors.Is(err, context.Canceled) {
			code = "operation_canceled"
		}

		err = oops.In("automation").Code(code).With("operation", "execute_shutdown").Wrap(err)
	}

	completed := s.complete(*outcome, result, err)

	return completed, err
}

func (s *Scheduler) complete(outcome AttemptOutcome, result AttemptResult, err error) AttemptOutcome {
	outcome.Result = result
	outcome.Err = err
	outcome.FinishedAt = s.config.Now()
	outcome.Duration = outcome.FinishedAt.Sub(outcome.StartedAt)

	s.record(outcome, err)

	return outcome
}

func (s *Scheduler) decide(ctx context.Context, trigger Trigger, now time.Time) PolicyDecision {
	s.mu.Lock()
	draining := s.draining
	s.mu.Unlock()

	if trigger == TriggerShutdown {
		return PolicyDecision{Admit: true, Reason: ReasonShutdownAdmit}
	}

	if draining {
		return PolicyDecision{Admit: false, Reason: ReasonDraining}
	}

	facts := PolicyFacts{Trigger: trigger, Now: now, Attached: 0, AttachedKnown: false, Draining: draining}

	if s.config.Probe != nil {
		count, err := s.config.Probe(ctx)
		if err != nil {
			return PolicyDecision{Admit: false, Reason: ReasonProbeFailed}
		}

		facts.Attached, facts.AttachedKnown = count, true
	}

	if s.config.Policy == nil {
		return PolicyDecision{Admit: true, Reason: ReasonAdmitted}
	}

	return s.config.Policy(facts)
}

func (s *Scheduler) beginAttempt() func() {
	s.mu.Lock()
	s.idle = make(chan struct{})
	s.stats.Attempts++
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		close(s.idle)
		s.mu.Unlock()
	}
}

func (s *Scheduler) record(outcome AttemptOutcome, err error) {
	outcome.Err = err
	if outcome.FinishedAt.IsZero() {
		outcome.FinishedAt = s.config.Now()
		outcome.Duration = outcome.FinishedAt.Sub(outcome.StartedAt)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.last, s.lastErr = outcome, err

	if outcome.Admitted {
		s.stats.Admitted++
	} else {
		s.stats.Skipped++
	}

	if err != nil {
		s.stats.Failures++
	}
}

func (s *Scheduler) recordDrainTimeout() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stats.DrainTimeouts++
}

func waitForIdle(ctx context.Context, idle <-chan struct{}, grace time.Duration) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case <-idle:
		return true
	case <-timer.C:
	case <-ctx.Done():
	}

	return false
}
