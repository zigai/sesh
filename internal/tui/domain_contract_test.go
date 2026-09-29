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

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

var (
	errProtectedGeneration = errors.New("cannot delete protected generation")
	errTestMockError       = errors.New("domain mutation error")
)

func TestDeleteSessionFailsClosedWhenHandlerMissing(t *testing.T) {
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
		OmitDeleter: true,
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

	handler := model.handleDelete()
	if handler == nil {
		t.Fatal("handleDelete() returned nil")
	}

	handler()

	if killed {
		t.Fatal("kill was executed when session deleter was unconfigured")
	}

	if deletedSaved {
		t.Fatal("saved deletion was executed when session deleter was unconfigured")
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "no session deleter configured") {
		t.Fatalf("status = (%v, %q), want fail-closed unconfigured error", model.statusLevel, model.statusMessage)
	}
}

func TestDeleteSessionPreservesDomainPartialReceipt(t *testing.T) {
	target := session.Session{
		ID:               "$1",
		Name:             "work",
		Kind:             session.KindLive,
		Origin:           session.OriginLocal,
		TmuxServerSocket: "/tmp/tmux/test",
	}

	t.Run("protected preflight failure causes no kill", func(t *testing.T) {
		dispatched := 0
		model := newTestModelWithDeps(Dependencies{
			DeleteSessionEverywhere: func(_ context.Context, s session.Session, _ bool) (DeleteResult, error) {
				dispatched++

				if s.ID != target.ID || s.Name != target.Name {
					t.Fatalf("received session = %+v, want target %+v", s, target)
				}

				return DeleteResult{LiveKilled: false, SavedDeleted: false}, errProtectedGeneration
			},
		})
		model.filtered = []session.Session{target}
		model.cursor = 0
		model.config.Settings.ConfirmDelete = false

		handler := model.handleDelete()
		if handler == nil {
			t.Fatal("handleDelete() returned nil")
		}

		handler()

		if dispatched != 1 {
			t.Fatalf("dispatches = %d, want exactly 1", dispatched)
		}

		if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "cannot delete protected generation") {
			t.Fatalf("status = (%v, %q), want protected generation error", model.statusLevel, model.statusMessage)
		}
	})

	t.Run("partial receipt preserved on domain failure", func(t *testing.T) {
		dispatched := 0
		model := newTestModelWithDeps(Dependencies{
			DeleteSessionEverywhere: func(_ context.Context, _ session.Session, _ bool) (DeleteResult, error) {
				dispatched++
				return DeleteResult{LiveKilled: true, SavedDeleted: false}, errTestMockError
			},
		})
		model.filtered = []session.Session{target}
		model.cursor = 0
		model.config.Settings.ConfirmDelete = false

		handler := model.handleDelete()
		if handler == nil {
			t.Fatal("handleDelete() returned nil")
		}

		handler()

		if dispatched != 1 {
			t.Fatalf("dispatches = %d, want exactly 1", dispatched)
		}

		if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "domain mutation error") {
			t.Fatalf("status = (%v, %q), want domain mutation error", model.statusLevel, model.statusMessage)
		}
	})
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
		dispatches     int
		receivedTarget session.Session
		receivedForce  bool
	)

	model := newTestModelWithDeps(Dependencies{
		DeleteSessionEverywhere: func(_ context.Context, s session.Session, force bool) (DeleteResult, error) {
			dispatches++
			receivedTarget = s
			receivedForce = force

			return DeleteResult{LiveKilled: true, SavedDeleted: true}, nil
		},
	})
	model.filtered = []session.Session{target}
	model.cursor = 0
	model.config.Settings.ConfirmDelete = true

	handler := model.handleDelete()
	if handler == nil {
		t.Fatal("handleDelete() returned nil")
	}

	handler()

	if model.dialog != dialogConfirmDelete {
		t.Fatalf("dialog = %v, want dialogConfirmDelete", model.dialog)
	}

	if dispatches != 0 {
		t.Fatalf("dispatches before confirmation = %d, want 0", dispatches)
	}

	model.confirmYes = true

	confirmHandler := model.handleConfirmActionKeys(keyText("enter"), "Delete failed", tuiOperationDeleteSession)
	if confirmHandler == nil {
		t.Fatal("handleConfirmActionKeys returned nil")
	}

	confirmHandler()

	if dispatches != 1 {
		t.Fatalf("dispatches after confirmation = %d, want exactly 1", dispatches)
	}

	if receivedTarget.ID != target.ID || receivedTarget.TmuxServerSocket != target.TmuxServerSocket {
		t.Fatalf("dispatched target = %+v, want %+v", receivedTarget, target)
	}

	if !receivedForce {
		t.Fatal("dispatched force = false, want true after explicit user confirmation")
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

func TestRemoteKillDispatchesToRemoteActionService(t *testing.T) {
	testRemoteActionDispatch(t, "kill")
}

func TestRemoteDeleteDispatchesToRemoteActionService(t *testing.T) {
	testRemoteActionDispatch(t, "delete")
}

func testRemoteActionDispatch(t *testing.T, action string) {
	t.Helper()

	target := session.Session{
		ID:     "remote-$1",
		Name:   "remote-sess",
		Kind:   session.KindLive,
		Origin: session.OriginSSH,
	}

	var (
		actionDispatched string
		targetReceived   session.Session
	)

	model := newTestModelWithDeps(Dependencies{
		RunRemoteAction: func(_ context.Context, _ *config.Config, received session.Session, request RemoteActionRequest) error {
			actionDispatched = request.Action
			targetReceived = received

			return nil
		},
		IsRemoteActionPermitted: func(_ *config.Config, _ session.Session, _ string) bool {
			return true
		},
	})
	model.filtered = []session.Session{target}
	model.cursor = 0
	model.config.Settings.ConfirmKill = false
	model.config.Settings.ConfirmDelete = false

	var handler func() (tea.Model, tea.Cmd)

	switch action {
	case "kill":
		handler = model.handleKill()
	case "delete":
		handler = model.handleDelete()
	default:
		t.Fatalf("unsupported test action %q", action)
	}

	if handler == nil {
		t.Fatalf("handle %s returned nil", action)
	}

	_, cmd := handler()
	if cmd == nil {
		t.Fatalf("handle %s returned nil command", action)
	}

	rawMsg := cmd()

	msg, ok := rawMsg.(sessionActionResultMsg)
	if !ok || msg.err != nil {
		t.Fatalf("msg = %#v, want successful remote %s", rawMsg, action)
	}

	if actionDispatched != action || targetReceived.ID != target.ID {
		t.Fatalf("action = %q target = %+v, want %s on target", actionDispatched, targetReceived, action)
	}
}
