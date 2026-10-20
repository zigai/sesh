//go:build linux || darwin

package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/logging"
)

const (
	EventSessionSwitchPre  = "session_switch_pre"
	EventSessionSwitchPost = "session_switch_post"
	EventSessionAttachPre  = "session_attach_pre"
	EventSessionAttachPost = "session_attach_post"
	EventTmuxStartupPost   = "tmux_startup_post"
)

const (
	DefaultHookTimeout  = 30 * time.Second
	MaxHookCount        = 64
	hookEnvEntriesCount = 8
)

const (
	HookStatusCompleted HookStatus = "completed"
	HookStatusFailed    HookStatus = "failed"
	HookStatusTimedOut  HookStatus = "timed_out"
	HookStatusCanceled  HookStatus = "canceled"
)

var (
	ErrInvalidHookEvent           = errors.New("invalid hook event")
	ErrInvalidHookConfig          = errors.New("invalid hook config")
	ErrHookTimeoutInvalid         = errors.New("invalid hook timeout")
	ErrHookLimitExceeded          = errors.New("hook limit exceeded")
	ErrHookCommandEmpty           = errors.New("hook command cannot be empty")
	ErrHookExecution              = errors.New("hook execution failed")
	ErrHookSupervisionUnsupported = errors.New("process group hook supervision unsupported on this platform")
)

type HookStatus string

type HookOutcome struct {
	Index    int
	Status   HookStatus
	ExitCode int
	Duration time.Duration
	Err      error
}

type HookResult struct {
	Event     string
	Attempted int
	Completed int
	Failed    int
	TimedOut  int
	Outcomes  []HookOutcome
}

type HookEvent struct {
	Name          string
	Phase         string
	Action        string
	PrevSession   string
	Session       string
	PrevSessionID string
	SessionID     string
	Source        string
}

type HookRunnerDeps struct {
	Now    func() time.Time
	Getenv func(string) string
}

type HookRunner struct {
	now    func() time.Time
	getenv func(string) string
}

func NewHookRunner(deps HookRunnerDeps) *HookRunner {
	if deps.Now == nil {
		deps.Now = time.Now
	}

	if deps.Getenv == nil {
		deps.Getenv = os.Getenv
	}

	return &HookRunner{
		now:    deps.Now,
		getenv: deps.Getenv,
	}
}

func RunHooks(ctx context.Context, cfg config.HookConfig, event HookEvent) (HookResult, error) {
	return NewHookRunner(HookRunnerDeps{
		Now:    nil,
		Getenv: nil,
	}).Run(ctx, cfg, event)
}

func CommandsForHookEvent(cfg config.HookConfig, eventName string) []config.HookCommand {
	switch eventName {
	case EventSessionSwitchPre:
		return cfg.SessionSwitchPre
	case EventSessionSwitchPost:
		return cfg.SessionSwitchPost
	case EventSessionAttachPre:
		return cfg.SessionAttachPre
	case EventSessionAttachPost:
		return cfg.SessionAttachPost
	case EventTmuxStartupPost:
		return cfg.TmuxStartupPost
	default:
		return nil
	}
}

func HookEventDefaults(eventName string) (string, string) {
	switch eventName {
	case EventSessionSwitchPre:
		return "switch", "pre"
	case EventSessionSwitchPost:
		return "switch", "post"
	case EventSessionAttachPre:
		return "attach", "pre"
	case EventSessionAttachPost:
		return "attach", "post"
	case EventTmuxStartupPost:
		return "startup", "post"
	default:
		return "", ""
	}
}

