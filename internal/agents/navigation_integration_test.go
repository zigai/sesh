//go:build (linux || darwin) && integration

package agents_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zigai/aht/v2/pkg/aht"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
)

func newTestNavigator(t *testing.T, server *gotmux.Server, statePath string, target agents.CurrentTarget) *agents.Navigator {
	t.Helper()

	navigator, err := agents.NewNavigator(agents.NavigatorOptions{
		Server:    server,
		StatePath: statePath,
		Target:    target,
	})
	if err != nil {
		t.Fatalf("NewNavigator failed: %v", err)
	}

	return navigator
}

func newAgentWindow(t *testing.T, ctx context.Context, server *gotmux.Server, session gotmux.Session, name string) agents.PaneLocation {
	t.Helper()

	window, err := session.NewWindow(ctx, gotmux.NewWindowOptions{
		Name:    name,
		Dir:     "",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		Select:  false,
	})
	if err != nil {
		t.Fatalf("creating window %s: %v", name, err)
	}

	pane, err := window.Window().ActivePane(ctx)
	if err != nil {
		t.Fatalf("active pane of %s: %v", name, err)
	}

	return livePaneFor(t, ctx, server, string(pane.ID()))
}

func TestAgentNavigationActivatesTargetWindow(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	session, err := server.Session(ctx, gotmux.SessionID(current.SessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	target := newAgentWindow(t, ctx, server, session, "target")

	if target.WindowID == current.WindowID {
		t.Fatal("fixture must create a distinct target window")
	}

	statePath := writeRegistry(t, paneFixture(target, string(aht.ActivityWaiting)))
	navigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: current.PaneID})

	selected, err := navigator.NextIdle(ctx, agents.NavigationGlobal)
	if err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	if selected.PaneID != target.PaneID {
		t.Fatalf("selected pane = %q, want %q", selected.PaneID, target.PaneID)
	}

	if !windowActive(t, ctx, server, target.SessionID, target.WindowID) {
		t.Fatalf("navigation returned pane %s but window %s stayed inactive", selected.PaneID, target.WindowID)
	}

	if !paneActive(t, ctx, server, target.PaneID) {
		t.Fatalf("navigation returned pane %s but tmux reports another active pane", target.PaneID)
	}

	if windowActive(t, ctx, server, current.SessionID, current.WindowID) {
		t.Fatalf("previous window %s stayed active after navigation", current.WindowID)
	}
}

func TestAgentNavigationCrossesWindowsWithinSession(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	session, err := server.Session(ctx, gotmux.SessionID(current.SessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	other := newAgentWindow(t, ctx, server, session, "other")
	target := newAgentWindow(t, ctx, server, session, "target")

	statePath := writeRegistry(t, paneFixture(target, string(aht.ActivityFailed)))
	navigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: current.PaneID})

	selected, err := navigator.NextIdle(ctx, agents.NavigationCurrentSession)
	if err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	if selected.PaneID != target.PaneID {
		t.Fatalf("selected pane = %q, want %q", selected.PaneID, target.PaneID)
	}

	if !windowActive(t, ctx, server, current.SessionID, target.WindowID) {
		t.Fatalf("target window %s is not active", target.WindowID)
	}

	if windowActive(t, ctx, server, current.SessionID, other.WindowID) {
		t.Fatalf("unrelated window %s became active", other.WindowID)
	}
}

func TestAgentNavigationSwitchesSessionForInvokingClient(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		name := "sole client of the invoking session"
		if explicit {
			name = "explicit client"
		}

		t.Run(name, func(t *testing.T) {
			assertClientFollowsAgent(t, explicit)
		})
	}
}

