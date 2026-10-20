//go:build linux || darwin

package agents_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/zigai/aht/v2/pkg/aht"

	"github.com/zigai/sesh/internal/agents"
)

var (
	errFakeActivation = errors.New("PRIVATE_ACTIVATION_ERROR")
	errFakeRollback   = errors.New("PRIVATE_ROLLBACK_ERROR")
)

const (
	defaultServerSocket = "default"
	firstSessionName    = "one"
)

type fakeController struct {
	current           agents.CurrentContext
	panes             []agents.PaneLocation
	currentErr        error
	panesErr          error
	activateErr       error
	history           string
	historyErr        error
	setHistoryErr     error
	setHistoryFails   int
	activated         []agents.PaneLocation
	historyWrites     []string
	toggleWrites      []string
	toggleWriteErr    error
	toggleFailAt      int
	sessionWindows    map[string][]agents.SessionWindow
	sessionWindowsErr error
	historyMap        map[string]string
	lockCalls         int
	unlockCalls       int
	lockErr           error
	unlockErr         error
}

func (f *fakeController) CurrentContext(context.Context) (agents.CurrentContext, error) {
	current := f.current
	if current.ServerPID == 0 {
		current.ServerPID = 4242
	}

	return current, f.currentErr
}

func (f *fakeController) Panes(context.Context) ([]agents.PaneLocation, error) {
	return f.panes, f.panesErr
}

func (f *fakeController) SessionWindows(_ context.Context, sessionID string) ([]agents.SessionWindow, error) {
	if f.sessionWindowsErr != nil {
		return nil, f.sessionWindowsErr
	}

	if f.sessionWindows != nil {
		return f.sessionWindows[sessionID], nil
	}

	windows := make([]agents.SessionWindow, 0, len(f.panes))

	for _, pane := range f.panes {
		if pane.SessionID != sessionID {
			continue
		}

		windows = append(windows, agents.SessionWindow{
			SessionID:   pane.SessionID,
			SessionName: pane.SessionName,
			WindowID:    pane.WindowID,
			WindowIndex: pane.WindowIndex,
		})
	}

	return windows, nil
}

func (f *fakeController) Activate(_ context.Context, _ agents.CurrentContext, target agents.PaneLocation) error {
	if f.activateErr != nil {
		return f.activateErr
	}

	f.activated = append(f.activated, target)

	return nil
}

func (f *fakeController) History(_ context.Context, clientKey string) (string, error) {
	if f.historyErr != nil {
		return "", f.historyErr
	}

	if f.historyMap != nil {
		return f.historyMap[clientKey], nil
	}

	if strings.HasPrefix(clientKey, "\x00sesh-agent-toggle\x00") {
		return "", nil
	}

	return f.history, nil
}

func (f *fakeController) SetHistory(_ context.Context, clientKey string, raw string) error {
	if after, ok := strings.CutPrefix(clientKey, "\x00sesh-agent-toggle\x00"); ok {
		f.toggleWrites = append(f.toggleWrites, raw)
		if f.toggleFailAt > 0 && len(f.toggleWrites) == f.toggleFailAt {
			return f.toggleWriteErr
		}

		if f.historyMap == nil {
			f.historyMap = make(map[string]string)
			f.historyMap[after] = f.history
		}

		f.historyMap[clientKey] = raw

		return nil
	}

	f.historyWrites = append(f.historyWrites, raw)

	if f.setHistoryFails > 0 && len(f.historyWrites) == f.setHistoryFails {
		return f.setHistoryErr
	}

	if f.historyMap == nil {
		f.historyMap = make(map[string]string)
	}

	f.historyMap[clientKey] = raw
	f.history = raw

	return nil
}

func (f *fakeController) Lock(_ context.Context, _ string) error {
	f.lockCalls++
	return f.lockErr
}

func (f *fakeController) Unlock(_ context.Context, _ string) error {
	f.unlockCalls++
	return f.unlockErr
}

type fakeSource struct {
	sessions []aht.Session
	err      error
}

func (s fakeSource) List(_ context.Context, filter aht.Filter) ([]aht.Session, error) {
	if s.err != nil {
		return nil, s.err
	}

	matched := make([]aht.Session, 0, len(s.sessions))

	for _, session := range s.sessions {
		if filter.Presence != "" && session.Presence() != filter.Presence {
			continue
		}

		matched = append(matched, session)
	}

	return matched, nil
}