func ValidateHookEvent(event *HookEvent) error {
	if event == nil {
		return oops.In("session").
			Code("invalid_hook_event").
			With("operation", "validate_event").
			Wrap(ErrInvalidHookEvent)
	}

	actionDef, phaseDef := HookEventDefaults(event.Name)
	if actionDef == "" || phaseDef == "" {
		return oops.In("session").
			Code("invalid_hook_event").
			With("operation", "validate_event").
			Wrap(ErrInvalidHookEvent)
	}

	if event.Action == "" {
		event.Action = actionDef
	} else if event.Action != actionDef {
		return oops.In("session").
			Code("invalid_hook_event").
			With("operation", "validate_event").
			Wrap(ErrInvalidHookEvent)
	}

	if event.Phase == "" {
		event.Phase = phaseDef
	} else if event.Phase != phaseDef {
		return oops.In("session").
			Code("invalid_hook_event").
			With("operation", "validate_event").
			Wrap(ErrInvalidHookEvent)
	}

	return nil
}

func ValidateHookConfig(cfg config.HookConfig, eventName string) error {
	if cfg.Timeout != "" {
		d, err := time.ParseDuration(cfg.Timeout)
		if err != nil || d <= 0 {
			return oops.In("session").
				Code("invalid_hook_config").
				With("operation", "validate_config").
				Wrap(ErrHookTimeoutInvalid)
		}
	}

	commands := CommandsForHookEvent(cfg, eventName)
	if len(commands) > MaxHookCount {
		return oops.In("session").
			Code("invalid_hook_config").
			With("operation", "validate_config").
			With("hook_count", len(commands)).
			Wrap(ErrHookLimitExceeded)
	}

	for i, cmd := range commands {
		if err := validateHookCommandEntry(cmd, i); err != nil {
			return err
		}
	}

	return nil
}

func ResolveHookTimeout(globalTimeoutStr, hookTimeoutStr string) (time.Duration, error) {
	if hookTimeoutStr != "" {
		d, err := time.ParseDuration(hookTimeoutStr)
		if err != nil || d <= 0 {
			return 0, oops.In("session").
				Code("invalid_hook_config").
				With("operation", "resolve_timeout").
				Wrap(ErrHookTimeoutInvalid)
		}

		return d, nil
	}

	if globalTimeoutStr != "" {
		d, err := time.ParseDuration(globalTimeoutStr)
		if err != nil || d <= 0 {
			return 0, oops.In("session").
				Code("invalid_hook_config").
				With("operation", "resolve_timeout").
				Wrap(ErrHookTimeoutInvalid)
		}

		return d, nil
	}

	return DefaultHookTimeout, nil
}

func HookEventEnv(base []string, event HookEvent) []string {
	keysToOmit := map[string]struct{}{
		"SESH_HOOK_EVENT":      {},
		"SESH_HOOK_PHASE":      {},
		"SESH_HOOK_ACTION":     {},
		"SESH_HOOK_SOURCE":     {},
		"SESH_SESSION":         {},
		"SESH_SESSION_ID":      {},
		"SESH_PREV_SESSION":    {},
		"SESH_PREV_SESSION_ID": {},
	}

	env := make([]string, 0, len(base)+hookEnvEntriesCount)

	for _, entry := range base {
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 {
			continue
		}

		key := entry[:eq]
		if _, exists := keysToOmit[key]; exists {
			continue
		}

		env = append(env, entry)
	}

	env = append(
		env,
		"SESH_HOOK_EVENT="+event.Name,
		"SESH_HOOK_PHASE="+event.Phase,
		"SESH_HOOK_ACTION="+event.Action,
		"SESH_HOOK_SOURCE="+event.Source,
		"SESH_SESSION="+event.Session,
		"SESH_SESSION_ID="+event.SessionID,
		"SESH_PREV_SESSION="+event.PrevSession,
		"SESH_PREV_SESSION_ID="+event.PrevSessionID,
	)

	return env
}

