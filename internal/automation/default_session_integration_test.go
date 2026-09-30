//go:build (linux || darwin) && integration

package automation_test

import (
	"slices"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/automation"
)

func newNumberedSession(t *testing.T, server *gotmux.Server, name string) gotmux.Session {
	t.Helper()

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    name,
		Dir:     "",
		Window:  "main",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 100, Height: 30},
		Start:   gotmux.StartPolicyExistingOnly,
		Group:   "",
	})
	if err != nil {
		t.Fatalf("NewSession(%s) failed: %v", name, err)
	}

	return session
}

func newBootstrapSession(t *testing.T, server *gotmux.Server, name string) (gotmux.Session, automation.BootstrapReceipt) {
	t.Helper()

	sess, receipt, err := automation.CreateBootstrapSession(t.Context(), server, gotmux.NewSessionOptions{
		Name:    name,
		Dir:     "",
		Window:  "main",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 100, Height: 30},
		Start:   gotmux.StartPolicyExistingOnly,
		Group:   "",
	})
	if err != nil {
		t.Fatalf("CreateBootstrapSession(%s) failed: %v", name, err)
	}

	return sess, receipt
}

func sessionNames(t *testing.T, server *gotmux.Server) []string {
	t.Helper()

	sessions, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatalf("Sessions failed: %v", err)
	}

	names := make([]string, 0, len(sessions))
	for _, session := range sessions {
		names = append(names, session.Name)
	}

	return names
}

func containsName(names []string, want string) bool {
	return slices.Contains(names, want)
}

func TestEnsureDefaultSessionRenamesVerifiedBootstrapSession(t *testing.T) {
	server := tmuxtest.NewServer(t)
	scratch, receipt := newBootstrapSession(t, server, "7")

	result, err := automation.EnsureDefaultSession(t.Context(), server, automation.DefaultSessionRequest{
		Current: scratch.ID(),
		Client:  gotmux.Client{},
		Receipt: receipt,
		Now:     time.Now(),
	})
	if err != nil {
		t.Fatalf("EnsureDefaultSession failed: %v", err)
	}

	if !result.Changed || result.Action != automation.DefaultSessionRenamed {
		t.Fatalf("result = %+v, want a rename", result)
	}

	if result.FromSession != "7" || result.ToSession != automation.DefaultSessionName {
		t.Fatalf("result = %+v, want 7 renamed to 0", result)
	}

	names := sessionNames(t, server)
	if !containsName(names, automation.DefaultSessionName) || containsName(names, "7") {
		t.Fatalf("sessions = %v, want the scratch session renamed to 0", names)
	}
}

func TestEnsureDefaultSessionKillsVerifiedBootstrapWhenDefaultExists(t *testing.T) {
	server := tmuxtest.NewServer(t)
	defaultSession := newNumberedSession(t, server, automation.DefaultSessionName)
	scratch, receipt := newBootstrapSession(t, server, "12")

	result, err := automation.EnsureDefaultSession(t.Context(), server, automation.DefaultSessionRequest{
		Current: scratch.ID(),
		Client:  gotmux.Client{},
		Receipt: receipt,
		Now:     time.Now(),
	})
	if err != nil {
		t.Fatalf("EnsureDefaultSession failed: %v", err)
	}

	if !result.Changed || result.Action != automation.DefaultSessionSwitched {
		t.Fatalf("result = %+v, want a switch", result)
	}

	if _, err := server.Session(t.Context(), scratch.ID()); err == nil {
		t.Fatal("verified scratch session must be removed")
	}

	info, err := defaultSession.Info(t.Context())
	if err != nil {
		t.Fatalf("default session missing: %v", err)
	}

	if info.Name != automation.DefaultSessionName {
		t.Fatalf("default session name = %q, want 0", info.Name)
	}
}

