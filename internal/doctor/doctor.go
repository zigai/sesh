//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
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
	ReasonSaveUnfinished    = "latest_save_unfinished"
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
	CheckRestorableSnapshot = "restorable-snapshot"
	CheckServiceOOMPolicy   = "service-oom-policy"
	CheckServiceCommands    = "service-commands"
	CheckActiveRestores     = "active-restores"
	CheckCustomField        = "custom-field"
)

type Result struct {
	Check   string `json:"check"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

type Options struct {
	Server            *gotmux.Server
	SnapshotDir       string
	HistoryPath       string
	DaemonState       string
	DaemonPhase       string
	InhibitorsHeld    bool
	LogindDelayMillis int64
	// ServiceDir is the systemd user unit directory whose sesh command lines are checked; empty
	// skips the check.
	ServiceDir string
	// Executable is the running sesh executable, recognized in unit command lines besides any
	// program named sesh.
	Executable string
	// ValidateCommand reports whether this sesh accepts a command line, given without the program;
	// nil skips the check.
	ValidateCommand func(args []string) error
	CustomFields    []CustomField
}

type Service struct {
	server            *gotmux.Server
	snapshotDir       string
	historyPath       string
	daemonState       string
	daemonPhase       string
	inhibitorsHeld    bool
	logindDelayMillis int64
	serviceDir        string
	executable        string
	validateCommand   func(args []string) error
	customFields      []CustomField
	unitOf            func(pid int) (string, error)
	oomPolicyOf       func(ctx context.Context, unit string) (string, error)
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
		serviceDir:        opts.ServiceDir,
		executable:        opts.Executable,
		validateCommand:   opts.ValidateCommand,
		customFields:      opts.CustomFields,
		unitOf:            serviceUnitOf,
		oomPolicyOf:       serviceOOMPolicyOf,
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

	tmuxRes, serverPID := s.checkTmux(ctx)
	results = append(results, tmuxRes)

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

	results = append(results, s.checkRestorable(ctx, tmuxRes)...)
	results = append(results, s.checkServiceOOMPolicy(ctx, serverPID)...)
	results = append(results, s.checkServiceCommands()...)
	results = append(results, s.checkActiveRestores(ctx)...)
	results = append(results, checkCustomFields(s.customFields)...)

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

	event := logger.WithLevel(level).
		Str("component", "doctor").
		Str("operation", "run").
		Str("status", status).
		Str("reason", reason).
		Int("check_count", len(results)).
		Int("warning_count", warningCount).
		Int("error_count", errorCount)

	if errorCount > 0 {
		event = event.Str("error_checks", checksWithStatus(results, StatusError))
	}

	if warningCount > 0 {
		event = event.Str("warning_checks", checksWithStatus(results, StatusWarning))
	}

	event.Msg("doctor completed")
}

func checksWithStatus(results []Result, status string) string {
	checks := make([]string, 0, len(results))

	for _, result := range results {
		if result.Status == status {
			checks = append(checks, result.Check)
		}
	}

	slices.Sort(checks)

	return strings.Join(slices.Compact(checks), ",")
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