func (r *HookRunner) Run(ctx context.Context, cfg config.HookConfig, event HookEvent) (HookResult, error) {
	if err := ctx.Err(); err != nil {
		canceledErr := oops.In("session").
			Code("operation_canceled").
			With("operation", "execute").
			Wrap(err)

		return HookResult{
			Event:     event.Name,
			Attempted: 0,
			Completed: 0,
			Failed:    0,
			TimedOut:  0,
			Outcomes:  nil,
		}, canceledErr
	}

	if err := ValidateHookEvent(&event); err != nil {
		return HookResult{
			Event:     event.Name,
			Attempted: 0,
			Completed: 0,
			Failed:    0,
			TimedOut:  0,
			Outcomes:  nil,
		}, err
	}

	if err := ValidateHookConfig(cfg, event.Name); err != nil {
		return HookResult{
			Event:     event.Name,
			Attempted: 0,
			Completed: 0,
			Failed:    0,
			TimedOut:  0,
			Outcomes:  nil,
		}, err
	}

	commands := CommandsForHookEvent(cfg, event.Name)
	result := HookResult{
		Event:     event.Name,
		Attempted: 0,
		Completed: 0,
		Failed:    0,
		TimedOut:  0,
		Outcomes:  make([]HookOutcome, 0, len(commands)),
	}

	if len(commands) == 0 {
		return result, nil
	}

	if err := checkPlatformSupervision(); err != nil {
		return result, oops.In("session").
			Code("supervision_unsupported").
			With("operation", "execute").
			Wrap(err)
	}

	for i, hook := range commands {
		if err := ctx.Err(); err != nil {
			if result.Attempted > 0 {
				zerolog.Ctx(ctx).Error().Str("component", "hooks").Str("operation", "execute").
					Str("hook_event", event.Name).Int("hook_index", i).Str("status", "failed").
					Str("reason", "canceled").Msg("hook sequence canceled")
			}

			return result, oops.In("session").
				Code("operation_canceled").
				With("operation", "execute").
				With("hook_index", i).
				Wrap(err)
		}

		timeout, err := ResolveHookTimeout(cfg.Timeout, hook.Timeout)
		if err != nil {
			return result, err
		}

		result.Attempted++

		outcome, abortErr := r.runCommand(ctx, event, hook, i, timeout)
		result.Outcomes = append(result.Outcomes, outcome)
		recordOutcome(&result, outcome.Status)

		if abortErr != nil {
			return result, abortErr
		}
	}

	return result, nil
}

func (r HookResult) Err() error {
	var errs []error

	for _, o := range r.Outcomes {
		if o.Err != nil {
			errs = append(errs, o.Err)
		}
	}

	return errors.Join(errs...)
}

func (r HookResult) Causes() []error {
	var causes []error

	for _, o := range r.Outcomes {
		if o.Err != nil {
			causes = append(causes, o.Err)
		}
	}

	return causes
}

func recordOutcome(result *HookResult, status HookStatus) {
	switch status {
	case HookStatusCompleted:
		result.Completed++
	case HookStatusTimedOut:
		result.TimedOut++
	case HookStatusFailed, HookStatusCanceled:
		result.Failed++
	}
}

func validateHookCommandEntry(cmd config.HookCommand, index int) error {
	if cmd.Timeout != "" {
		d, err := time.ParseDuration(cmd.Timeout)
		if err != nil || d <= 0 {
			return oops.In("session").
				Code("invalid_hook_config").
				With("operation", "validate_config").
				With("hook_index", index).
				Wrap(ErrHookTimeoutInvalid)
		}
	}

	hasShell := strings.TrimSpace(cmd.ShellCommand) != ""
	hasArgv := strings.TrimSpace(cmd.Command) != ""

	if !hasShell && !hasArgv {
		return oops.In("session").
			Code("invalid_hook_config").
			With("operation", "validate_config").
			With("hook_index", index).
			Wrap(ErrHookCommandEmpty)
	}

	if hasShell && hasArgv {
		return oops.In("session").
			Code("invalid_hook_config").
			With("operation", "validate_config").
			With("hook_index", index).
			Wrap(ErrInvalidHookConfig)
	}

	return nil
}