func TestEnsureDefaultSessionProtectsUnrelatedNumberedUserSessions(t *testing.T) {
	server := tmuxtest.NewServer(t)

	userSession := newNumberedSession(t, server, "1")

	defaultSession := newNumberedSession(t, server, automation.DefaultSessionName)

	result, err := automation.EnsureDefaultSession(t.Context(), server, automation.DefaultSessionRequest{
		Current: userSession.ID(),
		Client:  gotmux.Client{},
		Receipt: automation.BootstrapReceipt{},
		Now:     time.Now(),
	})
	if err != nil {
		t.Fatalf("EnsureDefaultSession failed: %v", err)
	}

	if result.Changed {
		t.Fatalf("result = %+v, user session must NOT be changed", result)
	}

	if result.Action != automation.DefaultSessionUnsupported {
		t.Fatalf("action = %q, want unsupported for unverified user work", result.Action)
	}

	info, err := userSession.Info(t.Context())
	if err != nil {
		t.Fatalf("user session was killed or missing: %v", err)
	}

	if info.Name != "1" {
		t.Fatalf("user session was renamed to %q, want 1", info.Name)
	}

	if _, err := defaultSession.Info(t.Context()); err != nil {
		t.Fatalf("default session missing: %v", err)
	}

	names := sessionNames(t, server)
	if !containsName(names, "1") || !containsName(names, automation.DefaultSessionName) {
		t.Fatalf("sessions = %v, want both '1' and '0' intact", names)
	}
}

func TestEnsureDefaultSessionProtectsNumberedUserSessionWithoutDefault(t *testing.T) {
	server := tmuxtest.NewServer(t)

	userSession := newNumberedSession(t, server, "42")

	result, err := automation.EnsureDefaultSession(t.Context(), server, automation.DefaultSessionRequest{
		Current: userSession.ID(),
		Client:  gotmux.Client{},
		Receipt: automation.BootstrapReceipt{},
		Now:     time.Now(),
	})
	if err != nil {
		t.Fatalf("EnsureDefaultSession failed: %v", err)
	}

	if result.Changed {
		t.Fatalf("result = %+v, user session must not be renamed", result)
	}

	if result.Action != automation.DefaultSessionUnsupported {
		t.Fatalf("action = %q, want unsupported for unverified user work", result.Action)
	}

	info, err := userSession.Info(t.Context())
	if err != nil {
		t.Fatalf("user session missing: %v", err)
	}

	if info.Name != "42" {
		t.Fatalf("user session renamed to %q, want 42", info.Name)
	}

	names := sessionNames(t, server)
	if containsName(names, automation.DefaultSessionName) {
		t.Fatalf("default session 0 must not be created from unverified session: %v", names)
	}
}

func TestEnsureDefaultSessionProtectsUnrelatedSessions(t *testing.T) {
	server := tmuxtest.NewServer(t)

	named := newNumberedSession(t, server, "work")
	multi := newNumberedSession(t, server, "3")

	if _, err := multi.NewWindow(t.Context(), gotmux.NewWindowOptions{
		Name:    "second",
		Dir:     "",
		Program: gotmux.Exec("sleep", "300"),
		Select:  false,
	}); err != nil {
		t.Fatalf("NewWindow failed: %v", err)
	}

	cases := []struct {
		name    string
		current gotmux.Session
		now     time.Time
	}{
		{name: "named session", current: named, now: time.Now()},
		{name: "multi-window session", current: multi, now: time.Now()},
		{name: "stale session", current: multi, now: time.Now().Add(2 * time.Minute)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := automation.EnsureDefaultSession(t.Context(), server, automation.DefaultSessionRequest{
				Current: testCase.current.ID(),
				Now:     testCase.now,
			})
			if err != nil {
				t.Fatalf("EnsureDefaultSession failed: %v", err)
			}

			if result.Changed {
				t.Fatalf("result = %+v, want no change for %s", result, testCase.name)
			}
		})
	}

	names := sessionNames(t, server)
	if !containsName(names, "work") || !containsName(names, "3") {
		t.Fatalf("sessions = %v, want protected sessions intact", names)
	}
}

func TestEnsureDefaultSessionWithoutScratchIdentityDoesNothing(t *testing.T) {
	server := tmuxtest.NewServer(t)
	newNumberedSession(t, server, "5")

	result, err := automation.EnsureDefaultSession(t.Context(), server, automation.DefaultSessionRequest{})
	if err != nil {
		t.Fatalf("EnsureDefaultSession failed: %v", err)
	}

	if result.Changed {
		t.Fatalf("result = %+v, want no change without an observed session", result)
	}
}
