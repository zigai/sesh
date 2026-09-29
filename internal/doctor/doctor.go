//go:build linux || darwin

package doctor

import (
	"context"
	"errors"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/zigai/gotmux/tmux"
)

const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusError   = "error"
)

const (
	ReasonUnavailable       = "unavailable"
	ReasonIncompatible      = "incompatible"
	ReasonNotConfigured     = "not_configured"
	ReasonNotFound          = "not_found"
	ReasonUninitialized     = "uninitialized"
	ReasonUnsafe            = "unsafe"
	ReasonRecoveryRequired  = "recovery_required"
	ReasonCorrupt           = "corrupt"
	ReasonIncomplete        = "incomplete"
	ReasonEmpty             = "empty"
	ReasonInspectionFailed  = "inspection_failed"
	ReasonObservational     = "observational_only"
	ReasonNotRunning        = "not_running"
	ReasonStale             = "stale"
	ReasonUnverifiable      = "unverifiable"
	ReasonInhibitorReleased = "inhibitor_released"
	ReasonLifecycleActive   = "lifecycle_active"
	ReasonInitializing      = "initializing"
)

const (
	CheckTmux               = "tmux"
	CheckSnapshotRepository = "snapshot-repository"
	CheckSnapshotCurrent    = "snapshot-current"
	CheckHistoryState       = "history-state"
	CheckCaptureGuarantee   = "capture-guarantee"
	CheckRestoreCapability  = "restore-capability"
	CheckAutosave           = "autosave"
	CheckDaemonRuntime      = "daemon-runtime"
)

type Result struct {
	Check   string `json:"check"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

type Options struct {
	Server            *tmux.Server
	SnapshotDir       string
	HistoryPath       string
	DaemonState       string
	DaemonPhase       string
	InhibitorsHeld    bool
	LogindDelayMillis int64
}

type Service struct {
	server            *tmux.Server
	snapshotDir       string
	historyPath       string
	daemonState       string
	daemonPhase       string
	inhibitorsHeld    bool
	logindDelayMillis int64
}

type outcomeLoggedError struct{ error }

func (e *outcomeLoggedError) Unwrap() error { return e.error }

func (e *outcomeLoggedError) OutcomeLogged() bool { return true }

func NewService(opts Options) *Service {
	return &Service{
		server:            opts.Server,
		snapshotDir:       opts.SnapshotDir,
		historyPath:       opts.HistoryPath,
		daemonState:       opts.DaemonState,
		daemonPhase:       opts.DaemonPhase,
		inhibitorsHeld:    opts.InhibitorsHeld,
		logindDelayMillis: opts.LogindDelayMillis,
	}
}

func Run(ctx context.Context, opts Options) ([]Result, error) {
	return NewService(opts).Run(ctx)
}

func (s *Service) Run(ctx context.Context) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		logDoctorCompleted(ctx, nil, err)

		return nil, doctorError(err)
	}

	logDoctorStarted(ctx)

	const expectedChecks = 8

	results := make([]Result, 0, expectedChecks)

	var cleanupErr error

	results = append(results, s.checkTmux(ctx))
	if err := ctx.Err(); err != nil {
		runErr := errors.Join(cleanupErr, err)
		logDoctorCompleted(ctx, results, runErr)

		return results, doctorError(runErr)
	}

	repoRes, curRes, snapErr := s.checkSnapshot(ctx)
	results = append(results, repoRes, curRes)

	if snapErr != nil {
		if errors.Is(snapErr, context.Canceled) || errors.Is(snapErr, context.DeadlineExceeded) {
			logDoctorCompleted(ctx, results, snapErr)

			return results, doctorError(snapErr)
		}

		cleanupErr = errors.Join(cleanupErr, snapErr)
	}

	if err := ctx.Err(); err != nil {
		runErr := errors.Join(cleanupErr, err)
		logDoctorCompleted(ctx, results, runErr)

		return results, doctorError(runErr)
	}

	results = append(results, s.checkHistory(ctx))
	if err := ctx.Err(); err != nil {
		runErr := errors.Join(cleanupErr, err)
		logDoctorCompleted(ctx, results, runErr)

		return results, doctorError(runErr)
	}

	results = append(results,
		checkCaptureGuarantee(),
		checkRestoreCapability(),
		s.checkDaemonRuntime(),
		checkAutosaveCapability(),
	)

	var runErr error
	if cleanupErr != nil {
		runErr = doctorError(cleanupErr)
	}

	logDoctorCompleted(ctx, results, runErr)

	return results, runErr
}

func HasErrors(results []Result) bool {
	for _, r := range results {
		if r.Status == StatusError {
			return true
		}
	}

	return false
}

func HasWarnings(results []Result) bool {
	for _, r := range results {
		if r.Status == StatusWarning {
			return true
		}
	}

	return false
}

func doctorError(err error) error {
	if err == nil {
		return nil
	}

	code := "doctor_failed"

	switch {
	case errors.Is(err, context.Canceled):
		code = "operation_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		code = "operation_timed_out"
	}

	return &outcomeLoggedError{
		error: oops.In("doctor").Code(code).With("operation", "run").Wrap(err),
	}
}

func logDoctorStarted(ctx context.Context) {
	zerolog.Ctx(ctx).Info().
		Str("component", "doctor").
		Str("operation", "run").
		Str("status", "started").
		Msg("doctor started")
}

func logDoctorCompleted(ctx context.Context, results []Result, runErr error) {
	logger := zerolog.Ctx(ctx)
	warningCount, errorCount := countResults(results)

	if runErr != nil {
		reason := "operation_failed"
		if errors.Is(runErr, context.Canceled) {
			reason = "operation_canceled"
		} else if errors.Is(runErr, context.DeadlineExceeded) {
			reason = "operation_timed_out"
		}

		logger.Error().
			Str("component", "doctor").
			Str("operation", "run").
			Str("status", "failed").
			Str("reason", reason).
			Int("check_count", len(results)).
			Int("warning_count", warningCount).
			Int("error_count", errorCount).
			Msg("doctor failed")

		return
	}

	status := "ok"
	reason := "ok"
	level := zerolog.InfoLevel

	if errorCount > 0 {
		status = "failed"
		reason = "checks_failed"
		level = zerolog.ErrorLevel
	} else if warningCount > 0 {
		status = "degraded"
		reason = "checks_degraded"
		level = zerolog.WarnLevel
	}

	logger.WithLevel(level).
		Str("component", "doctor").
		Str("operation", "run").
		Str("status", status).
		Str("reason", reason).
		Int("check_count", len(results)).
		Int("warning_count", warningCount).
		Int("error_count", errorCount).
		Msg("doctor completed")
}

func countResults(results []Result) (int, int) {
	warningCount := 0
	errorCount := 0

	for _, result := range results {
		switch result.Status {
		case StatusWarning:
			warningCount++
		case StatusError:
			errorCount++
		}
	}

	return warningCount, errorCount
}
