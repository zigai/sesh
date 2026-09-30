//go:build linux || darwin

package agents_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/zigai/aht/v2/pkg/aht"
	"github.com/zigai/aht/v2/pkg/registry"

	"github.com/zigai/sesh/internal/agents"
)

func idleSessionAt(pane agents.PaneLocation, minute int) aht.Session {
	s := attentionSessionFor(pane, aht.ActivityIdle)
	s.ActivityChangedAt = time.Date(2026, 9, 30, 12, minute, 0, 0, time.UTC)

	return s
}

func TestLatestIdleSelectsMostRecentTransitionAndBackReturnsToOrigin(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()
	controller := &fakeController{current: agents.CurrentContext{ServerSocket: defaultServerSocket, ClientName: "invoking-client", SessionID: "$1", PaneID: "%1"}, panes: panes}
	old := idleSessionAt(panes[2], 1)
	old.UpdatedAt = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	n := newFakeNavigator(t, controller, []aht.Session{old, idleSessionAt(panes[1], 2)})

	target, err := n.LatestIdle(t.Context(), agents.NavigationGlobal)
	if err != nil || target.PaneID != "%2" {
		t.Fatalf("target = %+v, err = %v", target, err)
	}

	if got := decodeHistory(t, controller.history); !reflect.DeepEqual(got, []string{"%1"}) {
		t.Fatalf("history = %v", got)
	}

	controller.current = agents.CurrentContext{ServerSocket: defaultServerSocket, ClientName: "invoking-client", SessionID: "$2", PaneID: "%2"}

	back, err := n.Back(t.Context())
	if err != nil || back.PaneID != "%1" {
		t.Fatalf("back = %+v, err = %v", back, err)
	}
}