func assertClientFollowsAgent(t *testing.T, explicitClient bool) {
	t.Helper()

	server, sessionID, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	session, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	clientName := attachTestClient(t, ctx, server, session)
	target := newAgentSession(t, ctx, server, "agents")

	statePath := writeRegistry(t, paneFixture(target, string(aht.ActivityWaiting)))

	targetSelector := agents.CurrentTarget{PaneID: current.PaneID}
	if explicitClient {
		targetSelector.Client = clientName
	}

	navigator := newTestNavigator(t, server, statePath, targetSelector)

	selected, err := navigator.NextIdle(ctx, agents.NavigationGlobal)
	if err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	if selected.SessionID != target.SessionID {
		t.Fatalf("selected session = %q, want %q", selected.SessionID, target.SessionID)
	}

	if got := clientSession(t, ctx, server, clientName); got != target.SessionID {
		t.Fatalf("client session = %q, want %q", got, target.SessionID)
	}

	if !windowActive(t, ctx, server, target.SessionID, target.WindowID) {
		t.Fatalf("target window %s is not active in session %s", target.WindowID, target.SessionID)
	}

	if !paneActive(t, ctx, server, target.PaneID) {
		t.Fatalf("target pane %s is not active", target.PaneID)
	}
}

func newAgentSession(t *testing.T, ctx context.Context, server *gotmux.Server, name string) agents.PaneLocation {
	t.Helper()

	session, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    name,
		Dir:     "",
		Window:  "",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 120, Height: 40},
		Start:   gotmux.ExistingOnly,
		Group:   "",
	})
	if err != nil {
		t.Fatalf("creating session %s: %v", name, err)
	}

	return livePaneFor(t, ctx, server, string(mustActivePane(t, ctx, session)))
}

func TestAgentNavigationFocusesNamedAgentPane(t *testing.T) {
	server, sessionID, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	session, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	clientName := attachTestClient(t, ctx, server, session)

	targetSession, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "focus-target",
		Dir:     "",
		Window:  "",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 120, Height: 40},
		Start:   gotmux.ExistingOnly,
		Group:   "",
	})
	if err != nil {
		t.Fatalf("creating focus target session: %v", err)
	}

	target := livePaneFor(t, ctx, server, string(mustActivePane(t, ctx, targetSession)))

	statePath := writeRegistry(t, paneFixture(target, string(aht.ActivityIdle)))
	navigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: current.PaneID})

	focused, err := navigator.Focus(ctx, agents.FocusTarget{PaneID: target.PaneID, SessionID: target.SessionID})
	if err != nil {
		t.Fatalf("Focus failed: %v", err)
	}

	if focused.SessionID != target.SessionID || focused.WindowID != target.WindowID {
		t.Fatalf("focused location = %+v, want session %s window %s", focused, target.SessionID, target.WindowID)
	}

	if got := clientSession(t, ctx, server, clientName); got != target.SessionID {
		t.Fatalf("client session = %q, want %q", got, target.SessionID)
	}

	if !windowActive(t, ctx, server, target.SessionID, target.WindowID) {
		t.Fatalf("focused window %s is not active", target.WindowID)
	}

	if !paneActive(t, ctx, server, target.PaneID) {
		t.Fatalf("focused pane %s is not active", target.PaneID)
	}
}

func TestAgentNavigationLeavesUnrelatedClientsUntouched(t *testing.T) {
	server, sessionID, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	invokingSession, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	otherSession, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "unrelated",
		Dir:     "",
		Window:  "",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 100, Height: 30},
		Start:   gotmux.ExistingOnly,
		Group:   "",
	})
	if err != nil {
		t.Fatalf("creating unrelated session: %v", err)
	}

	invokingClient := attachTestClient(t, ctx, server, invokingSession)
	otherClient := attachTestClient(t, ctx, server, otherSession)

	targetSession, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "agents",
		Dir:     "",
		Window:  "",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 120, Height: 40},
		Start:   gotmux.ExistingOnly,
		Group:   "",
	})
	if err != nil {
		t.Fatalf("creating target session: %v", err)
	}

	target := livePaneFor(t, ctx, server, string(mustActivePane(t, ctx, targetSession)))

	statePath := writeRegistry(t, paneFixture(target, string(aht.ActivityIdle)))
	navigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: current.PaneID, Client: invokingClient})

	if _, err := navigator.NextIdle(ctx, agents.NavigationGlobal); err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	if got := clientSession(t, ctx, server, invokingClient); got != target.SessionID {
		t.Fatalf("invoking client session = %q, want %q", got, target.SessionID)
	}

	if got := clientSession(t, ctx, server, otherClient); got != string(otherSession.ID()) {
		t.Fatalf("unrelated client session = %q, want %q", got, otherSession.ID())
	}
}

