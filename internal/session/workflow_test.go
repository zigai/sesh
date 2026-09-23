package session_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/session"
)

func TestWorkflow_ErrorSentinels(t *testing.T) {
	t.Parallel()

	sentinels := map[string]error{
		"ErrSessionAlreadyExists":      session.ErrSessionAlreadyExists,
		"ErrSessionNotFound":           session.ErrSessionNotFound,
		"ErrSessionAttached":           session.ErrSessionAttached,
		"ErrInvalidSessionName":        session.ErrInvalidSessionName,
		"ErrNotInsideTmux":             session.ErrNotInsideTmux,
		"ErrInvokingClientUnavailable": session.ErrInvokingClientUnavailable,
		"ErrInvokingClientAmbiguous":   session.ErrInvokingClientAmbiguous,
		"ErrSwitchUnverified":          session.ErrSwitchUnverified,
		"ErrTerminalRequired":          session.ErrTerminalRequired,
		"ErrSavedGenerationsProtected": session.ErrSavedGenerationsProtected,
		"ErrSavedSessionScopeConflict": session.ErrSavedSessionScopeConflict,
		"ErrSessionReplaced":           session.ErrSessionReplaced,
	}

	for name, sentinel := range sentinels {
		if sentinel == nil {
			t.Errorf("expected %s", name)
		}
	}
}

func TestWorkflow_EnterActionNames(t *testing.T) {
	t.Parallel()

	tests := map[session.EnterAction]string{
		session.EnterNone:        "none",
		session.EnterSwitch:      "switch",
		session.EnterAttach:      "attach",
		session.EnterAction(200): "unknown",
	}

	for action, want := range tests {
		if got := action.String(); got != want {
			t.Errorf("EnterAction(%d).String() = %q, want %q", action, got, want)
		}
	}
}

func TestWorkflow_CreateValidation(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	wf := session.NewWorkflow(nil, nil, "")

	for _, name := range []string{"", "has:colon", "has.period"} {
		if _, err := wf.CreateSession(ctx, name, "/tmp"); !errors.Is(err, session.ErrInvalidSessionName) {
			t.Fatalf("CreateSession(%q) error = %v, want ErrInvalidSessionName", name, err)
		}
	}
}

func TestWorkflow_RenameValidation(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	wf := session.NewWorkflow(nil, nil, "")

	for _, name := range []string{"", "invalid:name"} {
		if _, err := wf.RenameSessionOutcome(ctx, "old", name); !errors.Is(err, session.ErrInvalidSessionName) {
			t.Fatalf("RenameSessionOutcome(%q) error = %v, want ErrInvalidSessionName", name, err)
		}
	}
}

func TestWorkflow_EnterValidation(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	wf := session.NewWorkflow(nil, nil, "")

	if _, err := wf.EnterOrCreateSession(ctx, "has:colon", "/tmp"); !errors.Is(err, session.ErrInvalidSessionName) {
		t.Fatalf("EnterOrCreateSession error = %v, want ErrInvalidSessionName", err)
	}
}

func TestWorkflow_RequiresEndpointBeforeTmuxWork(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	wf := session.NewWorkflow(nil, nil, "")

	operations := map[string]func() error{
		"create": func() error {
			_, err := wf.CreateSession(ctx, "dev", "/tmp")

			return wrapWorkflowCall("create", err)
		},
		"enter": func() error {
			_, err := wf.EnterOrCreateSession(ctx, "dev", "/tmp")

			return wrapWorkflowCall("enter", err)
		},
		"switch": func() error {
			return wrapWorkflowCall("switch", wf.SwitchSession(ctx, "dev"))
		},
		"attach": func() error {
			_, err := wf.AttachSession(ctx, "dev")

			return wrapWorkflowCall("attach", err)
		},
		"rename": func() error {
			return wrapWorkflowCall("rename", wf.RenameSession(ctx, "dev", "renamed"))
		},
		"kill": func() error {
			return wrapWorkflowCall("kill", wf.KillSession(ctx, "dev", false))
		},
		"delete": func() error {
			_, err := wf.DeleteSessionEverywhere(ctx, "dev", false)

			return wrapWorkflowCall("delete", err)
		},
	}

	for name, operation := range operations {
		err := operation()
		if err == nil {
			t.Fatalf("%s succeeded without a tmux endpoint", name)
		}

		if code := workflowErrorCode(err); code != "endpoint_required" {
			t.Fatalf("%s error code = %q, want endpoint_required", name, code)
		}
	}
}

func wrapWorkflowCall(operation string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s: %w", operation, err)
}

func workflowErrorCode(err error) string {
	structured, ok := oops.AsOops(err)
	if !ok {
		return ""
	}

	code, _ := structured.Code().(string)

	return code
}