func newFakeNavigator(t *testing.T, controller *fakeController, sessions []aht.Session) *agents.Navigator {
	t.Helper()

	if controller.current.ServerPID == 0 {
		controller.current.ServerPID = 4242
	}

	navigator, err := agents.NewNavigator(agents.NavigatorOptions{
		Controller: controller,
		Source:     fakeSource{sessions: sessions},
	})
	if err != nil {
		t.Fatalf("NewNavigator failed: %v", err)
	}

	return navigator
}

func navigationPanes() []agents.PaneLocation {
	return []agents.PaneLocation{
		{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@1", WindowIndex: "1", PaneID: "%1", PaneIndex: "1", PanePID: 1001},
		{ServerSocket: defaultServerSocket, SessionID: "$2", SessionName: "two", WindowID: "@2", WindowIndex: "1", PaneID: "%2", PaneIndex: "1", PanePID: 1002},
		{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", WindowIndex: "2", PaneID: "%3", PaneIndex: "1", PanePID: 1003},
	}
}

func nativeHistory(t *testing.T, entries ...agents.HistoryEntry) string {
	t.Helper()

	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal native history: %v", err)
	}

	return string(data)
}

func historyEntry(paneID string, panePID int, windowID, sessionID string) agents.HistoryEntry {
	return agents.HistoryEntry{
		PaneID:       paneID,
		PanePID:      panePID,
		WindowID:     windowID,
		SessionID:    sessionID,
		ServerSocket: defaultServerSocket,
		ServerPID:    4242,
	}
}

func attentionSession(activity aht.Activity, paneID string) aht.Session {
	for _, pane := range navigationPanes() {
		if pane.PaneID == paneID {
			return attentionSessionFor(pane, activity)
		}
	}

	return aht.Session{
		Liveness: aht.Live{Activity: activity},
		Location: aht.Location{
			Kind:        aht.MultiplexerTmux,
			ServerID:    defaultServerSocket,
			SessionID:   "$1",
			SessionName: "one",
			WindowID:    "@1",
			PaneID:      paneID,
		},
	}
}

func attentionSessionFor(pane agents.PaneLocation, activity aht.Activity) aht.Session {
	return aht.Session{
		Liveness: aht.Live{Activity: activity},
		Location: aht.Location{
			Kind:        aht.MultiplexerTmux,
			ServerID:    pane.ServerSocket,
			SessionID:   pane.SessionID,
			SessionName: pane.SessionName,
			WindowID:    pane.WindowID,
			PaneID:      pane.PaneID,
			PanePID:     pane.PanePID,
		},
	}
}

func decodeHistory(t *testing.T, raw string) []string {
	t.Helper()

	if strings.TrimSpace(raw) == "" {
		return nil
	}

	var entries []agents.HistoryEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("navigation history %q is invalid: %v", raw, err)
	}

	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.PaneID)
	}

	return ids
}

func TestNextIdleWrapsAcrossSessions(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()
	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:   panes,
	}

	navigator := newFakeNavigator(t, controller, []aht.Session{
		attentionSession(aht.ActivityIdle, "%1"),
		attentionSession(aht.ActivityIdle, "%3"),
	})

	target, err := navigator.NextIdle(t.Context(), agents.NavigationGlobal)
	if err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	if target.PaneID != "%1" {
		t.Fatalf("target pane = %q, want wrapped %%1", target.PaneID)
	}

	if len(controller.activated) != 1 || controller.activated[0].PaneID != "%1" {
		t.Fatalf("activated panes = %+v, want %%1", controller.activated)
	}

	if got := decodeHistory(t, controller.history); !reflect.DeepEqual(got, []string{"%3"}) {
		t.Fatalf("history = %v, want [%%3]", got)
	}
}

func TestNextIdleCurrentSessionSkipsOtherSessions(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:   navigationPanes(),
	}

	navigator := newFakeNavigator(t, controller, []aht.Session{
		attentionSession(aht.ActivityIdle, "%1"),
		attentionSession(aht.ActivityIdle, "%2"),
	})

	target, err := navigator.NextIdle(t.Context(), agents.NavigationCurrentSession)
	if err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	if target.PaneID != "%1" {
		t.Fatalf("target pane = %q, want %%1 in the invoking session", target.PaneID)
	}
}

