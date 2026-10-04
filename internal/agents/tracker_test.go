//go:build linux || darwin

package agents_test

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/zigai/aht/v2/pkg/aht"
	"github.com/zigai/aht/v2/pkg/registry"

	"github.com/zigai/sesh/internal/agents"
)

var errFakeStyles = errors.New("PRIVATE_STYLE_ERROR")

type styleSetCall struct {
	WindowID string
	Style    string
}

type fakeStyles struct {
	active     map[string]agents.ActivePane
	applied    map[string]string
	setCalls   []styleSetCall
	clearCalls []string
	queryErr   error
	setErr     error
	clearErr   error
}

func newFakeStyles() *fakeStyles {
	return &fakeStyles{
		active:  make(map[string]agents.ActivePane),
		applied: make(map[string]string),
	}
}

func (f *fakeStyles) setActive(paneID, windowID string, paneActive, windowActive bool) {
	f.active[paneID] = agents.ActivePane{
		PaneID:       paneID,
		WindowID:     windowID,
		PaneActive:   paneActive,
		WindowActive: windowActive,
	}
}

func (f *fakeStyles) ActivePanes(context.Context) (map[string]agents.ActivePane, error) {
	if f.queryErr != nil {
		return nil, f.queryErr
	}

	active := make(map[string]agents.ActivePane, len(f.active))
	maps.Copy(active, f.active)

	return active, nil
}

func (f *fakeStyles) SetWindowStyle(_ context.Context, windowID string, style string) error {
	if f.setErr != nil {
		return f.setErr
	}

	f.setCalls = append(f.setCalls, styleSetCall{WindowID: windowID, Style: style})
	f.applied[windowID] = style

	return nil
}

func (f *fakeStyles) ClearWindowStyle(_ context.Context, windowID string) error {
	if f.clearErr != nil {
		return f.clearErr
	}

	f.clearCalls = append(f.clearCalls, windowID)
	delete(f.applied, windowID)

	return nil
}

func (f *fakeStyles) ClearOwned(_ context.Context) error {
	if f.clearErr != nil {
		return f.clearErr
	}

	for windowID := range f.applied {
		f.clearCalls = append(f.clearCalls, windowID)
		delete(f.applied, windowID)
	}

	return nil
}

func trackerSession(paneID, windowID string, activity aht.Activity, presence aht.Presence) aht.Session {
	return aht.Session{
		ID:       "session-" + paneID,
		Harness:  aht.HarnessOmp,
		Liveness: registry.NewLiveness(presence, activity, nil),
		Location: aht.Location{
			Kind:        aht.MultiplexerTmux,
			ServerID:    "/tmp/fixture/socket",
			SessionID:   "$1",
			SessionName: "dev",
			WindowID:    windowID,
			PaneID:      paneID,
		},
	}
}

func TestTrackerRunningToIdleInInactiveWindowMarksUnread(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())
	ctx := t.Context()

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityRunning, aht.PresenceLive)}); err != nil {
		t.Fatalf("running snapshot failed: %v", err)
	}

	if tracker.PaneUnread("%1") {
		t.Fatal("running agent must not be unread")
	}

	if style := tracker.WindowStyle("@1"); style != "" {
		t.Fatalf("running agent must not style its window, got %q", style)
	}

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityIdle, aht.PresenceLive)}); err != nil {
		t.Fatalf("idle snapshot failed: %v", err)
	}

	if !tracker.PaneUnread("%1") {
		t.Fatal("completion in an inactive window must mark the pane unread")
	}

	expected := agents.DefaultPalette[string(agents.StateUnread)]
	if style := tracker.WindowStyle("@1"); style != expected {
		t.Fatalf("window style = %q, want %q", style, expected)
	}

	if len(styles.setCalls) != 1 || styles.setCalls[0].WindowID != "@1" || styles.setCalls[0].Style != expected {
		t.Fatalf("style set calls = %+v", styles.setCalls)
	}
}

func TestTrackerActiveBypassSkipsUnreadInActiveWindow(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", true, true)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())
	ctx := t.Context()

	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityRunning, aht.PresenceLive)})
	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityIdle, aht.PresenceLive)})

	if tracker.PaneUnread("%1") {
		t.Fatal("completion in the active window must not be unread")
	}

	if len(styles.setCalls) != 0 {
		t.Fatalf("unexpected style set calls: %+v", styles.setCalls)
	}
}

func TestTrackerActiveBypassDisabledMarksUnread(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", true, true)

	opts := agents.DefaultTrackerOptions()
	opts.ActiveBypass = false

	tracker := agents.NewTracker(styles, opts)
	ctx := t.Context()

	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityRunning, aht.PresenceLive)})
	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityIdle, aht.PresenceLive)})

	if !tracker.PaneUnread("%1") {
		t.Fatal("ActiveBypass=false must mark completions unread")
	}
}

