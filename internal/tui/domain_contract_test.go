package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

var (
	errProtectedGeneration = errors.New("cannot delete protected generation")
	errTestMockError       = errors.New("domain mutation error")
)

func TestDeleteSessionFailsClosedWhenDispatcherMissing(t *testing.T) {
	killed := false
	deletedSaved := false

	target := session.Session{
		ID:               "$1",
		Name:             "work",
		Kind:             session.KindLive,
		Origin:           session.OriginLocal,
		TmuxServerSocket: "/tmp/tmux/test",
	}

	model := newTestModelWithDeps(Dependencies{
		OmitActions: true,
		TmuxKillSession: func(context.Context, string) error {
			killed = true
			return nil
		},
		DeleteSavedSession: func(context.Context, string) (int, error) {
			deletedSaved = true
			return 1, nil
		},
	})
	model.filtered = []session.Session{target}
	model.cursor = 0

	invokeTestAction(t, model, action.SessionDelete.ID)

	if killed {
		t.Fatal("kill was executed without an action dispatcher")
	}

	if deletedSaved {
		t.Fatal("saved deletion was executed without an action dispatcher")
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "no action dispatcher configured") {
		t.Fatalf("status = (%v, %q), want fail-closed unconfigured error", model.statusLevel, model.statusMessage)
	}
}

func TestDeleteSessionReportsDomainFailures(t *testing.T) {
	target := session.Session{
		ID:               "$1",
		Name:             "work",
		Kind:             session.KindLive,
		Origin:           session.OriginLocal,
		TmuxServerSocket: "/tmp/tmux/test",
	}

	for name, cause := range map[string]error{
		"protected preflight failure": errProtectedGeneration,
		"failure after partial work":  errTestMockError,
	} {
		t.Run(name, func(t *testing.T) {
			dispatched := 0
			model := newTestModelWithDeps(Dependencies{
				RunAction: func(_ context.Context, invocation action.Invocation) (any, error) {
					dispatched++

					input, ok := invocation.Input.(action.DeleteSession)
					if !ok || input.Session.ID != target.ID || input.Session.Name != target.Name {
						t.Fatalf("received input = %+v, want target %+v", invocation.Input, target)
					}

					return action.DeletedSession{Name: target.Name, LiveKilled: errors.Is(cause, errTestMockError)}, action.Wrap(action.CodeFailed, cause)
				},
			})
			model.filtered = []session.Session{target}
			model.cursor = 0
			setActionConfirm(model.config, action.SessionDelete.ID, false)

			invokeTestAction(t, model, action.SessionDelete.ID)

			if dispatched != 1 {
				t.Fatalf("dispatches = %d, want exactly 1", dispatched)
			}

			if model.statusLevel != statusError || !strings.Contains(model.statusMessage, cause.Error()) {
				t.Fatalf("status = (%v, %q), want %q", model.statusLevel, model.statusMessage, cause)
			}
		})
	}
}

func TestDeleteSessionDispatchesExactlyOnce(t *testing.T) {
	target := session.Session{
		ID:               "$99",
		Name:             "project-alpha",
		Kind:             session.KindLive,
		Origin:           session.OriginLocal,
		TmuxServerSocket: "/tmp/tmux/explicit",
	}

	var (
		dispatches int
		received   action.DeleteSession
	)

	model := newTestModelWithDeps(Dependencies{
		RunAction: func(_ context.Context, invocation action.Invocation) (any, error) {
			dispatches++
			received, _ = invocation.Input.(action.DeleteSession)

			return action.DeletedSession{Name: target.Name, LiveKilled: true, SavedDeleted: true}, nil
		},
	})
	model.filtered = []session.Session{target}
	model.cursor = 0
	setActionConfirm(model.config, action.SessionDelete.ID, true)

	invokeTestAction(t, model, action.SessionDelete.ID)

	if model.dialog != dialogConfirm {
		t.Fatalf("dialog = %v, want confirmation", model.dialog)
	}

	if dispatches != 0 {
		t.Fatalf("dispatches before confirmation = %d, want 0", dispatches)
	}

	model.confirmYes = true
	pressTestKey(t, model, keyCode(tea.KeyEnter))

	if dispatches != 1 {
		t.Fatalf("dispatches after confirmation = %d, want exactly 1", dispatches)
	}

	if received.Session.ID != target.ID || received.Session.Socket != target.TmuxServerSocket {
		t.Fatalf("dispatched target = %+v, want %+v", received.Session, target)
	}
}