func TestNextIdleSkipsRunningAgents(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@1", PaneID: "%1"},
		panes:   navigationPanes(),
	}

	navigator := newFakeNavigator(t, controller, []aht.Session{
		attentionSession(aht.ActivityRunning, "%2"),
	})

	_, err := navigator.NextIdle(t.Context(), agents.NavigationGlobal)
	if !errors.Is(err, agents.ErrNoTargetPane) {
		t.Fatalf("error = %v, want ErrNoTargetPane", err)
	}

	if len(controller.activated) != 0 {
		t.Fatalf("unexpected activations: %+v", controller.activated)
	}
}

func TestNextIdleSelectsInterruptedAgent(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@1", PaneID: "%1"},
		panes:   navigationPanes(),
	}
	navigator := newFakeNavigator(t, controller, []aht.Session{attentionSession(aht.ActivityInterrupted, "%3")})

	target, err := navigator.NextIdle(t.Context(), agents.NavigationGlobal)
	if err != nil {
		t.Fatal(err)
	}

	if target.PaneID != "%3" {
		t.Fatalf("target pane = %q, want %%3", target.PaneID)
	}
}

func TestNextIdleIgnoresAgentsOnOtherServers(t *testing.T) {
	t.Parallel()

	other := attentionSession(aht.ActivityWaiting, "%2")
	other.Location.ServerID = "/tmp/other/socket"

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@1", PaneID: "%1"},
		panes:   navigationPanes(),
	}

	navigator := newFakeNavigator(t, controller, []aht.Session{other})

	_, err := navigator.NextIdle(t.Context(), agents.NavigationGlobal)
	if !errors.Is(err, agents.ErrNoTargetPane) {
		t.Fatalf("error = %v, want ErrNoTargetPane for another server", err)
	}
}

func TestNextIdleRequiresLiveCurrentPane(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@9", PaneID: "%99"},
		panes:   navigationPanes(),
	}

	navigator := newFakeNavigator(t, controller, []aht.Session{attentionSession(aht.ActivityIdle, "%1")})

	_, err := navigator.NextIdle(t.Context(), agents.NavigationGlobal)
	if !errors.Is(err, agents.ErrCurrentPaneNotLive) {
		t.Fatalf("error = %v, want ErrCurrentPaneNotLive", err)
	}
}

func TestNextIdleActivationFailureRestoresHistory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		setHistoryFails int
		rollbackErr     error
		wantLogRecords  int
		wantHistoryIDs  []string
	}{
		{name: "successful rollback stays silent", setHistoryFails: 0, wantLogRecords: 0, wantHistoryIDs: nil},
		{name: "failed rollback warns", setHistoryFails: 2, rollbackErr: errFakeRollback, wantLogRecords: 1, wantHistoryIDs: []string{"%3"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer

			ctx := zerolog.New(&output).Level(zerolog.InfoLevel).With().
				Str("command", "agent.next").
				Str("version", "PRIVATE_BASE_VERSION").
				Logger().WithContext(context.Background())

			controller := &fakeController{
				current:         agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
				panes:           navigationPanes(),
				activateErr:     errFakeActivation,
				setHistoryFails: test.setHistoryFails,
				setHistoryErr:   test.rollbackErr,
			}

			navigator := newFakeNavigator(t, controller, []aht.Session{attentionSession(aht.ActivityIdle, "%1")})

			_, err := navigator.NextIdle(ctx, agents.NavigationGlobal)
			if !errors.Is(err, errFakeActivation) {
				t.Fatalf("error = %v, want the activation error", err)
			}

			if test.rollbackErr != nil && !errors.Is(err, test.rollbackErr) {
				t.Fatalf("error = %v, want the joined rollback error", err)
			}

			if got := decodeHistory(t, controller.history); !reflect.DeepEqual(got, test.wantHistoryIDs) {
				t.Fatalf("history = %v, want %v", got, test.wantHistoryIDs)
			}

			records := decodeLogRecords(t, &output)
			if len(records) != test.wantLogRecords {
				t.Fatalf("log records = %d, want %d: %s", len(records), test.wantLogRecords, output.String())
			}

			if test.wantLogRecords == 1 {
				assertLogField(t, records[0], "level", "warn")
				assertLogField(t, records[0], "component", "agents")
				assertLogField(t, records[0], "operation", "history_rollback")
				assertLogField(t, records[0], "status", "failed")
				assertLogField(t, records[0], "message", "agent navigation history rollback failed")
			}

			if test.wantLogRecords == 1 {
				assertLogField(t, records[0], "error", errFakeRollback.Error())
			}
		})
	}
}

