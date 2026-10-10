package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

func TestAttachCommandObservesModelLifecycleCancellation(t *testing.T) {
	var commandDone <-chan struct{}

	target := session.Session{
		ID:                     "",
		Name:                   testSupportSessionDevName,
		TmuxServerSocket:       "",
		Kind:                   session.KindLive,
		Origin:                 "",
		Generation:             "",
		RepositorySource:       "",
		NameRaw:                nil,
		PathRaw:                nil,
		RuntimeIdentity:        session.RuntimeIdentity{},
		RemoteStatus:           session.RemoteStatusAvailable,
		WindowCount:            0,
		Attached:               false,
		Activity:               time.Time{},
		Current:                false,
		ActivePaneID:           "",
		ActiveWindowName:       "",
		ActiveWindowIndex:      "",
		ActivePanePath:         "",
		ActivePaneCommand:      "",
		ActivePaneTitle:        "",
		AgentTotal:             0,
		AgentActive:            0,
		AgentWaiting:           0,
		AgentFailed:            0,
		AgentInterrupted:       0,
		AgentUnknown:           0,
		AgentIdleSince:         time.Time{},
		AgentWaitingSince:      time.Time{},
		AgentActivityChangedAt: time.Time{},
		Agents:                 nil,
	}

	model := newTestModelWithDeps(Dependencies{
		AttachSession: func(ctx context.Context, _ session.Session) (tea.ExecCommand, error) {
			commandDone = ctx.Done()

			return nil, nil //nolint:nilnil // A nil command explicitly requests application-owned attachment.
		},
	})

	_, cmd := model.handleSessionActionResult(sessionActionResultMsg{
		operation:      tuiOperationEnterLiveSession,
		deferred:       false,
		attach:         true,
		err:            nil,
		sessionName:    target.Name,
		target:         target,
		switchSession:  "",
		switchPane:     "",
		quit:           false,
		refresh:        false,
		statePersisted: false,
		agentView:      nil,
	})
	_ = cmd

	if commandDone == nil {
		t.Fatal("attach action was not called with model operation context")
	}

	model.Close()

	select {
	case <-commandDone:
	case <-time.After(time.Second):
		t.Fatal("attach command context did not observe model Close()")
	}
}

func TestSessionActionCommandDefersWorkflowUntilCommandRuns(t *testing.T) {
	model := newTestModel()
	called := false

	target := session.Session{
		ID:                     "",
		Name:                   testSupportSessionDevName,
		TmuxServerSocket:       "",
		Kind:                   session.KindLive,
		Origin:                 "",
		Generation:             "",
		RepositorySource:       "",
		NameRaw:                nil,
		PathRaw:                nil,
		RuntimeIdentity:        session.RuntimeIdentity{},
		RemoteStatus:           session.RemoteStatusAvailable,
		WindowCount:            0,
		Attached:               false,
		Activity:               time.Time{},
		Current:                false,
		ActivePaneID:           "",
		ActiveWindowName:       "",
		ActiveWindowIndex:      "",
		ActivePanePath:         "",
		ActivePaneCommand:      "",
		ActivePaneTitle:        "",
		AgentTotal:             0,
		AgentActive:            0,
		AgentWaiting:           0,
		AgentFailed:            0,
		AgentInterrupted:       0,
		AgentUnknown:           0,
		AgentIdleSince:         time.Time{},
		AgentWaitingSince:      time.Time{},
		AgentActivityChangedAt: time.Time{},
		Agents:                 nil,
	}

	command := model.sessionActionCommand(tuiOperationEnterLiveSession, target, func(context.Context) (EnterOutcome, error) {
		called = true

		return EnterOutcome{SessionName: testSupportSessionDevName, Action: EnterCompleted, Restored: false}, nil
	})

	if called {
		t.Fatal("sessionActionCommand() ran workflow on the update loop")
	}

	message := command()

	if !called {
		t.Fatal("session action command did not run workflow")
	}

	model.Update(message)

	if model.state.GetLastSwitch(session.Identity{Name: testSupportSessionDevName}) == 0 {
		t.Fatal("session action result did not record the dev session switch")
	}
}

type sessionActionOutcomeCase struct {
	name           string
	popup          bool
	outcome        EnterOutcome
	wantDeferred   bool
	wantAttach     bool
	wantSwitch     string
	wantStateSaved bool
}

func TestSessionActionCommandReportsDeferredAndAttachOutcomes(t *testing.T) {
	tests := []sessionActionOutcomeCase{
		{
			name:         "popup switch is deferred to the caller",
			popup:        true,
			outcome:      EnterOutcome{SessionName: testSupportSessionDevName, Action: EnterNeedsSwitch},
			wantDeferred: true,
			wantSwitch:   testSupportSessionDevName,
		},
		{
			name:       "standalone attach defers the terminal action",
			outcome:    EnterOutcome{SessionName: testSupportSessionDevName, Action: EnterNeedsAttach},
			wantAttach: true,
		},
		{
			name:           "completed entry records the switch",
			outcome:        EnterOutcome{SessionName: testSupportSessionDevName},
			wantStateSaved: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runSessionActionOutcomeCase(t, test)
		})
	}
}

