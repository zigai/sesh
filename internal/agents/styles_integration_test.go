//go:build (linux || darwin) && integration

package agents_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zigai/aht/v2/pkg/aht"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
)

const (
	testStatusStyleOption = "window-status-style"
	testBellStyleOption   = "window-status-bell-style"
	testOwnerOption       = "@sesh-agent-style"
	testPriorOption       = "@sesh-agent-style-prior"
)

func windowFor(t *testing.T, ctx context.Context, server *gotmux.Server, windowID string) gotmux.Window {
	t.Helper()

	window, err := server.Window(ctx, gotmux.WindowID(windowID))
	if err != nil {
		t.Fatalf("window handle: %v", err)
	}

	return window
}

func localOption(t *testing.T, ctx context.Context, window gotmux.Window, name string) (string, bool) {
	t.Helper()

	value, err := window.Options().Get(ctx, name)
	if err != nil {
		t.Fatalf("reading option %s: %v", name, err)
	}

	return value.Local.Get()
}

func userOption(t *testing.T, ctx context.Context, window gotmux.Window, name string) (string, bool) {
	t.Helper()

	value, err := window.Options().User(ctx, name)
	if err != nil {
		t.Fatalf("reading user option %s: %v", name, err)
	}

	return value.Effective.Get()
}

func TestWindowStyleRestoresPreviousValue(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	window := windowFor(t, ctx, server, current.WindowID)

	if err := window.Options().Set(ctx, testStatusStyleOption, "fg=red"); err != nil {
		t.Fatalf("seeding user style: %v", err)
	}

	styles := agents.NewTmuxStyleController(server, true, true)

	if err := styles.SetWindowStyle(ctx, current.WindowID, "fg=blue,bold"); err != nil {
		t.Fatalf("SetWindowStyle failed: %v", err)
	}

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != "fg=blue,bold" {
		t.Fatalf("applied style = %q (present %t), want fg=blue,bold", style, ok)
	}

	if _, ok := userOption(t, ctx, window, testOwnerOption); !ok {
		t.Fatal("style ownership marker missing while styled")
	}

	if err := styles.ClearWindowStyle(ctx, current.WindowID); err != nil {
		t.Fatalf("ClearWindowStyle failed: %v", err)
	}

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != "fg=red" {
		t.Fatalf("restored style = %q (present %t), want fg=red", style, ok)
	}

	if style, ok := localOption(t, ctx, window, testBellStyleOption); ok {
		t.Fatalf("unset bell style was restored as %q, want unset", style)
	}

	if _, ok := userOption(t, ctx, window, testOwnerOption); ok {
		t.Fatal("style ownership marker must be removed after clearing")
	}

	if _, ok := userOption(t, ctx, window, testPriorOption); ok {
		t.Fatal("prior style record must be removed after clearing")
	}
}

func TestWindowStyleReleaseSkipsUserChanges(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	window := windowFor(t, ctx, server, current.WindowID)

	styles := agents.NewTmuxStyleController(server, true, true)

	if err := styles.SetWindowStyle(ctx, current.WindowID, "fg=blue,bold"); err != nil {
		t.Fatalf("SetWindowStyle failed: %v", err)
	}

	if err := window.Options().Set(ctx, testStatusStyleOption, "fg=green"); err != nil {
		t.Fatalf("user style change: %v", err)
	}

	if err := styles.ClearWindowStyle(ctx, current.WindowID); err != nil {
		t.Fatalf("ClearWindowStyle failed: %v", err)
	}

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != "fg=green" {
		t.Fatalf("style = %q (present %t), want the user's fg=green", style, ok)
	}

	if _, ok := userOption(t, ctx, window, testOwnerOption); ok {
		t.Fatal("ownership marker must be removed even when the release is skipped")
	}
}

func TestWindowStyleFreshProcessPreservesManualEditDuringUpdate(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	window := windowFor(t, ctx, server, current.WindowID)
	first := agents.NewTmuxStyleController(server, true, true)

	if err := first.SetWindowStyle(ctx, current.WindowID, "fg=blue,bold"); err != nil {
		t.Fatalf("first SetWindowStyle failed: %v", err)
	}

	if err := window.Options().Set(ctx, testStatusStyleOption, "fg=magenta,bold"); err != nil {
		t.Fatalf("manual style update: %v", err)
	}

	second := agents.NewTmuxStyleController(server, true, true)
	if err := second.SetWindowStyle(ctx, current.WindowID, "fg=red,bold"); err != nil {
		t.Fatalf("fresh-process SetWindowStyle failed: %v", err)
	}

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != "fg=magenta,bold" {
		t.Fatalf("manual style overwritten by fresh process: %q (present %t)", style, ok)
	}
}

func TestWindowStyleClearOwnedFromFreshProcess(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	window := windowFor(t, ctx, server, current.WindowID)

	if err := window.Options().Set(ctx, testStatusStyleOption, "fg=red"); err != nil {
		t.Fatalf("seeding user style: %v", err)
	}

	styling := agents.NewTmuxStyleController(server, true, true)

	if err := styling.SetWindowStyle(ctx, current.WindowID, "fg=blue,bold"); err != nil {
		t.Fatalf("SetWindowStyle failed: %v", err)
	}

	cleanup := agents.NewTmuxStyleController(server, true, true)

	if err := cleanup.ClearOwned(ctx); err != nil {
		t.Fatalf("ClearOwned failed: %v", err)
	}

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != "fg=red" {
		t.Fatalf("restored style = %q (present %t), want fg=red", style, ok)
	}
}