func TestNextIdleReturnsNavigationUnlockFailure(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current:   agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:     navigationPanes(),
		unlockErr: errFakeRollback,
	}
	navigator := newFakeNavigator(t, controller, []aht.Session{attentionSession(aht.ActivityIdle, "%1")})

	_, err := navigator.NextIdle(t.Context(), agents.NavigationGlobal)
	if !errors.Is(err, errFakeRollback) {
		t.Fatalf("NextIdle error = %v, want unlock cause", err)
	}

	if len(controller.activated) != 1 {
		t.Fatalf("activation count = %d, want 1 before unlock failure", len(controller.activated))
	}
}

func TestNextIdleLogsActivationTopology(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	ctx := zerolog.New(&output).Level(zerolog.InfoLevel).With().
		Str("command", "agent.next").
		Logger().WithContext(context.Background())

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:   navigationPanes(),
	}

	navigator := newFakeNavigator(t, controller, []aht.Session{attentionSession(aht.ActivityIdle, "%2")})

	if _, err := navigator.NextIdle(ctx, agents.NavigationGlobal); err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	records := decodeLogRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("log records = %d, want 1: %s", len(records), output.String())
	}

	record := records[0]

	assertLogField(t, record, "level", "info")
	assertLogField(t, record, "component", "agents")
	assertLogField(t, record, "operation", "next_idle")
	assertLogField(t, record, "status", "ok")
	assertLogField(t, record, "scope", "global")
	assertLogField(t, record, "cross_session", true)
	assertLogField(t, record, "cross_window", true)
	assertLogField(t, record, "message", "agent pane activated")
}

func TestNextIdleLocalScopeLogsCurrentSession(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	ctx := zerolog.New(&output).Level(zerolog.InfoLevel).With().Logger().WithContext(context.Background())

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@1", PaneID: "%1"},
		panes:   navigationPanes(),
	}

	navigator := newFakeNavigator(t, controller, []aht.Session{attentionSession(aht.ActivityIdle, "%3")})

	if _, err := navigator.NextIdle(ctx, agents.NavigationCurrentSession); err != nil {
		t.Fatalf("NextIdle failed: %v", err)
	}

	records := decodeLogRecords(t, &output)
	if len(records) != 1 {
		t.Fatalf("log records = %d, want 1: %s", len(records), output.String())
	}

	assertLogField(t, records[0], "scope", "current_session")
	assertLogField(t, records[0], "cross_session", false)
	assertLogField(t, records[0], "cross_window", true)
}

func TestBackPopsHistoryAndSkipsDeadPanes(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, ClientName: "client-a", SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:   navigationPanes(),
		history: nativeHistory(
			t,
			historyEntry("%1", 1001, "@1", "$1"),
			historyEntry("%dead", 1998, "@dead", "$1"),
			historyEntry("%2", 1002, "@2", "$2"),
		),
	}

	navigator := newFakeNavigator(t, controller, nil)

	target, err := navigator.Back(t.Context())
	if err != nil {
		t.Fatalf("Back failed: %v", err)
	}

	if target.PaneID != "%2" {
		t.Fatalf("back target = %q, want %%2", target.PaneID)
	}

	if got := decodeHistory(t, controller.history); !reflect.DeepEqual(got, []string{"%1", "%dead"}) {
		t.Fatalf("history = %v, want [%%1 %%dead]", got)
	}

	controller.current = agents.CurrentContext{ServerSocket: defaultServerSocket, ClientName: "client-a", SessionID: "$2", SessionName: "two", WindowID: "@2", PaneID: "%2"}

	target, err = navigator.Back(t.Context())
	if err != nil {
		t.Fatalf("second Back failed: %v", err)
	}

	if target.PaneID != "%1" {
		t.Fatalf("second back target = %q, want %%1", target.PaneID)
	}

	if got := decodeHistory(t, controller.history); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("history = %v, want empty", got)
	}
}