func runSessionActionOutcomeCase(t *testing.T, test sessionActionOutcomeCase) {
	t.Helper()

	model := newTestModel()
	if test.popup {
		model.mode = modePopup
	}

	target := session.Session{
		ID:                     "",
		Name:                   testSupportSessionDevName,
		TmuxServerSocket:       "",
		Kind:                   session.KindLive,
		Origin:                 "",
		Generation:             "",
		RepositorySource:       "",
		NameRaw:                nil,
		PathRaw:                nil,
		RuntimeIdentity:        session.RuntimeIdentity{},
		RemoteStatus:           session.RemoteStatusAvailable,
		WindowCount:            0,
		Attached:               false,
		Activity:               time.Time{},
		Current:                false,
		ActivePaneID:           "",
		ActiveWindowName:       "",
		ActiveWindowIndex:      "",
		ActivePanePath:         "",
		ActivePaneCommand:      "",
		ActivePaneTitle:        "",
		AgentTotal:             0,
		AgentActive:            0,
		AgentWaiting:           0,
		AgentFailed:            0,
		AgentInterrupted:       0,
		AgentUnknown:           0,
		AgentIdleSince:         time.Time{},
		AgentWaitingSince:      time.Time{},
		AgentActivityChangedAt: time.Time{},
		Agents:                 nil,
	}

	message, ok := model.sessionActionCommand(
		tuiOperationEnterLiveSession,
		target,
		func(context.Context) (EnterOutcome, error) { return test.outcome, nil },
	)().(sessionActionResultMsg)
	if !ok {
		t.Fatalf("session action command returned %T, want sessionActionResultMsg", message)
	}

	if message.operation != tuiOperationEnterLiveSession {
		t.Fatalf("result operation = %q, want %q", message.operation, tuiOperationEnterLiveSession)
	}

	if message.deferred != test.wantDeferred || message.attach != test.wantAttach {
		t.Fatalf(
			"result deferred/attach = %v/%v, want %v/%v",
			message.deferred,
			message.attach,
			test.wantDeferred,
			test.wantAttach,
		)
	}

	if message.switchSession != test.wantSwitch {
		t.Fatalf("result switch session = %q, want %q", message.switchSession, test.wantSwitch)
	}

	if !message.quit {
		t.Fatal("result did not request the picker to quit")
	}

	if message.statePersisted != test.wantStateSaved {
		t.Fatalf("result state persisted = %v, want %v", message.statePersisted, test.wantStateSaved)
	}

	if message.err != nil {
		t.Fatalf("result error = %v, want nil", message.err)
	}
}

func TestSessionActionCommandReportsWorkflowFailureWithoutStateChange(t *testing.T) {
	model := newTestModel()
	target := session.Session{
		ID:                     "",
		Name:                   testSupportSessionDevName,
		TmuxServerSocket:       "",
		Kind:                   session.KindLive,
		Origin:                 "",
		Generation:             "",
		RepositorySource:       "",
		NameRaw:                nil,
		PathRaw:                nil,
		RuntimeIdentity:        session.RuntimeIdentity{},
		RemoteStatus:           session.RemoteStatusAvailable,
		WindowCount:            0,
		Attached:               false,
		Activity:               time.Time{},
		Current:                false,
		ActivePaneID:           "",
		ActiveWindowName:       "",
		ActiveWindowIndex:      "",
		ActivePanePath:         "",
		ActivePaneCommand:      "",
		ActivePaneTitle:        "",
		AgentTotal:             0,
		AgentActive:            0,
		AgentWaiting:           0,
		AgentFailed:            0,
		AgentInterrupted:       0,
		AgentUnknown:           0,
		AgentIdleSince:         time.Time{},
		AgentWaitingSince:      time.Time{},
		AgentActivityChangedAt: time.Time{},
		Agents:                 nil,
	}

	command := model.sessionActionCommand(tuiOperationEnterLiveSession, target, func(context.Context) (EnterOutcome, error) {
		return EnterOutcome{SessionName: testSupportSessionDevName, Action: EnterCompleted, Restored: false}, errRenameFailed
	})

	message, ok := command().(sessionActionResultMsg)
	if !ok {
		t.Fatalf("session action command returned %T, want sessionActionResultMsg", message)
	}

	if !errors.Is(message.err, errRenameFailed) {
		t.Fatalf("result error = %v, want %v", message.err, errRenameFailed)
	}

	if message.statePersisted || message.quit {
		t.Fatalf("failed result = %#v, want no state change and no quit", message)
	}

	if got := model.state.GetLastSwitch(session.Identity{Name: testSupportSessionDevName}); got != 0 {
		t.Fatalf("failed entry recorded switch at %d, want none", got)
	}
}
