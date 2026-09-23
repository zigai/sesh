//go:build linux || darwin

package session_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func hookLogContext(ctx context.Context, buf *bytes.Buffer) context.Context {
	logger := zerolog.New(buf)
	return logger.WithContext(ctx)
}

func TestHookCommandsForHookEvent(t *testing.T) {
	cmd1 := config.HookCommand{Command: "echo 1"}
	cmd2 := config.HookCommand{Command: "echo 2"}
	cmd3 := config.HookCommand{Command: "echo 3"}
	cmd4 := config.HookCommand{Command: "echo 4"}
	cmd5 := config.HookCommand{Command: "echo 5"}

	cfg := config.HookConfig{
		SessionSwitchPre:  []config.HookCommand{cmd1},
		SessionSwitchPost: []config.HookCommand{cmd2},
		SessionAttachPre:  []config.HookCommand{cmd3},
		SessionAttachPost: []config.HookCommand{cmd4},
		TmuxStartupPost:   []config.HookCommand{cmd5},
	}

	tests := []struct {
		event string
		want  []config.HookCommand
	}{
		{session.EventSessionSwitchPre, []config.HookCommand{cmd1}},
		{session.EventSessionSwitchPost, []config.HookCommand{cmd2}},
		{session.EventSessionAttachPre, []config.HookCommand{cmd3}},
		{session.EventSessionAttachPost, []config.HookCommand{cmd4}},
		{session.EventTmuxStartupPost, []config.HookCommand{cmd5}},
		{"unknown_event", nil},
	}

	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			got := session.CommandsForHookEvent(cfg, tt.event)
			if len(got) != len(tt.want) {
				t.Fatalf("CommandsForHookEvent(%q) len = %d, want %d", tt.event, len(got), len(tt.want))
			}

			if len(got) > 0 && got[0].Command != tt.want[0].Command {
				t.Fatalf("CommandsForHookEvent(%q)[0] = %q, want %q", tt.event, got[0].Command, tt.want[0].Command)
			}
		})
	}
}

func TestHookEventDefaults(t *testing.T) {
	tests := []struct {
		event      string
		wantAction string
		wantPhase  string
	}{
		{session.EventSessionSwitchPre, "switch", "pre"},
		{session.EventSessionSwitchPost, "switch", "post"},
		{session.EventSessionAttachPre, "attach", "pre"},
		{session.EventSessionAttachPost, "attach", "post"},
		{session.EventTmuxStartupPost, "startup", "post"},
		{"invalid_event", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			action, phase := session.HookEventDefaults(tt.event)
			if action != tt.wantAction || phase != tt.wantPhase {
				t.Fatalf("HookEventDefaults(%q) = (%q, %q), want (%q, %q)",
					tt.event, action, phase, tt.wantAction, tt.wantPhase)
			}
		})
	}
}

func TestHookEventValidation(t *testing.T) {
	t.Run("nil_event", func(t *testing.T) {
		err := session.ValidateHookEvent(nil)
		if !errors.Is(err, session.ErrInvalidHookEvent) {
			t.Fatalf("ValidateHookEvent(nil) = %v, want ErrInvalidHookEvent", err)
		}
	})

	t.Run("unknown_event_name", func(t *testing.T) {
		ev := &session.HookEvent{Name: "unknown"}

		err := session.ValidateHookEvent(ev)
		if !errors.Is(err, session.ErrInvalidHookEvent) {
			t.Fatalf("ValidateHookEvent(unknown) = %v, want ErrInvalidHookEvent", err)
		}
	})

	t.Run("populates_defaults_when_empty", func(t *testing.T) {
		ev := &session.HookEvent{Name: session.EventSessionSwitchPost}
		if err := session.ValidateHookEvent(ev); err != nil {
			t.Fatalf("ValidateHookEvent() unexpected error: %v", err)
		}

		if ev.Action != "switch" {
			t.Fatalf("ev.Action = %q, want switch", ev.Action)
		}

		if ev.Phase != "post" {
			t.Fatalf("ev.Phase = %q, want post", ev.Phase)
		}
	})

	t.Run("accepts_matching_explicit_action_and_phase", func(t *testing.T) {
		ev := &session.HookEvent{
			Name:   session.EventSessionAttachPre,
			Action: "attach",
			Phase:  "pre",
		}
		if err := session.ValidateHookEvent(ev); err != nil {
			t.Fatalf("ValidateHookEvent() unexpected error: %v", err)
		}
	})

	t.Run("rejects_mismatched_action", func(t *testing.T) {
		ev := &session.HookEvent{
			Name:   session.EventSessionAttachPre,
			Action: "wrong_action",
			Phase:  "pre",
		}

		err := session.ValidateHookEvent(ev)
		if !errors.Is(err, session.ErrInvalidHookEvent) {
			t.Fatalf("ValidateHookEvent() error = %v, want ErrInvalidHookEvent", err)
		}
	})

	t.Run("rejects_mismatched_phase", func(t *testing.T) {
		ev := &session.HookEvent{
			Name:   session.EventSessionAttachPre,
			Action: "attach",
			Phase:  "wrong_phase",
		}

		err := session.ValidateHookEvent(ev)
		if !errors.Is(err, session.ErrInvalidHookEvent) {
			t.Fatalf("ValidateHookEvent() error = %v, want ErrInvalidHookEvent", err)
		}
	})
}