func TestBackEmptyHistory(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:   navigationPanes(),
	}

	navigator := newFakeNavigator(t, controller, nil)

	_, err := navigator.Back(t.Context())
	if !errors.Is(err, agents.ErrEmptyNavigationHistory) {
		t.Fatalf("error = %v, want ErrEmptyNavigationHistory", err)
	}
}

func TestBackClearsStaleHistory(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:   navigationPanes(),
		history: nativeHistory(
			t,
			historyEntry("%dead", 1998, "@dead", "$1"),
			historyEntry("%gone", 1999, "@gone", "$1"),
		),
	}

	navigator := newFakeNavigator(t, controller, nil)

	_, err := navigator.Back(t.Context())
	if !errors.Is(err, agents.ErrNoTargetPane) {
		t.Fatalf("error = %v, want ErrNoTargetPane", err)
	}

	if got := decodeHistory(t, controller.history); len(got) != 0 {
		t.Fatalf("stale history = %v, want cleared", got)
	}

	if len(controller.activated) != 0 {
		t.Fatalf("unexpected activations: %+v", controller.activated)
	}
}

func TestBackSkipsCurrentPane(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:   navigationPanes(),
		history: nativeHistory(t, historyEntry("%3", 1003, "@3", "$1")),
	}

	navigator := newFakeNavigator(t, controller, nil)

	_, err := navigator.Back(t.Context())
	if !errors.Is(err, agents.ErrNoTargetPane) {
		t.Fatalf("error = %v, want ErrNoTargetPane", err)
	}
}

func TestCorruptHistoryFailsClosed(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@1", PaneID: "%1"},
		panes:   navigationPanes(),
		history: "{not-json",
	}

	navigator := newFakeNavigator(t, controller, []aht.Session{attentionSession(aht.ActivityIdle, "%3")})

	_, err := navigator.NextIdle(t.Context(), agents.NavigationGlobal)
	if err == nil {
		t.Fatal("expected failure on corrupt history, got nil")
	}

	if len(controller.activated) != 0 {
		t.Fatalf("navigation must not activate with corrupt history: %+v", controller.activated)
	}

	if controller.history != "{not-json" {
		t.Fatalf("corrupt history was overwritten: %q", controller.history)
	}
}

func TestStringOnlyNavigationHistoryIsRejected(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:   navigationPanes(),
		history: `["%1"]`,
	}
	navigator := newFakeNavigator(t, controller, nil)

	_, err := navigator.Back(t.Context())
	if !errors.Is(err, agents.ErrNavigationHistoryCorrupt) {
		t.Fatalf("Back error = %v, want ErrNavigationHistoryCorrupt", err)
	}

	if len(controller.activated) != 0 || controller.history != `["%1"]` {
		t.Fatalf("corrupt history caused mutation: activated=%+v history=%q", controller.activated, controller.history)
	}
}

func TestFocusRequiresLivePaneAndMatchingSession(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@1", PaneID: "%1"},
		panes:   navigationPanes(),
	}

	navigator := newFakeNavigator(t, controller, nil)

	target, err := navigator.Focus(t.Context(), agents.FocusTarget{PaneID: "%3", SessionName: firstSessionName})
	if err != nil {
		t.Fatalf("Focus failed: %v", err)
	}

	if target.PaneID != "%3" || target.WindowID != "@3" {
		t.Fatalf("focus target = %+v, want %%3 in @3", target)
	}

	if got := decodeHistory(t, controller.history); !reflect.DeepEqual(got, []string{"%1"}) {
		t.Fatalf("history = %v, want [%%1]", got)
	}

	if _, err := navigator.Focus(t.Context(), agents.FocusTarget{PaneID: "%2", SessionName: firstSessionName}); !errors.Is(err, agents.ErrPaneNotLive) {
		t.Fatalf("session mismatch error = %v, want ErrPaneNotLive", err)
	}

	if _, err := navigator.Focus(t.Context(), agents.FocusTarget{PaneID: "%404"}); !errors.Is(err, agents.ErrPaneNotLive) {
		t.Fatalf("missing pane error = %v, want ErrPaneNotLive", err)
	}
}