func TestStandaloneLiveEnterDefersTerminalAttachToApplication(t *testing.T) {
	target := session.Session{
		ID:               "$42",
		Name:             "interactive-work",
		Kind:             session.KindLive,
		Origin:           session.OriginLocal,
		TmuxServerSocket: "/tmp/tmux/local",
	}

	model := newTestModelWithDeps(Dependencies{
		OmitTerminalAttach: true,
		TmuxIsInsideTmux:   func() bool { return false },
		EnterSession: func(_ context.Context, s session.Session) (EnterOutcome, error) {
			return EnterOutcome{SessionName: s.Name, Action: EnterNeedsAttach, Restored: false}, nil
		},
	})
	model.mode = modeFullscreen
	model.filtered = []session.Session{target}
	model.cursor = 0

	handler := model.handleEnter()
	if handler == nil {
		t.Fatal("handleEnter() returned nil")
	}

	_, cmd := handler()
	if cmd == nil {
		t.Fatal("handleEnter() returned nil command")
	}

	msg := cmd()
	nextModel, quitCmd := model.Update(msg)

	if quitCmd == nil {
		t.Fatal("Update(msg) returned nil quit command")
	}

	res, ok := nextModel.(*Model)
	if !ok {
		t.Fatalf("nextModel = %T, want *Model", nextModel)
	}

	deferredTarget, ok := res.DeferredAttachTarget()
	if !ok {
		t.Fatal("DeferredAttachTarget() reported false, want true")
	}

	if deferredTarget.ID != target.ID || deferredTarget.Name != target.Name {
		t.Fatalf("deferred target = %+v, want %+v", deferredTarget, target)
	}

	if res.DeferredAttachSession() != target.Name {
		t.Fatalf("DeferredAttachSession() = %q, want %q", res.DeferredAttachSession(), target.Name)
	}
}

type mockExecCommand struct {
	ran bool
}

func (m *mockExecCommand) Run() error {
	m.ran = true
	return nil
}

func (m *mockExecCommand) SetStdin(io.Reader)  {}
func (m *mockExecCommand) SetStdout(io.Writer) {}
func (m *mockExecCommand) SetStderr(io.Writer) {}
func TestStandaloneLiveEnterExecutesInjectableTerminalAttach(t *testing.T) {
	target := session.Session{
		ID:               "$42",
		Name:             "interactive-work",
		Kind:             session.KindLive,
		Origin:           session.OriginLocal,
		TmuxServerSocket: "/tmp/tmux/local",
	}

	mockCmd := &mockExecCommand{ran: false}
	attachCalled := false

	model := newTestModelWithDeps(Dependencies{
		TmuxIsInsideTmux: func() bool { return false },
		EnterSession: func(_ context.Context, s session.Session) (EnterOutcome, error) {
			return EnterOutcome{SessionName: s.Name, Action: EnterNeedsAttach, Restored: false}, nil
		},
		AttachSession: func(_ context.Context, s session.Session) (tea.ExecCommand, error) {
			attachCalled = true

			if s.ID != target.ID {
				t.Fatalf("attach target ID = %q, want %q", s.ID, target.ID)
			}

			return mockCmd, nil
		},
	})
	model.mode = modeFullscreen
	model.filtered = []session.Session{target}
	model.cursor = 0

	handler := model.handleEnter()
	if handler == nil {
		t.Fatal("handleEnter() returned nil")
	}

	_, cmd := handler()
	if cmd == nil {
		t.Fatal("handleEnter() returned nil command")
	}

	msg := cmd()
	_, execCmd := model.Update(msg)

	if !attachCalled {
		t.Fatal("AttachSession callback was not called")
	}

	if execCmd == nil {
		t.Fatal("tea.Exec command was not returned")
	}
}

func TestModelCloseBlocksUntilWorkersJoinAndRejectsPostCloseState(t *testing.T) {
	var (
		workerStarted atomic.Bool
		workerJoined  atomic.Bool
	)

	model := newTestModelWithDeps(Dependencies{
		WatchAgentSummaries: func(ctx context.Context, _ func(uint64, []session.AgentSummary, error) error) error {
			workerStarted.Store(true)
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond)
			workerJoined.Store(true)

			return ctx.Err()
		},
	})

	cmd := model.startAgentSummaryWatchCommand()
	if cmd == nil {
		t.Fatal("startAgentSummaryWatchCommand returned nil")
	}

	go func() {
		_ = cmd()
	}()

	deadline := time.After(2 * time.Second)

	for !workerStarted.Load() {
		select {
		case <-deadline:
			t.Fatal("background worker did not start in time")
		case <-time.After(10 * time.Millisecond):
		}
	}

	model.Close()

	if !workerJoined.Load() {
		t.Fatal("Close() returned before in-flight background worker joined")
	}

	err := model.updateState(context.Background(), func(s *session.State) {
		s.PopupDisplayMode = "invalid-post-close"
	})
	if err == nil {
		t.Fatal("updateState() succeeded after Close(), want rejection")
	}

	if model.state.PopupDisplayMode == "invalid-post-close" {
		t.Fatal("model state was modified after Close()")
	}
}