func TestAgentNavigationSkipsStaleRegistryPanes(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	session, err := server.Session(ctx, gotmux.SessionID(current.SessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	target := newAgentWindow(t, ctx, server, session, "target")

	stale := paneFixture(target, string(aht.ActivityWaiting))
	stale.PanePID = target.PanePID + 1

	statePath := writeRegistry(t, stale)
	navigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: current.PaneID})

	_, err = navigator.NextIdle(ctx, agents.NavigationGlobal)
	if !errors.Is(err, agents.ErrNoTargetPane) {
		t.Fatalf("error = %v, want ErrNoTargetPane for a reused pane", err)
	}

	if windowActive(t, ctx, server, current.SessionID, target.WindowID) {
		t.Fatalf("stale agent window %s must not be activated", target.WindowID)
	}
}

func TestAgentNavigationBackSkipsVanishedHistory(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	session, err := server.Session(ctx, gotmux.SessionID(current.SessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	previous := newAgentWindow(t, ctx, server, session, "previous")

	statePath := writeRegistry(t,
		paneFixture(current, string(aht.ActivityIdle)),
		paneFixture(previous, string(aht.ActivityWaiting)),
	)
	navigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: current.PaneID})

	moved, err := navigator.NextIdle(ctx, agents.NavigationGlobal)
	if err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	if moved.PaneID != previous.PaneID {
		t.Fatalf("moved pane = %q, want %q", moved.PaneID, previous.PaneID)
	}

	if err := setNavigationHistory(ctx, server, []string{"%9999", current.PaneID}); err != nil {
		t.Fatalf("setting history: %v", err)
	}

	backNavigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: previous.PaneID})

	target, err := backNavigator.Back(ctx)
	if err != nil {
		t.Fatalf("Back failed: %v", err)
	}

	if target.PaneID != current.PaneID {
		t.Fatalf("back target = %q, want %q", target.PaneID, current.PaneID)
	}

	if !windowActive(t, ctx, server, current.SessionID, current.WindowID) {
		t.Fatalf("window %s is not active after back", current.WindowID)
	}

	if !paneActive(t, ctx, server, current.PaneID) {
		t.Fatalf("pane %s is not active after back", current.PaneID)
	}

	if windowActive(t, ctx, server, current.SessionID, previous.WindowID) {
		t.Fatalf("unrelated window %s became active", previous.WindowID)
	}
}

func TestAgentNavigationFocusesSharedWindowInRequestedSession(t *testing.T) {
	server, sessionID, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	primary, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	primaryInfo, err := primary.Info(ctx)
	if err != nil {
		t.Fatalf("session info: %v", err)
	}

	target := newAgentWindow(t, ctx, server, primary, "shared")

	grouped, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "grouped",
		Dir:     "",
		Window:  "",
		Program: gotmux.Program{},
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 100, Height: 30},
		Start:   gotmux.ExistingOnly,
		Group:   primaryInfo.Name,
	})
	if err != nil {
		t.Fatalf("creating grouped session: %v", err)
	}

	groupedSessionID := string(grouped.ID())

	if !windowActive(t, ctx, server, groupedSessionID, target.WindowID) {
		if err := selectWindowByID(ctx, server, groupedSessionID, target.WindowID); err != nil {
			t.Fatalf("selecting shared window in grouped session: %v", err)
		}
	}

	statePath := writeRegistry(t, paneFixture(target, string(aht.ActivityWaiting)))
	navigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: current.PaneID, SessionID: groupedSessionID})

	selected, err := navigator.NextIdle(ctx, agents.NavigationCurrentSession)
	if err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	if selected.SessionID != groupedSessionID {
		t.Fatalf("selected session = %q, want the grouped session %q", selected.SessionID, groupedSessionID)
	}

	if !windowActive(t, ctx, server, groupedSessionID, target.WindowID) {
		t.Fatalf("shared window %s is not active in the grouped session", target.WindowID)
	}

	if !windowActive(t, ctx, server, sessionID, current.WindowID) {
		t.Fatalf("primary session moved off window %s while focusing the grouped session", current.WindowID)
	}
}