func TestLatestIdleTiesUsePaneOrderAndCurrentWinnerDoesNotWriteHistory(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()
	controller := &fakeController{current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", PaneID: "%1"}, panes: panes}
	n := newFakeNavigator(t, controller, []aht.Session{idleSessionAt(panes[2], 1), idleSessionAt(panes[0], 1)})

	target, err := n.LatestIdle(t.Context(), agents.NavigationGlobal)
	if err != nil || target.PaneID != "%1" {
		t.Fatalf("target = %+v, err = %v", target, err)
	}

	if len(controller.activated) != 0 || len(controller.historyWrites) != 0 || controller.lockCalls != 0 {
		t.Fatalf("current winner mutated navigation state: %+v", controller)
	}
}

func TestLatestIdleFiltersServerBeforeDeduplication(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()
	otherPane := panes[1]
	otherPane.ServerSocket = "other"
	controller := &fakeController{current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", PaneID: "%1"}, panes: append(panes, otherPane)}
	n := newFakeNavigator(t, controller, []aht.Session{idleSessionAt(otherPane, 5), idleSessionAt(panes[1], 2)})

	target, err := n.LatestIdle(t.Context(), agents.NavigationGlobal)
	if err != nil || target.PaneID != "%2" || target.ServerSocket != defaultServerSocket {
		t.Fatalf("target = %+v, err = %v", target, err)
	}
}

func TestLatestIdleLocalScopeRetargetsLinkedWindow(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()
	controller := &fakeController{
		current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", SessionName: firstSessionName, PaneID: "%1"}, panes: panes,
		sessionWindows: map[string][]agents.SessionWindow{"$1": {{SessionID: "$1", SessionName: firstSessionName, WindowID: "@2", WindowIndex: "4"}, {SessionID: "$1", SessionName: firstSessionName, WindowID: "@1", WindowIndex: "1"}}},
	}
	n := newFakeNavigator(t, controller, []aht.Session{idleSessionAt(panes[1], 2), idleSessionAt(panes[2], 5)})

	target, err := n.LatestIdle(t.Context(), agents.NavigationCurrentSession)
	if err != nil || target.PaneID != "%2" || target.SessionID != "$1" || target.WindowIndex != "4" {
		t.Fatalf("target = %+v, err = %v", target, err)
	}
}

func TestLatestIdleRejectsIneligibleRecordsWithoutSuccessLogs(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()

	for _, name := range []string{"running", "waiting", "failed", "interrupted", "unknown", "zero_time", "vanished", "wrong_process", "wrong_session", "noninteractive", "ended"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := ineligibleIdleSession(panes[1], name)

			controller := &fakeController{current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", PaneID: "%1"}, panes: panes}
			n := newFakeNavigator(t, controller, []aht.Session{s})

			var output bytes.Buffer

			ctx := zerolog.New(&output).WithContext(t.Context())
			_, err := n.LatestIdle(ctx, agents.NavigationGlobal)

			enriched, ok := oops.AsOops(err)
			if !errors.Is(err, agents.ErrNoTargetPane) || !ok || enriched.Code() != "not_found" {
				t.Fatalf("error = %v, code = %q", err, enriched.Code())
			}

			if output.Len() != 0 || len(controller.historyWrites) != 0 || len(controller.activated) != 0 {
				t.Fatalf("ineligible record produced effects: %s", output.String())
			}
		})
	}
}

func TestLatestIdleLogsOnlyPrivacySafeOwnedSuccess(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()

	for _, tc := range []struct {
		name  string
		scope agents.NavigationScope
	}{{"global", agents.NavigationGlobal}, {"current_session", agents.NavigationCurrentSession}} {
		t.Run(tc.name, func(t *testing.T) {
			scope := tc.scope

			t.Parallel()

			controller := &fakeController{current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", PaneID: "%1"}, panes: panes}
			n := newFakeNavigator(t, controller, []aht.Session{idleSessionAt(panes[2], 1)})

			var output bytes.Buffer

			ctx := zerolog.New(&output).WithContext(context.Background())
			if _, err := n.LatestIdle(ctx, scope); err != nil {
				t.Fatal(err)
			}

			records := decodeLogRecords(t, &output)
			if len(records) != 1 {
				t.Fatalf("records = %v", records)
			}

			r := records[0]

			wantScope := "global"
			if scope == agents.NavigationCurrentSession {
				wantScope = "current_session"
			}

			want := map[string]any{"level": "info", "component": "agents", "operation": "latest_idle", "status": "ok", "cross_session": false, "cross_window": true, "scope": wantScope, "message": "agent pane activated"}
			if !reflect.DeepEqual(r, want) {
				t.Fatalf("record = %#v, want %#v", r, want)
			}
		})
	}
}

func TestLatestIdleActivationFailurePreservesCauseAndHistory(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()
	controller := &fakeController{current: agents.CurrentContext{ServerSocket: defaultServerSocket, SessionID: "$1", PaneID: "%1"}, panes: panes, activateErr: errFakeActivation}
	n := newFakeNavigator(t, controller, []aht.Session{idleSessionAt(panes[1], 1)})

	var output bytes.Buffer

	_, err := n.LatestIdle(zerolog.New(&output).WithContext(t.Context()), agents.NavigationGlobal)
	if !errors.Is(err, errFakeActivation) || controller.history != "" || output.Len() != 0 {
		t.Fatalf("error = %v, history = %q, logs = %s", err, controller.history, output.String())
	}
}

func ineligibleIdleSession(pane agents.PaneLocation, name string) aht.Session {
	s := idleSessionAt(pane, 1)

	switch name {
	case "zero_time":
		s.ActivityChangedAt = time.Time{}
	case "vanished":
		s.Location.PaneID = "%404"
	case "wrong_process":
		s.Location.PanePID++
	case "wrong_session":
		s.Location.SessionID = "$404"
	case "noninteractive":
		s.Observations.Native = &registry.NativeObservation{Attributes: map[string]string{"aht_interaction_mode": "print"}}
	case "ended":
		s.Liveness = aht.Gone{}
	default:
		s.Liveness = aht.Live{Activity: aht.Activity(name)}
	}

	return s
}