func TestFocusUsesRequestedSessionLinkForSharedPane(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()
	panes = append(panes, agents.PaneLocation{
		ServerSocket: defaultServerSocket,
		SessionID:    "$2",
		SessionName:  "two",
		WindowID:     "@1",
		WindowIndex:  "4",
		PaneID:       "%1",
		PaneIndex:    "1",
		PanePID:      1001,
	})
	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:   panes,
	}
	navigator := newFakeNavigator(t, controller, nil)

	target, err := navigator.Focus(t.Context(), agents.FocusTarget{PaneID: "%1", SessionName: "two"})
	if err != nil {
		t.Fatalf("Focus failed: %v", err)
	}

	if target.SessionID != "$2" || target.SessionName != "two" || target.WindowIndex != "4" {
		t.Fatalf("Focus target = %+v, want requested shared-window link", target)
	}
}

func TestFocusOnCurrentPaneIsANoOp(t *testing.T) {
	t.Parallel()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, WindowID: "@1", PaneID: "%1"},
		panes:   navigationPanes(),
	}

	navigator := newFakeNavigator(t, controller, nil)

	if _, err := navigator.Focus(t.Context(), agents.FocusTarget{PaneID: "%1"}); err != nil {
		t.Fatalf("Focus failed: %v", err)
	}

	if len(controller.activated) != 0 || len(controller.historyWrites) != 0 {
		t.Fatalf("focusing the current pane must not activate or record history: %+v %+v", controller.activated, controller.historyWrites)
	}
}

func TestAttentionVocabulary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		activity string
		want     string
	}{
		{activity: string(aht.ActivityWaiting), want: "waiting"},
		{activity: string(aht.ActivityFailed), want: "failed"},
		{activity: string(aht.ActivityIdle), want: "idle"},
		{activity: string(aht.ActivityRunning), want: "normal"},
		{activity: string(aht.ActivityInterrupted), want: "idle"},
		{activity: string(aht.ActivityUnknown), want: "normal"},
		{activity: "", want: "normal"},
	}

	for _, test := range tests {
		if got := agents.AttentionFor(test.activity); got != test.want {
			t.Fatalf("AttentionFor(%q) = %q, want %q", test.activity, got, test.want)
		}
	}
}

func decodeLogRecords(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()

	text := strings.TrimSpace(output.String())
	if text == "" {
		return nil
	}

	lines := strings.Split(text, "\n")

	records := make([]map[string]any, 0, len(lines))

	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decoding log record %q: %v", line, err)
		}

		records = append(records, record)
	}

	return records
}

func assertLogField(t *testing.T, record map[string]any, field string, want any) {
	t.Helper()

	if got := record[field]; !reflect.DeepEqual(got, want) {
		t.Fatalf("log field %q = %#v, want %#v; record = %#v", field, got, want, record)
	}
}