func mustActivePane(t *testing.T, ctx context.Context, session gotmux.Session) gotmux.PaneID {
	t.Helper()

	windows, _, err := session.WindowInfos(ctx)
	if err != nil || len(windows) == 0 {
		t.Fatalf("session windows: %v", err)
	}

	pane, err := windows[0].Handle().ActivePane(ctx)
	if err != nil {
		t.Fatalf("active pane: %v", err)
	}

	return pane.ID()
}

func setNavigationHistory(ctx context.Context, server *gotmux.Server, history []string) error {
	encoded, err := json.Marshal(history)
	if err != nil {
		return fmt.Errorf("encoding navigation history: %w", err)
	}

	if err := server.GlobalSessionOptions().SetUser(ctx, "@sesh-agent-navigation-history", string(encoded)); err != nil {
		return fmt.Errorf("writing navigation history: %w", err)
	}

	return nil
}

func selectWindowByID(ctx context.Context, server *gotmux.Server, sessionID string, windowID string) error {
	session, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		return fmt.Errorf("resolving session %s: %w", sessionID, err)
	}

	links, err := session.Windows(ctx)
	if err != nil {
		return fmt.Errorf("listing session windows: %w", err)
	}

	for _, link := range links {
		if link.Window().ID() != gotmux.WindowID(windowID) {
			continue
		}

		if err := link.Select(ctx); err != nil {
			return fmt.Errorf("selecting window %s: %w", windowID, err)
		}

		return nil
	}

	return agents.ErrPaneNotLive
}

func TestAgentNavigationUsesVerifiedAmbientContext(t *testing.T) {
	server, sessionID, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	session, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	target := newAgentWindow(t, ctx, server, session, "ambient-target")

	identity, err := server.Probe(ctx)
	if err != nil {
		t.Fatalf("probing server: %v", err)
	}

	t.Setenv("TMUX", strings.Join([]string{current.ServerSocket, strconv.Itoa(identity.Identity.PID), strings.TrimPrefix(sessionID, "$")}, ","))
	t.Setenv("TMUX_PANE", current.PaneID)

	statePath := writeRegistry(t, paneFixture(target, string(aht.ActivityWaiting)))
	navigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{})

	selected, err := navigator.NextIdle(ctx, agents.NavigationGlobal)
	if err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	if selected.PaneID != target.PaneID {
		t.Fatalf("selected pane = %q, want %q", selected.PaneID, target.PaneID)
	}

	if !windowActive(t, ctx, server, target.SessionID, target.WindowID) {
		t.Fatalf("ambient navigation left window %s inactive", target.WindowID)
	}
}

func TestAgentNavigationRejectsUnverifiedAmbientContext(t *testing.T) {
	server, sessionID, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	session, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	target := newAgentWindow(t, ctx, server, session, "unverified-target")

	t.Setenv("TMUX", strings.Join([]string{current.ServerSocket, "1", strings.TrimPrefix(sessionID, "$")}, ","))
	t.Setenv("TMUX_PANE", current.PaneID)

	statePath := writeRegistry(t, paneFixture(target, string(aht.ActivityWaiting)))
	navigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{})

	_, err = navigator.NextIdle(ctx, agents.NavigationGlobal)
	if !errors.Is(err, agents.ErrCurrentContextUnknown) {
		t.Fatalf("error = %v, want ErrCurrentContextUnknown for an unverified daemon", err)
	}

	if windowActive(t, ctx, server, target.SessionID, target.WindowID) {
		t.Fatalf("unverified context must not activate window %s", target.WindowID)
	}
}