func TestHookConfigValidationCommands(t *testing.T) {
	t.Run("valid_empty_config", func(t *testing.T) {
		cfg := config.HookConfig{}
		if err := session.ValidateHookConfig(cfg, session.EventSessionSwitchPre); err != nil {
			t.Fatalf("ValidateHookConfig(empty) error = %v, want nil", err)
		}
	})

	t.Run("valid_shell_command", func(t *testing.T) {
		cfg := config.HookConfig{
			SessionSwitchPre: []config.HookCommand{
				{ShellCommand: "echo hello"},
			},
		}
		if err := session.ValidateHookConfig(cfg, session.EventSessionSwitchPre); err != nil {
			t.Fatalf("ValidateHookConfig(shell) error = %v, want nil", err)
		}
	})

	t.Run("valid_argv_command", func(t *testing.T) {
		cfg := config.HookConfig{
			SessionSwitchPre: []config.HookCommand{
				{Command: "echo", Args: []string{"hello"}},
			},
		}
		if err := session.ValidateHookConfig(cfg, session.EventSessionSwitchPre); err != nil {
			t.Fatalf("ValidateHookConfig(argv) error = %v, want nil", err)
		}
	})

	t.Run("empty_command_specification", func(t *testing.T) {
		cfg := config.HookConfig{
			SessionSwitchPost: []config.HookCommand{
				{Timeout: "5s"},
			},
		}

		err := session.ValidateHookConfig(cfg, session.EventSessionSwitchPost)
		if !errors.Is(err, session.ErrHookCommandEmpty) {
			t.Fatalf("ValidateHookConfig(empty command) = %v, want ErrHookCommandEmpty", err)
		}
	})

	t.Run("both_shell_and_argv_specified", func(t *testing.T) {
		cfg := config.HookConfig{
			SessionSwitchPost: []config.HookCommand{
				{ShellCommand: "echo 1", Command: "echo", Args: []string{"1"}},
			},
		}

		err := session.ValidateHookConfig(cfg, session.EventSessionSwitchPost)
		if !errors.Is(err, session.ErrInvalidHookConfig) {
			t.Fatalf("ValidateHookConfig(both) = %v, want ErrInvalidHookConfig", err)
		}
	})

	t.Run("exceeds_max_hook_count", func(t *testing.T) {
		cmds := make([]config.HookCommand, session.MaxHookCount+1)
		for i := range cmds {
			cmds[i] = config.HookCommand{Command: "echo"}
		}

		cfg := config.HookConfig{
			SessionSwitchPost: cmds,
		}

		err := session.ValidateHookConfig(cfg, session.EventSessionSwitchPost)
		if !errors.Is(err, session.ErrHookLimitExceeded) {
			t.Fatalf("ValidateHookConfig(exceeded) = %v, want ErrHookLimitExceeded", err)
		}
	})
}

