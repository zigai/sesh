//go:build linux || darwin

package agents_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/zigai/aht/v2/pkg/aht"

	"github.com/zigai/sesh/internal/agents"
)

func establishedToggle(t *testing.T) (*agents.Navigator, *fakeController) {
	t.Helper()

	panes := navigationPanes()
	controller := &fakeController{current: paneContext(panes[0], "client"), panes: panes}

	n := newFakeNavigator(t, controller, []aht.Session{idleSessionAt(panes[1], 1)})
	if _, err := n.LatestIdle(t.Context(), agents.NavigationGlobal); err != nil {
		t.Fatal(err)
	}

	controller.current = paneContext(panes[1], "client")

	return n, controller
}

func TestToggleReturnsRepeatedlyAndReplacesClosedWorkingPane(t *testing.T) {
	t.Parallel()

	n, controller := establishedToggle(t)
	for _, want := range []string{"%1", "%2", "%1"} {
		target, err := n.Toggle(t.Context(), agents.NavigationGlobal)
		if err != nil || target.PaneID != want {
			t.Fatalf("target = %+v, error = %v, want %s", target, err, want)
		}

		controller.current = paneContext(target, "client")
	}

	controller.panes = controller.panes[1:]
	controller.current = paneContext(controller.panes[1], "client")

	target, err := n.Toggle(t.Context(), agents.NavigationGlobal)
	if err != nil || target.PaneID != "%2" {
		t.Fatalf("replacement target = %+v, error = %v", target, err)
	}

	controller.current = paneContext(target, "client")

	target, err = n.Toggle(t.Context(), agents.NavigationGlobal)
	if err != nil || target.PaneID != "%3" {
		t.Fatalf("replacement return = %+v, error = %v", target, err)
	}
}

func TestToggleRejectsStaleEndpointsAndCorruptStateWithoutNavigation(t *testing.T) {
	t.Parallel()

	for _, reason := range []string{"pane_pid", "server_pid", "window", "session", "socket", "corrupt"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			n, controller := establishedToggle(t)

			switch reason {
			case "pane_pid":
				controller.panes[0].PanePID++
			case "server_pid":
				controller.current.ServerPID++
			case "window":
				controller.panes[0].WindowID = "@999"
			case "session":
				controller.panes[0].SessionID = "$999"
			case "socket":
				controller.panes[0].ServerSocket = "other"
			case "corrupt":
				controller.historyMap["\x00sesh-agent-toggle\x00client"] = `{"working":{}}`
			}

			before := len(controller.activated)

			var logs bytes.Buffer

			_, err := n.Toggle(zerolog.New(&logs).WithContext(t.Context()), agents.NavigationGlobal)
			if err == nil || len(controller.activated) != before || logs.Len() != 0 {
				t.Fatalf("error = %v, activations = %v, logs = %q", err, controller.activated, logs.String())
			}
		})
	}
}

func TestToggleActivationFailurePreservesBothStatesAndIndependentRollbackFailure(t *testing.T) {
	t.Parallel()

	for _, rollbackFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "restored", true: "cleanup_cause_retained"}[rollbackFails], func(t *testing.T) {
			t.Parallel()
			n, controller := establishedToggle(t)
			previous := controller.historyMap["\x00sesh-agent-toggle\x00client"]
			history := controller.history

			controller.activateErr = errFakeActivation
			if rollbackFails {
				controller.toggleFailAt = len(controller.toggleWrites) + 2
				controller.toggleWriteErr = errFakeRollback
			}

			var logs bytes.Buffer

			_, err := n.Toggle(zerolog.New(&logs).WithContext(t.Context()), agents.NavigationGlobal)
			if !errors.Is(err, errFakeActivation) || errors.Is(err, errFakeRollback) != rollbackFails || controller.history != history || logs.Len() != 0 {
				t.Fatalf("error = %v, history = %q, logs = %q", err, controller.history, logs.String())
			}

			if !rollbackFails && controller.historyMap["\x00sesh-agent-toggle\x00client"] != previous {
				t.Fatal("toggle pair was not restored")
			}
		})
	}
}

func TestFocusOrdinaryPaneAndUnavailableRegistryPreserveLastAgent(t *testing.T) {
	t.Parallel()

	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary_pane", true: "unavailable_registry"}[unavailable], func(t *testing.T) {
			t.Parallel()
			n, controller := establishedToggle(t)

			if unavailable {
				var err error

				n, err = agents.NewNavigator(agents.NavigatorOptions{Controller: controller, Source: fakeSource{err: errFakeActivation}})
				if err != nil {
					t.Fatal(err)
				}
			}

			target, err := n.Focus(t.Context(), agents.FocusTarget{PaneID: "%3", SessionID: "$1"})
			if err != nil {
				t.Fatal(err)
			}

			controller.current = paneContext(target, "client")

			target, err = n.Toggle(t.Context(), agents.NavigationGlobal)
			if err != nil || target.PaneID != "%2" {
				t.Fatalf("target = %+v, error = %v", target, err)
			}
		})
	}
}

func TestAgentNavigationChangesAgentWithoutChangingWorkingPaneAndNoopKeepsPair(t *testing.T) {
	t.Parallel()
	_, controller := establishedToggle(t)
	n := newFakeNavigator(t, controller, []aht.Session{idleSessionAt(controller.panes[2], 2)})

	target, err := n.LatestIdle(t.Context(), agents.NavigationGlobal)
	if err != nil || target.PaneID != "%3" {
		t.Fatalf("target = %+v, error = %v", target, err)
	}

	controller.current = paneContext(target, "client")
	pairWrites := len(controller.toggleWrites)
	historyWrites := len(controller.historyWrites)

	if _, err := n.LatestIdle(t.Context(), agents.NavigationGlobal); err != nil {
		t.Fatal(err)
	}

	if len(controller.toggleWrites) != pairWrites || len(controller.historyWrites) != historyWrites {
		t.Fatal("already focused agent changed navigation state")
	}

	target, err = n.Toggle(t.Context(), agents.NavigationGlobal)
	if err != nil || target.PaneID != "%1" {
		t.Fatalf("working target = %+v, error = %v", target, err)
	}
}

func TestFocusLinkedAgentRecognizesOwnerAndKeepsInvokingSession(t *testing.T) {
	t.Parallel()

	panes := navigationPanes()
	alias := panes[1]
	alias.SessionID = "$1"
	alias.SessionName = firstSessionName
	controller := &fakeController{current: paneContext(panes[0], "client"), panes: append(panes, alias)}
	n := newFakeNavigator(t, controller, []aht.Session{idleSessionAt(panes[1], 1)})

	target, err := n.Focus(t.Context(), agents.FocusTarget{PaneID: "%2"})
	if err != nil || target.SessionID != "$1" {
		t.Fatalf("linked focus = %+v, error = %v", target, err)
	}

	controller.current = paneContext(target, "client")

	target, err = n.Toggle(t.Context(), agents.NavigationGlobal)
	if err != nil || target.PaneID != "%1" {
		t.Fatalf("linked return = %+v, error = %v", target, err)
	}

	controller.current = paneContext(target, "client")

	target, err = n.Toggle(t.Context(), agents.NavigationGlobal)
	if err != nil || target.PaneID != "%2" || target.SessionID != "$1" {
		t.Fatalf("linked agent = %+v, error = %v", target, err)
	}
}