func TestAgentNavigationTwoClientsIndependentHistory(t *testing.T) {
	server, sessionID, currentA := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	sessionA, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		t.Fatalf("session A handle: %v", err)
	}

	clientA := attachTestClient(t, ctx, server, sessionA)

	sessionB, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "session-b",
		Dir:     "",
		Window:  "",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 100, Height: 30},
		Start:   gotmux.ExistingOnly,
		Group:   "",
	})
	if err != nil {
		t.Fatalf("creating session B: %v", err)
	}

	clientB := attachTestClient(t, ctx, server, sessionB)
	currentB := livePaneFor(t, ctx, server, string(mustActivePane(t, ctx, sessionB)))

	targetA := newAgentWindow(t, ctx, server, sessionA, "agent-a")
	targetB := newAgentWindow(t, ctx, server, sessionB, "agent-b")

	statePath := writeRegistry(t,
		paneFixture(targetA, string(aht.ActivityWaiting)),
		paneFixture(targetB, string(aht.ActivityWaiting)),
	)

	assertClientNavigatesNext(t, ctx, server, statePath, clientA, currentA, targetA)
	assertClientNavigatesNext(t, ctx, server, statePath, clientB, currentB, targetB)

	assertClientNavigatesBack(t, ctx, server, statePath, clientA, currentA, targetA)

	if !windowActive(t, ctx, server, targetB.SessionID, targetB.WindowID) {
		t.Fatalf("Client B window was moved unexpectedly when Client A navigated back")
	}

	assertClientNavigatesBack(t, ctx, server, statePath, clientB, currentB, targetB)
}

func assertClientNavigatesNext(
	t *testing.T,
	ctx context.Context,
	server *gotmux.Server,
	statePath string,
	client string,
	current agents.PaneLocation,
	target agents.PaneLocation,
) {
	t.Helper()

	nav := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: current.PaneID, Client: client})

	selected, err := nav.NextIdle(ctx, agents.NavigationCurrentSession)
	if err != nil {
		t.Fatalf("Client %s NextIdle failed: %v", client, err)
	}

	if selected.PaneID != target.PaneID {
		t.Fatalf("Client %s selected %q, want %q", client, selected.PaneID, target.PaneID)
	}
}

func assertClientNavigatesBack(
	t *testing.T,
	ctx context.Context,
	server *gotmux.Server,
	statePath string,
	client string,
	current agents.PaneLocation,
	target agents.PaneLocation,
) {
	t.Helper()

	nav := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: target.PaneID, Client: client})

	back, err := nav.Back(ctx)
	if err != nil {
		t.Fatalf("Client %s Back failed: %v", client, err)
	}

	if back.PaneID != current.PaneID {
		t.Fatalf("Client %s Back returned %q, want %q", client, back.PaneID, current.PaneID)
	}

	if !windowActive(t, ctx, server, current.SessionID, current.WindowID) {
		t.Fatalf("Client %s window is not active after Back", client)
	}
}

func TestAgentNavigationBackRejectsRespawnedPaneIncarnation(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	session, err := server.Session(ctx, gotmux.SessionID(current.SessionID))
	if err != nil {
		t.Fatalf("session handle: %v", err)
	}

	target := newAgentWindow(t, ctx, server, session, "agent-target")

	statePath := writeRegistry(t, paneFixture(target, string(aht.ActivityWaiting)))
	navigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: current.PaneID})

	if _, err := navigator.NextIdle(ctx, agents.NavigationGlobal); err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	origPane, err := server.Pane(ctx, gotmux.PaneID(current.PaneID))
	if err != nil {
		t.Fatalf("pane handle: %v", err)
	}

	if err := origPane.Respawn(ctx, gotmux.RespawnOptions{
		KillRunning: true,
		Program:     gotmux.Exec("sleep", "300"),
	}); err != nil {
		t.Fatalf("respawning pane: %v", err)
	}

	respawnedLoc := livePaneFor(t, ctx, server, current.PaneID)
	if respawnedLoc.PanePID == current.PanePID {
		t.Fatalf("respawned pane PID did not change: %d", respawnedLoc.PanePID)
	}

	backNavigator := newTestNavigator(t, server, statePath, agents.CurrentTarget{PaneID: target.PaneID})

	_, err = backNavigator.Back(ctx)
	if !errors.Is(err, agents.ErrNoTargetPane) {
		t.Fatalf("Back error = %v, want ErrNoTargetPane for respawned pane incarnation", err)
	}

	if !windowActive(t, ctx, server, target.SessionID, target.WindowID) {
		t.Fatalf("target window %s lost focus when respawned pane was rejected", target.WindowID)
	}

	if windowActive(t, ctx, server, current.SessionID, current.WindowID) {
		t.Fatalf("respawned window %s was incorrectly activated", current.WindowID)
	}
}