func TestNextIdleConcurrentMutationPreservesHistory(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	ctx := zerolog.New(&output).Level(zerolog.InfoLevel).With().Logger().WithContext(context.Background())

	controller := &fakeController{
		current:     agents.CurrentContext{ServerSocket: defaultServerSocket, ClientName: "client-a", SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:       navigationPanes(),
		activateErr: errFakeActivation,
	}

	controller.activateErr = errFakeActivation
	origActivate := controller.Activate
	_ = origActivate

	concurrentRaw := `[{"paneId":"%concurrent"}]`

	failingController := &concurrentMutationController{
		fakeController: controller,
		onActivate: func() {
			controller.historyMap["client-a"] = concurrentRaw
			controller.history = concurrentRaw
		},
	}

	mutatingNav, err := agents.NewNavigator(agents.NavigatorOptions{
		Controller: failingController,
		Source:     fakeSource{sessions: []aht.Session{attentionSession(aht.ActivityIdle, "%1")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = mutatingNav.NextIdle(ctx, agents.NavigationGlobal)
	if !errors.Is(err, errFakeActivation) {
		t.Fatalf("error = %v, want errFakeActivation", err)
	}

	if got := controller.historyMap["client-a"]; got != concurrentRaw {
		t.Fatalf("history was overwritten: got %q, want preserved %q", got, concurrentRaw)
	}

	records := decodeLogRecords(t, &output)
	foundSkippedRollback := false

	for _, r := range records {
		if r["reason"] == "concurrent_mutation_detected" {
			foundSkippedRollback = true
			break
		}
	}

	if !foundSkippedRollback {
		t.Fatalf("expected concurrent_mutation_detected log warning, got: %s", output.String())
	}
}

type concurrentMutationController struct {
	*fakeController

	onActivate func()
}

func (c *concurrentMutationController) Activate(ctx context.Context, current agents.CurrentContext, target agents.PaneLocation) error {
	if c.onActivate != nil {
		c.onActivate()
	}

	return c.fakeController.Activate(ctx, current, target)
}

func TestBackRejectsProcessIncarnationMismatch(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	ctx := zerolog.New(&output).Level(zerolog.InfoLevel).With().Logger().WithContext(context.Background())

	panes := navigationPanes()
	panes[0].PanePID = 9999

	historyRaw := nativeHistory(t, historyEntry("%1", 1111, "@1", "$1"))

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, ClientName: "client-a", SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:   panes,
		history: historyRaw,
	}

	navigator := newFakeNavigator(t, controller, nil)

	_, err := navigator.Back(ctx)
	if !errors.Is(err, agents.ErrNoTargetPane) {
		t.Fatalf("error = %v, want ErrNoTargetPane when history pane process incarnation changed", err)
	}

	if len(controller.activated) != 0 {
		t.Fatalf("respawned pane must not be activated: %+v", controller.activated)
	}

	records := decodeLogRecords(t, &output)
	foundMismatch := false

	for _, r := range records {
		if r["reason"] == "process_incarnation_mismatch" {
			foundMismatch = true
			break
		}
	}

	if !foundMismatch {
		t.Fatalf("expected process_incarnation_mismatch warning, got: %s", output.String())
	}
}

func TestNavigationIsolatesHistoryByClientKey(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()

	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, ClientName: "client-a", SessionID: "$1", SessionName: firstSessionName, WindowID: "@3", PaneID: "%3"},
		panes:   panes,
	}

	sessions := []aht.Session{
		attentionSession(aht.ActivityIdle, "%1"),
		attentionSession(aht.ActivityIdle, "%2"),
	}

	navigator := newFakeNavigator(t, controller, sessions)

	if _, err := navigator.NextIdle(t.Context(), agents.NavigationGlobal); err != nil {
		t.Fatalf("Client A NextIdle failed: %v", err)
	}

	controller.current = agents.CurrentContext{ServerSocket: defaultServerSocket, ClientName: "client-b", SessionID: "$1", SessionName: firstSessionName, WindowID: "@1", PaneID: "%1"}

	if _, err := navigator.NextIdle(t.Context(), agents.NavigationGlobal); err != nil {
		t.Fatalf("Client B NextIdle failed: %v", err)
	}

	histA := decodeHistory(t, controller.historyMap["client-a"])
	histB := decodeHistory(t, controller.historyMap["client-b"])

	if !reflect.DeepEqual(histA, []string{"%3"}) {
		t.Fatalf("client A history = %v, want [%%3]", histA)
	}

	if !reflect.DeepEqual(histB, []string{"%1"}) {
		t.Fatalf("client B history = %v, want [%%1]", histB)
	}

	controller.current = agents.CurrentContext{ServerSocket: defaultServerSocket, ClientName: "client-a", SessionID: "$1", SessionName: firstSessionName, WindowID: "@1", PaneID: "%1"}

	target, err := navigator.Back(t.Context())
	if err != nil {
		t.Fatalf("Client A Back failed: %v", err)
	}

	if target.PaneID != "%3" {
		t.Fatalf("Client A Back returned %q, want %%3", target.PaneID)
	}

	histBAfter := decodeHistory(t, controller.historyMap["client-b"])
	if !reflect.DeepEqual(histBAfter, []string{"%1"}) {
		t.Fatalf("client B history changed after client A Back: %v", histBAfter)
	}
}