func TestWindowStyleClearOwnedLeavesForeignStylesAlone(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	window := windowFor(t, ctx, server, current.WindowID)

	if err := window.Options().Set(ctx, testStatusStyleOption, "fg=red"); err != nil {
		t.Fatalf("seeding user style: %v", err)
	}

	cleanup := agents.NewTmuxStyleController(server, true, true)

	if err := cleanup.ClearOwned(ctx); err != nil {
		t.Fatalf("ClearOwned failed: %v", err)
	}

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != "fg=red" {
		t.Fatalf("foreign style = %q (present %t), want untouched fg=red", style, ok)
	}
}

func TestWindowStyleCorruptPriorFailsClosedAndRetainsOwnership(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	window := windowFor(t, ctx, server, current.WindowID)
	styles := agents.NewTmuxStyleController(server, true, true)

	if err := styles.SetWindowStyle(ctx, current.WindowID, "fg=blue,bold"); err != nil {
		t.Fatalf("SetWindowStyle failed: %v", err)
	}

	if err := window.Options().SetUser(ctx, testPriorOption, "{invalid"); err != nil {
		t.Fatalf("corrupting prior marker: %v", err)
	}

	cleanup := agents.NewTmuxStyleController(server, true, true)
	if err := cleanup.ClearOwned(ctx); !errors.Is(err, agents.ErrStyleOwnershipCorrupt) {
		t.Fatalf("ClearOwned error = %v, want ErrStyleOwnershipCorrupt", err)
	}

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != "fg=blue,bold" {
		t.Fatalf("style changed without a readable prior: %q (present %t)", style, ok)
	}

	if _, ok := userOption(t, ctx, window, testOwnerOption); !ok {
		t.Fatal("ownership marker was discarded after failed recovery")
	}
}

func TestTrackerStylesLiveAgentWindow(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	window := windowFor(t, ctx, server, current.WindowID)

	styles := agents.NewTmuxStyleController(server, true, true)
	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())

	session := trackerSessionForPane(current, string(aht.ActivityWaiting))

	if err := tracker.HandleSnapshot(ctx, []aht.Session{session}); err != nil {
		t.Fatalf("HandleSnapshot failed: %v", err)
	}

	expected := agents.DefaultPalette[string(agents.StateWaiting)]

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != expected {
		t.Fatalf("window style = %q (present %t), want %q", style, ok, expected)
	}

	if err := tracker.ClearAll(ctx); err != nil {
		t.Fatalf("ClearAll failed: %v", err)
	}

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); ok {
		t.Fatalf("window style = %q, want unset after ClearAll", style)
	}
}

func TestTrackerRestoresUserStyleAfterSessionEnds(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	window := windowFor(t, ctx, server, current.WindowID)

	if err := window.Options().Set(ctx, testStatusStyleOption, "fg=red"); err != nil {
		t.Fatalf("seeding user style: %v", err)
	}

	styles := agents.NewTmuxStyleController(server, true, true)
	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSessionForPane(current, string(aht.ActivityWaiting))}); err != nil {
		t.Fatalf("HandleSnapshot failed: %v", err)
	}

	gone := trackerSessionForPane(current, string(aht.ActivityWaiting))
	gone.Liveness = aht.Gone{}

	if err := tracker.HandleSnapshot(ctx, []aht.Session{gone}); err != nil {
		t.Fatalf("HandleSnapshot failed: %v", err)
	}

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != "fg=red" {
		t.Fatalf("window style = %q (present %t), want the user's fg=red", style, ok)
	}
}

func TestTrackerPreservesUserManualOptionEditsDuringTracking(t *testing.T) {
	server, _, current := newTmuxFixture(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	window := windowFor(t, ctx, server, current.WindowID)

	styles := agents.NewTmuxStyleController(server, true, true)
	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSessionForPane(current, string(aht.ActivityWaiting))}); err != nil {
		t.Fatalf("HandleSnapshot failed: %v", err)
	}

	expected := agents.DefaultPalette[string(agents.StateWaiting)]
	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != expected {
		t.Fatalf("window style = %q, want %q", style, expected)
	}

	if err := window.Options().Set(ctx, testStatusStyleOption, "fg=magenta,bold"); err != nil {
		t.Fatalf("user set style: %v", err)
	}

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSessionForPane(current, string(aht.ActivityFailed))}); err != nil {
		t.Fatalf("second HandleSnapshot failed: %v", err)
	}

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != "fg=magenta,bold" {
		t.Fatalf("user manual edit was overwritten: style = %q (ok %t), want fg=magenta,bold", style, ok)
	}

	if err := tracker.ClearAll(ctx); err != nil {
		t.Fatalf("ClearAll failed: %v", err)
	}

	if style, ok := localOption(t, ctx, window, testStatusStyleOption); !ok || style != "fg=magenta,bold" {
		t.Fatalf("user manual edit was overwritten by cleanup: style = %q, want fg=magenta,bold", style)
	}
}

func trackerSessionForPane(pane agents.PaneLocation, activity string) aht.Session {
	return aht.Session{
		ID:       "agent-" + pane.PaneID,
		Harness:  aht.HarnessOmp,
		Liveness: aht.Live{Activity: aht.Activity(activity)},
		Location: aht.Location{
			Kind:        aht.MultiplexerTmux,
			ServerID:    pane.ServerSocket,
			SessionID:   pane.SessionID,
			SessionName: pane.SessionName,
			WindowID:    pane.WindowID,
			WindowIndex: pane.WindowIndex,
			PaneID:      pane.PaneID,
			PaneIndex:   pane.PaneIndex,
			PanePID:     pane.PanePID,
		},
	}
}