func TestHookConfigValidationTimeouts(t *testing.T) {
	t.Run("invalid_global_timeout", func(t *testing.T) {
		cfg := config.HookConfig{Timeout: "invalid"}

		err := session.ValidateHookConfig(cfg, session.EventSessionSwitchPre)
		if !errors.Is(err, session.ErrHookTimeoutInvalid) {
			t.Fatalf("ValidateHookConfig(invalid timeout) = %v, want ErrHookTimeoutInvalid", err)
		}
	})

	t.Run("negative_global_timeout", func(t *testing.T) {
		cfg := config.HookConfig{Timeout: "-5s"}

		err := session.ValidateHookConfig(cfg, session.EventSessionSwitchPre)
		if !errors.Is(err, session.ErrHookTimeoutInvalid) {
			t.Fatalf("ValidateHookConfig(-5s) = %v, want ErrHookTimeoutInvalid", err)
		}
	})

	t.Run("zero_global_timeout", func(t *testing.T) {
		cfg := config.HookConfig{Timeout: "0s"}

		err := session.ValidateHookConfig(cfg, session.EventSessionSwitchPre)
		if !errors.Is(err, session.ErrHookTimeoutInvalid) {
			t.Fatalf("ValidateHookConfig(0s) = %v, want ErrHookTimeoutInvalid", err)
		}
	})

	t.Run("invalid_command_timeout", func(t *testing.T) {
		cfg := config.HookConfig{
			SessionSwitchPost: []config.HookCommand{
				{Command: "echo", Timeout: "not_a_duration"},
			},
		}

		err := session.ValidateHookConfig(cfg, session.EventSessionSwitchPost)
		if !errors.Is(err, session.ErrHookTimeoutInvalid) {
			t.Fatalf("ValidateHookConfig(cmd invalid timeout) = %v, want ErrHookTimeoutInvalid", err)
		}
	})
}

func TestHookResolveTimeout(t *testing.T) {
	t.Run("hook_timeout_precedence", func(t *testing.T) {
		d, err := session.ResolveHookTimeout("10s", "5s")
		if err != nil || d != 5*time.Second {
			t.Fatalf("ResolveHookTimeout(10s, 5s) = (%v, %v), want (5s, nil)", d, err)
		}
	})

	t.Run("global_timeout_fallback", func(t *testing.T) {
		d, err := session.ResolveHookTimeout("10s", "")
		if err != nil || d != 10*time.Second {
			t.Fatalf("ResolveHookTimeout(10s, \"\") = (%v, %v), want (10s, nil)", d, err)
		}
	})

	t.Run("default_timeout_fallback", func(t *testing.T) {
		d, err := session.ResolveHookTimeout("", "")
		if err != nil || d != session.DefaultHookTimeout {
			t.Fatalf("ResolveHookTimeout(\"\", \"\") = (%v, %v), want (%v, nil)", d, err, session.DefaultHookTimeout)
		}
	})

	t.Run("rejects_invalid_hook_timeout", func(t *testing.T) {
		_, err := session.ResolveHookTimeout("10s", "invalid")
		if !errors.Is(err, session.ErrHookTimeoutInvalid) {
			t.Fatalf("ResolveHookTimeout() err = %v, want ErrHookTimeoutInvalid", err)
		}
	})

	t.Run("rejects_invalid_global_timeout", func(t *testing.T) {
		_, err := session.ResolveHookTimeout("invalid", "")
		if !errors.Is(err, session.ErrHookTimeoutInvalid) {
			t.Fatalf("ResolveHookTimeout() err = %v, want ErrHookTimeoutInvalid", err)
		}
	})
}

func TestHookEventEnv(t *testing.T) {
	base := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/testuser",
		"SESH_HOOK_EVENT=old_event",
		"SESH_SESSION=old_session",
		"CUSTOM_HOOK_EVENT=custom_event",
		"CUSTOM_SESSION=custom_session",
		"USER=testuser",
	}

	ev := session.HookEvent{
		Name:          session.EventSessionSwitchPost,
		Phase:         "post",
		Action:        "switch",
		Source:        "cli",
		Session:       "target_sess",
		SessionID:     "$1",
		PrevSession:   "prev_sess",
		PrevSessionID: "$0",
	}

	env := session.HookEventEnv(base, ev)

	envMap := make(map[string]string)
	counts := make(map[string]int)

	for _, entry := range env {
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 {
			continue
		}

		k := entry[:eq]
		v := entry[eq+1:]
		envMap[k] = v
		counts[k]++
	}

	if envMap["PATH"] != "/usr/bin:/bin" || envMap["HOME"] != "/home/testuser" || envMap["USER"] != "testuser" {
		t.Fatalf("standard env not preserved: %#v", envMap)
	}

	wantKeys := map[string]string{
		"SESH_HOOK_EVENT":      session.EventSessionSwitchPost,
		"SESH_HOOK_PHASE":      "post",
		"SESH_HOOK_ACTION":     "switch",
		"SESH_HOOK_SOURCE":     "cli",
		"SESH_SESSION":         "target_sess",
		"SESH_SESSION_ID":      "$1",
		"SESH_PREV_SESSION":    "prev_sess",
		"SESH_PREV_SESSION_ID": "$0",
	}

	for k, wantV := range wantKeys {
		if gotV := envMap[k]; gotV != wantV {
			t.Fatalf("env[%q] = %q, want %q", k, gotV, wantV)
		}

		if counts[k] != 1 {
			t.Fatalf("duplicate key in env: %q (count %d)", k, counts[k])
		}
	}

	if envMap["CUSTOM_HOOK_EVENT"] != "custom_event" {
		t.Fatalf("unrelated CUSTOM_HOOK_EVENT was stripped, got %q", envMap["CUSTOM_HOOK_EVENT"])
	}

	if envMap["CUSTOM_SESSION"] != "custom_session" {
		t.Fatalf("unrelated CUSTOM_SESSION was stripped, got %q", envMap["CUSTOM_SESSION"])
	}
}