func TestTrackerWindowVisitClearsUnread(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())
	ctx := t.Context()

	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityRunning, aht.PresenceLive)})
	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityIdle, aht.PresenceLive)})

	if !tracker.PaneUnread("%1") {
		t.Fatal("expected unread before the visit")
	}

	styles.setActive("%1", "@1", true, true)

	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityIdle, aht.PresenceLive)})

	if tracker.PaneUnread("%1") {
		t.Fatal("visiting the window must clear unread")
	}

	if style := tracker.WindowStyle("@1"); style != "" {
		t.Fatalf("window style after visit = %q, want cleared", style)
	}

	if len(styles.clearCalls) == 0 {
		t.Fatal("expected a style clear after the visit")
	}
}

func TestTrackerIdleUnreadLifecycleAcrossSessionVisits(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())
	ctx := t.Context()

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityRunning, aht.PresenceLive)}); err != nil {
		t.Fatalf("running snapshot failed: %v", err)
	}

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityIdle, aht.PresenceLive)}); err != nil {
		t.Fatalf("idle snapshot failed: %v", err)
	}

	if !tracker.PaneUnread("%1") {
		t.Fatal("expected unread on running -> idle in inactive window")
	}

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityIdle, aht.PresenceLive)}); err != nil {
		t.Fatalf("second idle snapshot failed: %v", err)
	}

	if !tracker.PaneUnread("%1") {
		t.Fatal("unread must persist across subsequent idle snapshots before visit")
	}

	styles.setActive("%1", "@1", true, true)

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityIdle, aht.PresenceLive)}); err != nil {
		t.Fatalf("visited snapshot failed: %v", err)
	}

	if tracker.PaneUnread("%1") {
		t.Fatal("visiting the window must clear unread")
	}

	styles.setActive("%1", "@1", false, false)

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityIdle, aht.PresenceLive)}); err != nil {
		t.Fatalf("snapshot after leaving visited window failed: %v", err)
	}

	if tracker.PaneUnread("%1") {
		t.Fatal("leaving visited window must not mark unread again")
	}
}

func TestTrackerClearsStyleWhenWindowLeftAsStateReturnsNormal(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", true, true)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())
	ctx := t.Context()

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceLive)}); err != nil {
		t.Fatalf("waiting snapshot failed: %v", err)
	}

	if _, applied := styles.applied["@1"]; !applied {
		t.Fatal("waiting pane must style its window")
	}

	styles.setActive("%1", "@1", true, false)

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityInterrupted, aht.PresenceLive)}); err != nil {
		t.Fatalf("interrupted snapshot failed: %v", err)
	}

	if style, applied := styles.applied["@1"]; applied {
		t.Fatalf("tmux window style = %q after state returned to normal, want cleared", style)
	}

	if style := tracker.WindowStyle("@1"); style != "" {
		t.Fatalf("tracked window style = %q, want empty", style)
	}
}

func TestTrackerReappliesStyleWhenWindowLeft(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", true, true)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())
	ctx := t.Context()

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceLive)}); err != nil {
		t.Fatalf("waiting snapshot failed: %v", err)
	}

	styles.setActive("%1", "@1", true, false)

	if err := tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceLive)}); err != nil {
		t.Fatalf("waiting snapshot after leaving failed: %v", err)
	}

	if len(styles.setCalls) != 2 {
		t.Fatalf("set calls = %+v, want the waiting style reapplied after leaving the window", styles.setCalls)
	}
}

func TestTrackerMultiPanePriority(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)
	styles.setActive("%2", "@1", false, false)
	styles.setActive("%3", "@1", false, false)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())
	ctx := t.Context()

	_ = tracker.HandleSnapshot(ctx, []aht.Session{
		trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceLive),
		trackerSession("%2", "@1", aht.ActivityFailed, aht.PresenceLive),
		trackerSession("%3", "@1", aht.ActivityIdle, aht.PresenceLive),
	})

	if style := tracker.WindowStyle("@1"); style != agents.DefaultPalette[string(agents.StateWaiting)] {
		t.Fatalf("window style = %q, want waiting style", style)
	}

	_ = tracker.HandleSnapshot(ctx, []aht.Session{
		trackerSession("%1", "@1", aht.ActivityRunning, aht.PresenceLive),
		trackerSession("%2", "@1", aht.ActivityFailed, aht.PresenceLive),
		trackerSession("%3", "@1", aht.ActivityIdle, aht.PresenceLive),
	})

	if style := tracker.WindowStyle("@1"); style != agents.DefaultPalette[string(agents.StateFailed)] {
		t.Fatalf("window style = %q, want failed style", style)
	}

	_ = tracker.HandleSnapshot(ctx, []aht.Session{
		trackerSession("%1", "@1", aht.ActivityRunning, aht.PresenceLive),
		trackerSession("%2", "@1", aht.ActivityInterrupted, aht.PresenceLive),
		trackerSession("%3", "@1", aht.ActivityIdle, aht.PresenceLive),
	})

	if style := tracker.WindowStyle("@1"); style != "" {
		t.Fatalf("window style = %q, want cleared when all panes are normal", style)
	}
}

func TestTrackerClearsStyleWhenSessionGone(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())
	ctx := t.Context()

	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceLive)})

	if tracker.WindowStyle("@1") == "" {
		t.Fatal("expected a styled window while waiting")
	}

	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceGone)})

	if style := tracker.WindowStyle("@1"); style != "" {
		t.Fatalf("window style = %q, want cleared after the session is gone", style)
	}
}

