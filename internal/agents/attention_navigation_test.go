//go:build linux || darwin

package agents_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/rs/zerolog"
	"github.com/zigai/aht/v2/pkg/aht"

	"github.com/zigai/sesh/internal/agents"
)

func paneContext(pane agents.PaneLocation, client string) agents.CurrentContext {
	return agents.CurrentContext{ServerSocket: pane.ServerSocket, ClientName: client, SessionID: pane.SessionID, SessionName: pane.SessionName, WindowID: pane.WindowID, PaneID: pane.PaneID, PanePID: pane.PanePID, ServerPID: 4242}
}

func TestAttentionActionsOwnPrivacySafeNavigationRecords(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"waiting", "failed", "unread", "toggle"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()

			panes := navigationPanes()
			controller := &fakeController{current: paneContext(panes[0], "client"), panes: panes}

			s := idleSessionAt(panes[1], 1)
			if operation == "waiting" || operation == "failed" {
				s.Liveness = aht.Live{Activity: aht.Activity(operation)}
			}

			n := newFakeNavigator(t, controller, []aht.Session{s})
			if operation == "toggle" {
				if _, err := n.LatestIdle(t.Context(), agents.NavigationGlobal); err != nil {
					t.Fatal(err)
				}
			}

			var logs bytes.Buffer

			ctx := zerolog.New(&logs).WithContext(t.Context())

			navigateLoggedAction(t, ctx, n, operation)

			records := decodeLogRecords(t, &logs)

			want := map[string]any{"level": "info", "component": "agents", "operation": operation, "status": "ok", "cross_session": true, "cross_window": true, "scope": "global", "message": "agent pane activated"}
			if len(records) != 1 || !reflect.DeepEqual(records[0], want) {
				t.Fatalf("records = %#v, want %#v", records, want)
			}
		})
	}
}

func navigateLoggedAction(t *testing.T, ctx context.Context, n *agents.Navigator, operation string) {
	t.Helper()

	var err error

	switch operation {
	case "waiting":
		_, err = n.Waiting(ctx, agents.NavigationGlobal)
	case "failed":
		_, err = n.Failed(ctx, agents.NavigationGlobal)
	case "unread":
		_, err = n.UnreadIdle(ctx, agents.NavigationGlobal, nil)
	case "toggle":
		_, err = n.Toggle(ctx, agents.NavigationGlobal)
	}

	if err != nil {
		t.Fatal(err)
	}
}

func TestUnreadIdleUsesObservedTransitionAndReturnsCurrentObservation(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()
	controller := &fakeController{current: paneContext(panes[0], "client"), panes: panes}
	n := newFakeNavigator(t, controller, []aht.Session{idleSessionAt(panes[0], 5), idleSessionAt(panes[1], 3), idleSessionAt(panes[2], 2)})

	visit, err := n.UnreadIdle(t.Context(), agents.NavigationGlobal, nil)
	if err != nil || visit.Pane.PaneID != "%2" || !visit.ActivityChanged.Equal(idleSessionAt(panes[1], 3).ActivityChangedAt) || visit.Origin == nil || visit.Origin.PaneID != "%1" {
		t.Fatalf("visit = %+v, error = %v", visit, err)
	}

	visits := make([]agents.PaneVisit, 0, 3)
	visits = append(visits, *visit.Origin, agents.PaneVisit{ServerSocket: visit.Pane.ServerSocket, SessionName: "before-rename", PaneID: visit.Pane.PaneID, ViewedAt: visit.ActivityChanged})
	controller.current = paneContext(panes[1], "client")

	second, err := n.UnreadIdle(t.Context(), agents.NavigationGlobal, visits)
	if err != nil || second.Pane.PaneID != "%3" {
		t.Fatalf("second = %+v, error = %v", second, err)
	}

	visits = append(visits, agents.PaneVisit{ServerSocket: second.Pane.ServerSocket, PaneID: second.Pane.PaneID, ViewedAt: second.ActivityChanged})
	controller.current = paneContext(panes[2], "client")

	var logs bytes.Buffer

	last, err := n.UnreadIdle(zerolog.New(&logs).WithContext(t.Context()), agents.NavigationGlobal, visits)
	if !errors.Is(err, agents.ErrNoTargetPane) || last.Origin == nil || logs.Len() != 0 {
		t.Fatalf("last = %+v, error = %v, logs = %q", last, err, logs.String())
	}
}