func TestHookRunnerCanceledBeforeFirstLaunch(t *testing.T) {
	cfg := config.HookConfig{
		SessionSwitchPre: []config.HookCommand{
			{Command: "echo", Args: []string{"hello"}},
		},
	}

	var logOutput bytes.Buffer

	baseCtx := hookLogContext(t.Context(), &logOutput)
	ctx, cancel := context.WithCancel(baseCtx)
	cancel()

	runner := session.NewHookRunner(session.HookRunnerDeps{})
	res, err := runner.Run(ctx, cfg, session.HookEvent{Name: session.EventSessionSwitchPre})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() err = %v, want context.Canceled", err)
	}

	if res.Attempted != 0 {
		t.Fatalf("res.Attempted = %d, want 0", res.Attempted)
	}

	if logOutput.Len() != 0 {
		t.Fatalf("expected 0 log records on pre-launch cancellation, got: %s", logOutput.String())
	}
}

func TestHookRunnerInvalidEventNoLogs(t *testing.T) {
	var logOutput bytes.Buffer

	ctx := hookLogContext(t.Context(), &logOutput)

	runner := session.NewHookRunner(session.HookRunnerDeps{})
	res, err := runner.Run(ctx, config.HookConfig{}, session.HookEvent{Name: "invalid_event"})

	if !errors.Is(err, session.ErrInvalidHookEvent) {
		t.Fatalf("Run() err = %v, want ErrInvalidHookEvent", err)
	}

	if res.Attempted != 0 {
		t.Fatalf("res.Attempted = %d, want 0", res.Attempted)
	}

	if logOutput.Len() != 0 {
		t.Fatalf("expected 0 log records on invalid event, got: %s", logOutput.String())
	}
}

func TestHookRunnerInvalidConfigNoLogs(t *testing.T) {
	cfg := config.HookConfig{
		Timeout: "-10s",
	}

	var logOutput bytes.Buffer

	ctx := hookLogContext(t.Context(), &logOutput)

	runner := session.NewHookRunner(session.HookRunnerDeps{})
	res, err := runner.Run(ctx, cfg, session.HookEvent{Name: session.EventSessionSwitchPre})

	if !errors.Is(err, session.ErrHookTimeoutInvalid) {
		t.Fatalf("Run() err = %v, want ErrHookTimeoutInvalid", err)
	}

	if res.Attempted != 0 {
		t.Fatalf("res.Attempted = %d, want 0", res.Attempted)
	}

	if logOutput.Len() != 0 {
		t.Fatalf("expected 0 log records on invalid config, got: %s", logOutput.String())
	}
}

func TestHookOopsErrorInspection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	runner := session.NewHookRunner(session.HookRunnerDeps{})
	_, err := runner.Run(ctx, config.HookConfig{}, session.HookEvent{Name: session.EventSessionSwitchPre})

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("oops.AsOops(err) = false; want oops error")
	}

	if oopsErr.Domain() != "session" {
		t.Fatalf("oopsErr.Domain() = %q, want session", oopsErr.Domain())
	}

	if oopsErr.Code() != "operation_canceled" {
		t.Fatalf("oopsErr.Code() = %q, want operation_canceled", oopsErr.Code())
	}
}
