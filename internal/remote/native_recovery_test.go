package remote

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/process"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/restore"
)

func TestNativeRestoreAcceptsVerifiedBestEffortOnly(t *testing.T) {
	for _, status := range []string{"best_effort", "partial", "uncertain", "failed"} {
		for _, verified := range []bool{false, true} {
			result := engine.RestoreResult{}
			result.Status, result.Verified = status, verified
			result.MissingRequirements = []restore.MissingRequirement{{Domain: "terminal", Status: "observed", Reason: "terminal_state_checkpoint_unavailable"}}

			wantIncomplete := status != "best_effort" || !verified
			if got := restoreIncomplete(result); got != wantIncomplete {
				t.Fatalf("status=%s verified=%t incomplete=%t", status, verified, got)
			}

			result.Uncertain = true
			if !restoreIncomplete(result) {
				t.Fatal("unknown effect accepted as a completed remote action")
			}
		}
	}
}

func TestNewNativeSavedSessionsProcessEnvironmentDefaultsAndOverrides(t *testing.T) {
	server, err := integration.Open("/tmp/tmux.sock")
	if err != nil {
		t.Fatalf("integration.Open failed: %v", err)
	}

	t.Run("defaults when nil", func(t *testing.T) {
		svc, err := NewNativeSavedSessions(NativeSavedConfig{
			Repository: "/tmp/repo",
			LeaseRoot:  "/tmp/lease",
			Server:     server,
		})
		if err != nil {
			t.Fatalf("NewNativeSavedSessions failed: %v", err)
		}

		expected := config.FilterProcessEnvironmentKeys(config.DefaultProcessEnvironment)
		if !slices.Equal(svc.config.ProcessEnvironment, expected) {
			t.Fatalf("ProcessEnvironment = %#v, want %#v", svc.config.ProcessEnvironment, expected)
		}

		if svc.config.AgentSource == nil {
			t.Fatal("AgentSource must not be nil")
		}
	})

	t.Run("preserves explicit empty list", func(t *testing.T) {
		svc, err := NewNativeSavedSessions(NativeSavedConfig{
			Repository:         "/tmp/repo",
			LeaseRoot:          "/tmp/lease",
			Server:             server,
			ProcessEnvironment: []string{},
		})
		if err != nil {
			t.Fatalf("NewNativeSavedSessions failed: %v", err)
		}

		if len(svc.config.ProcessEnvironment) != 0 {
			t.Fatalf("ProcessEnvironment = %#v, want empty", svc.config.ProcessEnvironment)
		}
	})

	t.Run("filters TMUX and TMUX_PANE from custom keys", func(t *testing.T) {
		svc, err := NewNativeSavedSessions(NativeSavedConfig{
			Repository:         "/tmp/repo",
			LeaseRoot:          "/tmp/lease",
			Server:             server,
			ProcessEnvironment: []string{"TMUX", "CUSTOM_VAR", "TMUX_PANE"},
		})
		if err != nil {
			t.Fatalf("NewNativeSavedSessions failed: %v", err)
		}

		expected := []string{"CUSTOM_VAR"}
		if !slices.Equal(svc.config.ProcessEnvironment, expected) {
			t.Fatalf("ProcessEnvironment = %#v, want %#v", svc.config.ProcessEnvironment, expected)
		}
	})

	t.Run("preserves explicit agent source", func(t *testing.T) {
		customStore := agents.NewAHTStore("/custom/aht/path.json")

		svc, err := NewNativeSavedSessions(NativeSavedConfig{
			Repository:  "/tmp/repo",
			LeaseRoot:   "/tmp/lease",
			Server:      server,
			AgentSource: customStore,
		})
		if err != nil {
			t.Fatalf("NewNativeSavedSessions failed: %v", err)
		}

		if svc.config.AgentSource != customStore {
			t.Fatalf("AgentSource = %#v, want customStore", svc.config.AgentSource)
		}
	})
}

func TestNewNativeSavedSessionsProcessEnvironmentRejections(t *testing.T) {
	server, err := integration.Open("/tmp/tmux.sock")
	if err != nil {
		t.Fatalf("integration.Open failed: %v", err)
	}

	t.Run("rejects invalid variable name", func(t *testing.T) {
		_, err := NewNativeSavedSessions(NativeSavedConfig{
			Repository:         "/tmp/repo",
			LeaseRoot:          "/tmp/lease",
			Server:             server,
			ProcessEnvironment: []string{"INVALID=KEY"},
		})
		if err == nil {
			t.Fatal("expected error for invalid key")
		}

		if !errors.Is(err, process.ErrInvalidVariableName) {
			t.Fatalf("expected ErrInvalidVariableName, got: %v", err)
		}
	})

	t.Run("rejects excessive keys", func(t *testing.T) {
		keys := make([]string, 257)
		for i := range keys {
			keys[i] = fmt.Sprintf("K%d", i)
		}

		_, err := NewNativeSavedSessions(NativeSavedConfig{
			Repository:         "/tmp/repo",
			LeaseRoot:          "/tmp/lease",
			Server:             server,
			ProcessEnvironment: keys,
		})
		if err == nil {
			t.Fatal("expected error for excessive keys")
		}

		if !errors.Is(err, process.ErrEnvironmentLimitExceeded) {
			t.Fatalf("expected ErrEnvironmentLimitExceeded, got: %v", err)
		}
	})
}