func TestTrackerClearAllReleasesOwnedWindows(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)
	styles.setActive("%2", "@2", false, false)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())
	ctx := t.Context()

	_ = tracker.HandleSnapshot(ctx, []aht.Session{
		trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceLive),
		trackerSession("%2", "@2", aht.ActivityFailed, aht.PresenceLive),
	})

	if tracker.WindowStyle("@1") == "" || tracker.WindowStyle("@2") == "" {
		t.Fatal("expected both windows styled")
	}

	if err := tracker.ClearAll(ctx); err != nil {
		t.Fatalf("ClearAll failed: %v", err)
	}

	if len(styles.clearCalls) != 2 {
		t.Fatalf("clear calls = %d, want 2", len(styles.clearCalls))
	}

	if tracker.WindowStyle("@1") != "" || tracker.WindowStyle("@2") != "" {
		t.Fatal("tracker still reports styles after ClearAll")
	}
}

func TestTrackerIgnoresUnknownPanes(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())

	_ = tracker.HandleSnapshot(t.Context(), []aht.Session{
		trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceLive),
		trackerSession("%404", "@404", aht.ActivityWaiting, aht.PresenceLive),
	})

	if tracker.WindowStyle("@404") != "" {
		t.Fatal("a pane missing from the tmux pane list must not be styled")
	}

	if tracker.WindowStyle("@1") == "" {
		t.Fatal("live panes must still be styled")
	}
}

func TestTrackerSkipsCycleWhenTmuxQueryFails(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)
	styles.queryErr = errFakeStyles

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())

	if err := tracker.HandleSnapshot(t.Context(), []aht.Session{trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceLive)}); err != nil {
		t.Fatalf("HandleSnapshot must not fail the watch loop: %v", err)
	}

	if len(styles.setCalls) != 0 {
		t.Fatalf("cycle with unknown focus must not style: %+v", styles.setCalls)
	}
}

func TestTrackerReturnsStyleMutationFailure(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)
	styles.setErr = errFakeStyles

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())

	err := tracker.HandleSnapshot(t.Context(), []aht.Session{
		trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceLive),
	})
	if !errors.Is(err, errFakeStyles) {
		t.Fatalf("HandleSnapshot error = %v, want style mutation cause", err)
	}

	if style := tracker.WindowStyle("@1"); style != "" {
		t.Fatalf("failed mutation recorded style %q", style)
	}
}

func TestTrackerIgnoresHeadlessSessions(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())

	session := trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceLive)
	session.Process = &registry.ProcessIdentity{PID: 4242, StartIdentity: "boot:42"}
	session.Observations.Native = &registry.NativeObservation{
		Attributes: map[string]string{"aht_interaction_mode": "print"},
		Process:    *session.Process,
	}

	_ = tracker.HandleSnapshot(t.Context(), []aht.Session{session})

	if len(styles.setCalls) != 0 {
		t.Fatalf("headless runs must not style windows: %+v", styles.setCalls)
	}
}

func TestTrackerInterruptedSessionDoesNotStyle(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())

	_ = tracker.HandleSnapshot(t.Context(), []aht.Session{trackerSession("%1", "@1", aht.ActivityInterrupted, aht.PresenceLive)})

	if len(styles.setCalls) != 0 {
		t.Fatalf("interrupted agents must not style windows: %+v", styles.setCalls)
	}
}

func TestTrackerWindowVisitClearsFailedAlert(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())
	ctx := t.Context()

	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityFailed, aht.PresenceLive)})

	if !tracker.PaneFailed("%1") {
		t.Fatal("expected a failed alert")
	}

	styles.setActive("%1", "@1", true, true)

	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityFailed, aht.PresenceLive)})

	if tracker.PaneFailed("%1") {
		t.Fatal("visiting the window must clear the failed alert")
	}
}

func TestTrackerRunningClearsFailedAlert(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)

	tracker := agents.NewTracker(styles, agents.DefaultTrackerOptions())
	ctx := t.Context()

	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityFailed, aht.PresenceLive)})
	_ = tracker.HandleSnapshot(ctx, []aht.Session{trackerSession("%1", "@1", aht.ActivityRunning, aht.PresenceLive)})

	if tracker.PaneFailed("%1") {
		t.Fatal("a retry must clear the failed alert")
	}
}

func TestTrackerCustomPalette(t *testing.T) {
	t.Parallel()

	styles := newFakeStyles()
	styles.setActive("%1", "@1", false, false)

	opts := agents.DefaultTrackerOptions()
	opts.Palette = map[string]string{string(agents.StateWaiting): "fg=white,bg=blue"}

	tracker := agents.NewTracker(styles, opts)

	_ = tracker.HandleSnapshot(t.Context(), []aht.Session{trackerSession("%1", "@1", aht.ActivityWaiting, aht.PresenceLive)})

	if style := tracker.WindowStyle("@1"); style != "fg=white,bg=blue" {
		t.Fatalf("window style = %q, want the custom palette entry", style)
	}
}
