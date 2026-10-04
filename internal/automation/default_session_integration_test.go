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

func requireUnsupportedAdoption(t *testing.T, server *gotmux.Server, request automation.DefaultSessionRequest) {
	t.Helper()

	result, err := automation.EnsureDefaultSession(t.Context(), server, request)
	if err != nil {
		t.Fatalf("EnsureDefaultSession failed: %v", err)
	}

	if result.Changed || result.Action != automation.DefaultSessionUnsupported {
		t.Fatalf("result = %+v, want an unsupported adoption without changes", result)
	}
}

func onlyPaneOf(t *testing.T, server *gotmux.Server, session gotmux.Session) gotmux.PaneInfo {
	t.Helper()

	panes, err := server.Panes(t.Context())
	if err != nil {
		t.Fatalf("Panes failed: %v", err)
	}

	for _, pane := range panes {
		if id, ok := pane.SessionID.Get(); ok && id == session.ID() {
			return pane
		}
	}

	t.Fatalf("session %s has no pane", session.ID())

	return gotmux.PaneInfo{}
}

func TestEnsureDefaultSessionProtectsUserSessionFromAnotherSessionsReceipt(t *testing.T) {
	server := tmuxtest.NewServer(t)

	userSession := newNumberedSession(t, server, "1")
	newNumberedSession(t, server, automation.DefaultSessionName)
	_, receipt := newBootstrapSession(t, server, "9")

	requireUnsupportedAdoption(t, server, automation.DefaultSessionRequest{
		Current: userSession.ID(),
		Receipt: receipt,
		Now:     time.Now(),
	})

	names := sessionNames(t, server)
	if !containsName(names, "1") || !containsName(names, automation.DefaultSessionName) || !containsName(names, "9") {
		t.Fatalf("sessions = %v, want 1, 0 and 9 intact", names)
	}
}

func TestEnsureDefaultSessionProtectsUserSessionWithoutStampedReceipt(t *testing.T) {
	server := tmuxtest.NewServer(t)

	userSession := newNumberedSession(t, server, "42")
	_, issued := newBootstrapSession(t, server, "9")

	pane := onlyPaneOf(t, server, userSession)
	receipt := issued
	receipt.SessionID = userSession.ID()
	receipt.PaneID = pane.ID
	receipt.PanePID = pane.PID
	receipt.InitialCommand = pane.CurrentCommand

	requireUnsupportedAdoption(t, server, automation.DefaultSessionRequest{
		Current: userSession.ID(),
		Receipt: receipt,
		Now:     time.Now(),
	})

	info, err := userSession.Info(t.Context())
	if err != nil {
		t.Fatalf("user session missing: %v", err)
	}

	if info.Name != "42" {
		t.Fatalf("user session renamed to %q, want 42", info.Name)
	}
}

func TestEnsureDefaultSessionProtectsBootstrapSessionThatGainedWork(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, server *gotmux.Server, session gotmux.Session)
	}{
		{
			name: "second window",
			change: func(t *testing.T, _ *gotmux.Server, session gotmux.Session) {
				t.Helper()

				if _, err := session.NewWindow(t.Context(), gotmux.NewWindowOptions{
					Name:    "second",
					Program: gotmux.Exec("sleep", "300"),
				}); err != nil {
					t.Fatalf("NewWindow failed: %v", err)
				}
			},
		},
		{
			name: "split pane",
			change: func(t *testing.T, server *gotmux.Server, session gotmux.Session) {
				t.Helper()

				pane, err := server.Pane(t.Context(), onlyPaneOf(t, server, session).ID)
				if err != nil {
					t.Fatalf("Pane failed: %v", err)
				}

				if _, err := pane.Split(t.Context(), gotmux.SplitOptions{Program: gotmux.Exec("sleep", "300")}); err != nil {
					t.Fatalf("Split failed: %v", err)
				}
			},
		},
		{
			name: "replaced initial program",
			change: func(t *testing.T, server *gotmux.Server, session gotmux.Session) {
				t.Helper()

				pane, err := server.Pane(t.Context(), onlyPaneOf(t, server, session).ID)
				if err != nil {
					t.Fatalf("Pane failed: %v", err)
				}

				if err := pane.Respawn(t.Context(), gotmux.RespawnOptions{
					KillRunning: true,
					Program:     gotmux.Exec("sleep", "300"),
				}); err != nil {
					t.Fatalf("Respawn failed: %v", err)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := tmuxtest.NewServer(t)
			scratch, receipt := newBootstrapSession(t, server, "3")

			testCase.change(t, server, scratch)

			requireUnsupportedAdoption(t, server, automation.DefaultSessionRequest{
				Current: scratch.ID(),
				Receipt: receipt,
				Now:     time.Now(),
			})

			names := sessionNames(t, server)
			if !containsName(names, "3") || containsName(names, automation.DefaultSessionName) {
				t.Fatalf("sessions = %v, want session 3 kept and no default created", names)
			}
		})
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