func (r *HookRunner) runCommand(
	ctx context.Context,
	event HookEvent,
	hook config.HookCommand,
	index int,
	timeout time.Duration,
) (HookOutcome, error) {
	cmd := r.buildCmd(ctx, hook)
	cmd.Env = HookEventEnv(os.Environ(), event)

	logger := zerolog.Ctx(ctx)
	logger.Info().
		Str("component", "hooks").
		Str("operation", "execute").
		Str("hook_event", event.Name).
		Int("hook_index", index).
		Str("status", "started").
		Int64("timeout_ms", timeout.Milliseconds()).
		Msg("hook started")

	status, elapsed, runErr := runSupervisedCommand(ctx, cmd, timeout, r.now)
	if runErr != nil {
		runErr = oops.In("session").Code("hook_execution_failed").With("operation", "execute").
			With("phase", string(status)).With("hook_index", index).Wrap(runErr)
	}

	switch status {
	case HookStatusCanceled:
		exitCode := 1
		if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
			exitCode = exitErr.ExitCode()
		}

		logger.Error().
			Str("component", "hooks").
			Str("operation", "execute").
			Str("hook_event", event.Name).
			Int("hook_index", index).
			Str("status", "failed").
			Str("reason", "canceled").
			Func(logging.Cause(runErr)).
			Int64("timeout_ms", timeout.Milliseconds()).
			Int64("duration_ms", elapsed.Milliseconds()).
			Msg("hook canceled")

		return HookOutcome{
			Index:    index,
			Status:   HookStatusCanceled,
			ExitCode: exitCode,
			Duration: elapsed,
			Err:      runErr,
		}, oops.In("session").Code("operation_canceled").With("operation", "execute").With("hook_index", index).Wrap(runErr)

	case HookStatusTimedOut:
		logger.Warn().
			Str("component", "hooks").
			Str("operation", "execute").
			Str("hook_event", event.Name).
			Int("hook_index", index).
			Str("status", "failed").
			Str("reason", "deadline_exceeded").
			Int64("timeout_ms", timeout.Milliseconds()).
			Int64("duration_ms", elapsed.Milliseconds()).
			Msg("hook timed out")

		return HookOutcome{
			Index:    index,
			Status:   HookStatusTimedOut,
			ExitCode: -1,
			Duration: elapsed,
			Err:      runErr,
		}, nil

	case HookStatusFailed:
		exitCode, reason := -1, "start_failed"
		if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
			exitCode, reason = exitErr.ExitCode(), "execution_failed"
		}

		logger.Warn().
			Str("component", "hooks").
			Str("operation", "execute").
			Str("hook_event", event.Name).
			Int("hook_index", index).
			Str("status", "failed").
			Str("reason", reason).
			Int("exit_code", exitCode).
			Func(logging.Cause(runErr)).
			Int64("timeout_ms", timeout.Milliseconds()).
			Int64("duration_ms", elapsed.Milliseconds()).
			Msg("hook failed")

		return HookOutcome{
			Index:    index,
			Status:   HookStatusFailed,
			ExitCode: exitCode,
			Duration: elapsed,
			Err:      runErr,
		}, nil

	case HookStatusCompleted:
		logger.Info().
			Str("component", "hooks").
			Str("operation", "execute").
			Str("hook_event", event.Name).
			Int("hook_index", index).
			Str("status", "ok").
			Int64("duration_ms", elapsed.Milliseconds()).
			Msg("hook completed")

		return HookOutcome{
			Index:    index,
			Status:   HookStatusCompleted,
			ExitCode: 0,
			Duration: elapsed,
			Err:      nil,
		}, nil
	}

	return HookOutcome{
		Index:    index,
		Status:   HookStatusFailed,
		ExitCode: 1,
		Duration: 0,
		Err:      nil,
	}, nil
}

func (r *HookRunner) buildCmd(ctx context.Context, hook config.HookCommand) *exec.Cmd {
	var cmd *exec.Cmd

	switch {
	case hook.ShellCommand != "":
		shell := strings.TrimSpace(r.getenv("SHELL"))
		if shell == "" {
			shell = "/bin/sh"
		}

		cmd = exec.CommandContext(ctx, shell, "-lc", hook.ShellCommand)
	default:
		cmd = exec.CommandContext(ctx, hook.Command, hook.Args...)
	}

	cmd.Cancel = nil
	cmd.WaitDelay = 0

	return cmd
}
