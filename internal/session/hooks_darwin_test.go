//go:build darwin

package session_test

import (
	"errors"
	"testing"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestHooksRejectUnsupportedSupervisionBeforeLaunch(t *testing.T) {
	cfg := config.HookConfig{SessionSwitchPre: []config.HookCommand{{Command: "/nonexistent/hook"}}}
	runner := session.NewHookRunner(session.HookRunnerDeps{})
	result, err := runner.Run(t.Context(), cfg, session.HookEvent{Name: session.EventSessionSwitchPre})
	if !errors.Is(err, session.ErrHookSupervisionUnsupported) || result.Attempted != 0 || len(result.Outcomes) != 0 {
		t.Fatalf("unsupported supervision attempted a launch: %+v, %v", result, err)
	}
}

func TestEmptyHooksDoNotRequirePlatformSupervision(t *testing.T) {
	runner := session.NewHookRunner(session.HookRunnerDeps{})
	result, err := runner.Run(t.Context(), config.HookConfig{}, session.HookEvent{Name: session.EventSessionSwitchPre})
	if err != nil || result.Attempted != 0 {
		t.Fatalf("empty hooks failed: %+v, %v", result, err)
	}
}