func TestModelLifecyclePreservesConfiguredLogger(t *testing.T) {
	type parentContextKey string

	const testKey parentContextKey = "secret_parent_key"

	var logBuffer bytes.Buffer

	logger := zerolog.New(&logBuffer).With().Str("component", "tui_test").Logger()

	parentCtx := context.WithValue(context.Background(), testKey, "secret_value")
	parentCtx = logger.WithContext(parentCtx)

	model, err := NewModel(parentCtx, config.DefaultConfig(), testState(), testServices(Dependencies{}))
	if err != nil {
		t.Fatalf("NewModel() error = %v", err)
	}
	defer model.Close()

	opCtx := model.operationContext()

	if got := opCtx.Value(testKey); got != nil {
		t.Fatalf("opCtx retained parent value %v, want nil", got)
	}

	opLogger := zerolog.Ctx(opCtx)
	opLogger.Info().Msg("testing_logger_retention")

	if !strings.Contains(logBuffer.String(), "testing_logger_retention") {
		t.Fatalf("log output = %q, want logger retention", logBuffer.String())
	}

	if !strings.Contains(logBuffer.String(), `"component":"tui_test"`) {
		t.Fatalf("log output = %q, want configured logger field", logBuffer.String())
	}
}

func TestActionsPreserveImmutableTargetIdentityDuringConcurrentRefresh(t *testing.T) {
	targetA := session.Session{
		ID:               "$1",
		Name:             "shared-name",
		Kind:             session.KindLive,
		Origin:           session.OriginLocal,
		TmuxServerSocket: "/tmp/tmux/socket-A",
	}

	targetB := session.Session{
		ID:               "$2",
		Name:             "shared-name",
		Kind:             session.KindLive,
		Origin:           session.OriginLocal,
		TmuxServerSocket: "/tmp/tmux/socket-B",
	}

	actionDispatched := make(chan struct{})
	refreshCompleted := make(chan struct{})

	var receivedTarget session.Session

	model := newTestModelWithDeps(Dependencies{
		EnterSession: func(_ context.Context, s session.Session) (EnterOutcome, error) {
			receivedTarget = s

			close(actionDispatched)

			<-refreshCompleted

			return EnterOutcome{SessionName: s.Name, Action: EnterCompleted, Restored: false}, nil
		},
	})
	model.mode = modeInline
	model.sessions = []session.Session{targetA}
	model.filtered = []session.Session{targetA}
	model.cursor = 0

	handler := model.handleEnter()
	if handler == nil {
		t.Fatal("handleEnter() returned nil")
	}

	_, cmd := handler()
	if cmd == nil {
		t.Fatal("handleEnter() returned nil command")
	}

	msgChan := make(chan tea.Msg)
	go func() {
		msgChan <- cmd()
	}()

	select {
	case <-actionDispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("action command did not start in time")
	}

	model.sessions = []session.Session{targetB}
	model.filtered = []session.Session{targetB}
	model.cursor = 0

	close(refreshCompleted)

	var msg tea.Msg
	select {
	case msg = <-msgChan:
	case <-time.After(2 * time.Second):
		t.Fatal("action command did not complete")
	}

	resultMsg, ok := msg.(sessionActionResultMsg)
	if !ok {
		t.Fatalf("msg = %T, want sessionActionResultMsg", msg)
	}

	if receivedTarget.ID != targetA.ID || receivedTarget.TmuxServerSocket != targetA.TmuxServerSocket {
		t.Fatalf("action received target = %+v, want targetA %+v", receivedTarget, targetA)
	}

	if resultMsg.target.ID != targetA.ID {
		t.Fatalf("resultMsg.target.ID = %q, want %q", resultMsg.target.ID, targetA.ID)
	}
}

func TestRemoteKillDispatchesToPeer(t *testing.T) {
	testRemoteActionDispatch(t, action.SessionKill.ID)
}

func TestRemoteDeleteDispatchesToPeer(t *testing.T) {
	testRemoteActionDispatch(t, action.SessionDelete.ID)
}

func testRemoteActionDispatch(t *testing.T, id action.ID) {
	t.Helper()

	target := session.Session{
		ID:              "remote-$1",
		Name:            "remote-sess",
		Kind:            session.KindLive,
		Origin:          session.OriginSSH,
		RuntimeIdentity: session.RuntimeIdentity{ConnectionID: "peer-a"},
	}

	var dispatched action.Invocation

	model := newTestModelWithDeps(Dependencies{
		RunAction: func(_ context.Context, invocation action.Invocation) (any, error) {
			dispatched = invocation

			switch invocation.Input.(type) {
			case action.KillSession:
				return action.KilledSession{Session: action.RefOf(target)}, nil
			default:
				return action.DeletedSession{Name: target.Name, LiveKilled: true}, nil
			}
		},
	})
	model.filtered = []session.Session{target}
	model.cursor = 0
	setActionConfirm(model.config, id, false)

	trace := invokeTestAction(t, model, id)
	if len(trace.results) != 1 || trace.results[0].err != nil {
		t.Fatalf("results = %+v, want one successful remote %s", trace.results, id)
	}

	if dispatched.Action != id || dispatched.At != action.Peer("peer-a") {
		t.Fatalf("dispatched %s at %+v, want %s at the peer", dispatched.Action, dispatched.At, id)
	}
}
