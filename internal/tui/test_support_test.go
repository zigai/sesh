package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/testsupport/testenv"
)

func TestMain(m *testing.M) {
	os.Exit(testenv.RunIsolated(m))
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

const (
	testSupportSessionDevName     = "dev"
	testSupportSessionWorkName    = "work"
	testSupportSessionProjectName = "project"
	testSupportSessionArchiveName = "archive"
	testSupportSessionAlphaName   = "alpha"
	testSupportHarnessCodex       = "codex"
	testSupportAgentRegistryID    = "agent-1"
	testSupportNewSessionName     = "new-session"
)

var (
	errTmuxUnavailable        = errors.New("tmux unavailable")
	errRenameFailed           = errors.New("rename failed")
	errSaveFailed             = errors.New("save failed")
	errWorkingDirectoryFailed = errors.New("cwd failed")
	errSnapshotFailed         = errors.New("snapshot failed")
	errUnexpectedTestAction   = errors.New("unexpected test action")
)

type Dependencies struct {
	OmitEnterer      bool
	OmitActions      bool
	OmitPanes        bool
	OmitRemoteAttach bool
	OmitOriginBridge bool
	DeferSwitch      bool
	DeferAttach      bool

	LoadCatalog            func(context.Context) (session.CatalogResult, error)
	EnterSession           func(context.Context, session.Session) (EnterOutcome, error)
	RunAction              func(context.Context, action.Invocation) (any, error)
	AcceptsAction          func(action.Location, action.ID) bool
	TmuxActivatePaneTarget func(context.Context, session.Session, string) error

	TmuxSessionExists     func(context.Context, string) (bool, error)
	TmuxCreateSession     func(context.Context, string, string) error
	TmuxSwitchSession     func(context.Context, string) error
	TmuxActivatePane      func(context.Context, string, string) error
	TmuxRenameSession     func(context.Context, string, string) error
	TmuxKillSession       func(context.Context, string) error
	TmuxIsInsideTmux      func() bool
	LoadAgentSummaries    func(context.Context) ([]session.AgentSummary, error)
	WatchAgentSummaries   func(context.Context, func(uint64, []session.AgentSummary, error) error) error
	TmuxPaneLocations     func(context.Context) ([]PaneLocation, error)
	LoadRemoteCatalog     func() (RemoteCatalog, error)
	WatchRemoteCatalog    func(context.Context, func(RemoteCatalog, error)) error
	AttachRemoteSession   func(context.Context, session.Session, string) (tea.ExecCommand, error)
	LoadOriginSessions    func(context.Context) (string, []session.Session, error)
	RequestOriginSwitch   func(context.Context, session.Session) error
	DeleteSavedSession    func(context.Context, string) (int, error)
	RestoreSavedSession   func(context.Context, string) error
	SnapshotSession       func(context.Context, string) error
	GetWorkingDirectory   func() (string, error)
	SaveModelState        func(*session.State) error
	UpdateModelStateBlock func()
}

type testServiceAdapter struct {
	deps  *Dependencies
	model *Model
}

func (adapter *testServiceAdapter) isPopup() bool {
	if adapter.deps.DeferSwitch {
		return true
	}

	if adapter.model != nil && adapter.model.mode == modePopup {
		return true
	}

	return os.Getenv("TMUX_POPUP") != ""
}

func (adapter *testServiceAdapter) LoadCatalog(ctx context.Context) (session.CatalogResult, error) {
	return adapter.deps.LoadCatalog(ctx)
}

func (adapter *testServiceAdapter) TmuxIsInsideTmux() bool {
	return adapter.deps.TmuxIsInsideTmux()
}

func (adapter *testServiceAdapter) EnterSession(ctx context.Context, target session.Session) (EnterOutcome, error) {
	if adapter.deps.EnterSession != nil {
		return adapter.deps.EnterSession(ctx, target)
	}

	if adapter.deps.TmuxIsInsideTmux != nil && !adapter.deps.TmuxIsInsideTmux() {
		return EnterOutcome{SessionName: target.Name, Action: EnterNeedsAttach, Restored: false}, nil
	}

	if adapter.isPopup() {
		return EnterOutcome{SessionName: target.Name, Action: EnterNeedsSwitch, Restored: false}, nil
	}

	if adapter.deps.TmuxSwitchSession != nil {
		err := adapter.deps.TmuxSwitchSession(ctx, target.Name)

		return EnterOutcome{SessionName: target.Name, Action: EnterCompleted, Restored: false}, err
	}

	return EnterOutcome{SessionName: target.Name, Action: EnterCompleted, Restored: false}, nil
}

func (adapter *testServiceAdapter) Accepts(at action.Location, id action.ID) bool {
	if adapter.deps.AcceptsAction != nil {
		return adapter.deps.AcceptsAction(at, id)
	}

	if at.Kind == action.LocationOrigin {
		return id == action.SessionSwitch.ID && (adapter.deps.RequestOriginSwitch != nil || adapter.deps.LoadOriginSessions != nil)
	}

	return true
}

func (adapter *testServiceAdapter) Dispatch(ctx context.Context, invocation action.Invocation) (any, error) {
	if adapter.deps.RunAction != nil {
		return adapter.deps.RunAction(ctx, invocation)
	}

	output, err := adapter.dispatchFake(ctx, invocation)
	if err != nil {
		return output, action.Wrap(testActionCode(err), err)
	}

	return output, nil
}

func (adapter *testServiceAdapter) dispatchFake(ctx context.Context, invocation action.Invocation) (any, error) {
	switch input := invocation.Input.(type) {
	case action.CreateSession:
		return adapter.fakeCreate(ctx, input)
	case action.RenameSession:
		return adapter.fakeRename(ctx, input)
	case action.KillSession:
		return action.KilledSession{Session: input.Session}, adapter.fakeKill(ctx, input.Session.Name)
	case action.DeleteSession:
		return adapter.fakeDelete(ctx, input)
	case action.SaveSnapshot:
		return adapter.fakeSave(ctx, input)
	case action.RestoreSnapshot:
		return adapter.fakeRestore(ctx, invocation, input)
	case action.SwitchSession:
		if adapter.deps.RequestOriginSwitch == nil {
			return action.SwitchedSession(input), nil
		}

		return action.SwitchedSession(input), adapter.deps.RequestOriginSwitch(ctx, input.Session.Session(invocation.At))
	default:
		return nil, errUnexpectedTestAction
	}
}

func (adapter *testServiceAdapter) fakeCreate(ctx context.Context, input action.CreateSession) (action.CreatedSession, error) {
	created := action.CreatedSession{Session: testLiveRef("$created", input.Name), Directory: input.Directory}

	if adapter.deps.TmuxSessionExists != nil {
		exists, err := adapter.deps.TmuxSessionExists(ctx, input.Name)
		if err != nil {
			return created, err
		}

		if exists {
			return created, session.ErrSessionAlreadyExists
		}
	}

	if adapter.deps.TmuxCreateSession != nil {
		return created, adapter.deps.TmuxCreateSession(ctx, input.Name, input.Directory)
	}

	return created, nil
}

func (adapter *testServiceAdapter) fakeRename(ctx context.Context, input action.RenameSession) (action.RenamedSession, error) {
	renamed := action.RenamedSession{PreviousName: input.Session.Name, Name: input.Name, HistoryUpdated: false}
	if adapter.deps.TmuxRenameSession != nil {
		return renamed, adapter.deps.TmuxRenameSession(ctx, input.Session.Name, input.Name)
	}

	return renamed, nil
}

func (adapter *testServiceAdapter) fakeKill(ctx context.Context, name string) error {
	if adapter.deps.TmuxKillSession != nil {
		return adapter.deps.TmuxKillSession(ctx, name)
	}

	return nil
}

func (adapter *testServiceAdapter) fakeDelete(ctx context.Context, input action.DeleteSession) (action.DeletedSession, error) {
	deleted := action.DeletedSession{
		Name: input.Session.Name, LiveKilled: false, SavedDeleted: false, SavedGenerations: 0, SavedSessions: 0,
		PreservedGenerations: []string{}, PendingRewrites: []string{},
	}

	if !input.Session.Saved() {
		if err := adapter.fakeKill(ctx, input.Session.Name); err != nil {
			return deleted, err
		}

		deleted.LiveKilled = true
	}

	if adapter.deps.DeleteSavedSession != nil {
		count, err := adapter.deps.DeleteSavedSession(ctx, input.Session.Name)
		if err != nil {
			return deleted, err
		}

		deleted.SavedDeleted, deleted.SavedGenerations = count > 0, count
	}

	return deleted, nil
}

func (adapter *testServiceAdapter) fakeSave(ctx context.Context, input action.SaveSnapshot) (action.SavedSnapshot, error) {
	saved := action.SavedSnapshot{Attempt: "attempt", Generation: "generation", Status: "complete"}

	for _, ref := range input.Sessions {
		if adapter.deps.SnapshotSession != nil {
			if err := adapter.deps.SnapshotSession(ctx, ref.Name); err != nil {
				return saved, err
			}
		}
	}

	return saved, nil
}

func (adapter *testServiceAdapter) fakeRestore(ctx context.Context, invocation action.Invocation, input action.RestoreSnapshot) (action.RestoredSnapshot, error) {
	restored := action.RestoredSnapshot{
		OperationID: "operation", Generation: input.Generation, Status: "complete", Reason: "", Sessions: []action.SessionRef{},
		Panes: []action.RestoredPane{}, Verified: true, Uncertain: false, MissingRequirements: []action.Requirement{},
		SessionsSkipped: []action.SkippedSession{}, Advisories: []action.Mismatch{}, Mismatches: []action.Mismatch{},
	}

	for _, name := range adapter.restoredNames(invocation, input) {
		if adapter.deps.TmuxSessionExists != nil {
			exists, err := adapter.deps.TmuxSessionExists(ctx, name)
			if err != nil {
				return restored, err
			}

			if exists {
				return restored, session.ErrSessionAlreadyExists
			}
		}

		if adapter.deps.RestoreSavedSession != nil {
			if err := adapter.deps.RestoreSavedSession(ctx, name); err != nil {
				return restored, err
			}
		}

		restored.Sessions = append(restored.Sessions, testLiveRef("$restored", name))
	}

	return restored, nil
}

func (adapter *testServiceAdapter) restoredNames(invocation action.Invocation, input action.RestoreSnapshot) []string {
	names := make([]string, 0, len(input.Sessions))

	for _, id := range input.Sessions {
		name := id

		if adapter.model != nil {
			for _, row := range slices.Concat(adapter.model.sessions, adapter.model.filtered) {
				if row.Saved() && row.ID == id && action.LocationOf(row) == invocation.At {
					name = row.Name
				}
			}
		}

		names = append(names, name)
	}

	return names
}

func testLiveRef(id, name string) action.SessionRef {
	return action.SessionRef{
		Kind: session.KindLive, ID: id, Name: name, Socket: "", ServerPID: 0,
		ServerStartedAt: time.Time{}, CreatedAt: time.Time{}, Generation: "",
	}
}

func testActionCode(err error) action.Code {
	switch {
	case errors.Is(err, session.ErrSessionAlreadyExists):
		return action.CodeAlreadyExists
	case errors.Is(err, session.ErrSessionNotFound):
		return action.CodeNotFound
	default:
		return action.CodeFailed
	}
}

func (adapter *testServiceAdapter) TmuxActivatePane(ctx context.Context, target session.Session, paneID string) error {
	if adapter.deps.TmuxActivatePaneTarget != nil {
		return adapter.deps.TmuxActivatePaneTarget(ctx, target, paneID)
	}

	if adapter.deps.TmuxActivatePane != nil {
		return adapter.deps.TmuxActivatePane(ctx, target.Name, paneID)
	}

	return nil
}

func (adapter *testServiceAdapter) TmuxPaneLocations(ctx context.Context) ([]PaneLocation, error) {
	return adapter.deps.TmuxPaneLocations(ctx)
}

func (adapter *testServiceAdapter) LoadAgentSummaries(ctx context.Context) (AgentSnapshot, error) {
	summaries, err := adapter.deps.LoadAgentSummaries(ctx)
	if err != nil {
		return AgentSnapshot{Summaries: nil, Panes: nil, PanesObserved: false}, err
	}

	return adapter.agentSnapshot(ctx, summaries), nil
}

func (adapter *testServiceAdapter) WatchAgentSummaries(
	ctx context.Context,
	yield func(uint64, AgentSnapshot, error) error,
) error {
	return adapter.deps.WatchAgentSummaries(ctx, func(revision uint64, summaries []session.AgentSummary, err error) error {
		if err != nil {
			return yield(revision, AgentSnapshot{Summaries: summaries, Panes: nil, PanesObserved: false}, err)
		}

		return yield(revision, adapter.agentSnapshot(ctx, summaries), nil)
	})
}

func (adapter *testServiceAdapter) agentSnapshot(ctx context.Context, summaries []session.AgentSummary) AgentSnapshot {
	snapshot := AgentSnapshot{Summaries: summaries, Panes: nil, PanesObserved: false}
	if len(summaries) == 0 || adapter.deps.OmitPanes {
		return snapshot
	}

	panes, err := adapter.deps.TmuxPaneLocations(ctx)
	snapshot.Panes = panes
	snapshot.PanesObserved = err == nil

	return snapshot
}

func (adapter *testServiceAdapter) LoadRemoteCatalog() (RemoteCatalog, error) {
	return adapter.deps.LoadRemoteCatalog()
}

func (adapter *testServiceAdapter) WatchRemoteCatalog(
	ctx context.Context,
	yield func(RemoteCatalog, error),
) error {
	return adapter.deps.WatchRemoteCatalog(ctx, yield)
}

func (adapter *testServiceAdapter) AttachRemoteSession(ctx context.Context, target session.Session, paneID string) (tea.ExecCommand, error) {
	return adapter.deps.AttachRemoteSession(ctx, target, paneID)
}

func (adapter *testServiceAdapter) LoadOriginSessions(ctx context.Context) (string, []session.Session, error) {
	if adapter.deps.LoadOriginSessions != nil {
		return adapter.deps.LoadOriginSessions(ctx)
	}

	return "", nil, nil
}

func (adapter *testServiceAdapter) GetWorkingDirectory() (string, error) {
	return adapter.deps.GetWorkingDirectory()
}

func (adapter *testServiceAdapter) DeferAgentViews([]AgentView) bool {
	return false
}

func (adapter *testServiceAdapter) UpdateModelState(
	ctx context.Context,
	update session.StateUpdate,
) error {
	if adapter.deps.UpdateModelStateBlock != nil {
		adapter.deps.UpdateModelStateBlock()
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("update model state: %w", err)
	}

	st := &session.State{}
	update(st)

	return adapter.deps.SaveModelState(st)
}

func testServices(deps Dependencies) Services {
	setDefaultTmuxDependencies(&deps)
	setDefaultInfrastructureDependencies(&deps)

	adapter := &testServiceAdapter{deps: &deps}
	services := defaultTestServices(adapter)

	omitTestServices(&services, deps)
	addOptionalTestServices(&services, deps, adapter)

	return services
}

func defaultTestServices(adapter *testServiceAdapter) Services {
	return Services{
		Catalog:          adapter,
		Detector:         adapter,
		Enterer:          adapter,
		Actions:          adapter,
		Panes:            adapter,
		Agents:           adapter,
		Tasks:            nil,
		AgentWatcher:     nil,
		Remote:           adapter,
		RemoteWatcher:    nil,
		RemoteAttach:     nil,
		OriginBridge:     nil,
		WorkingDirectory: adapter,
		State:            adapter,
	}
}

func omitTestServices(services *Services, deps Dependencies) {
	if deps.OmitEnterer {
		services.Enterer = nil
	}

	if deps.OmitActions {
		services.Actions = nil
	}

	if deps.OmitPanes {
		services.Panes = nil
	}
}

func addOptionalTestServices(services *Services, deps Dependencies, adapter *testServiceAdapter) {
	if deps.WatchAgentSummaries != nil {
		services.AgentWatcher = adapter
	}

	if deps.WatchRemoteCatalog != nil {
		services.RemoteWatcher = adapter
	}

	if deps.AttachRemoteSession != nil {
		services.RemoteAttach = adapter
	}

	if deps.RequestOriginSwitch != nil || deps.LoadOriginSessions != nil {
		services.OriginBridge = adapter
	}
}

func setDefaultTmuxDependencies(deps *Dependencies) {
	if deps.LoadCatalog == nil {
		deps.LoadCatalog = func(context.Context) (session.CatalogResult, error) {
			return session.CatalogResult{
				Sessions: []session.Session{},
				Status:   session.StatusComplete,
				LiveErr:  nil,
				SavedErr: nil,
			}, nil
		}
	}

	if deps.TmuxIsInsideTmux == nil {
		deps.TmuxIsInsideTmux = func() bool { return true }
	}

	if deps.TmuxPaneLocations == nil {
		deps.TmuxPaneLocations = func(context.Context) ([]PaneLocation, error) { return nil, errTmuxUnavailable }
	}

	if deps.LoadAgentSummaries == nil {
		deps.LoadAgentSummaries = func(context.Context) ([]session.AgentSummary, error) { return nil, nil }
	}
}

func setDefaultInfrastructureDependencies(deps *Dependencies) {
	if deps.GetWorkingDirectory == nil {
		deps.GetWorkingDirectory = func() (string, error) { return "", nil }
	}

	if deps.SaveModelState == nil {
		deps.SaveModelState = func(*session.State) error { return nil }
	}

	if deps.LoadRemoteCatalog == nil {
		deps.LoadRemoteCatalog = func() (RemoteCatalog, error) {
			return RemoteCatalog{
				GeneratedAt: time.Time{},
				Connections: nil,
			}, nil
		}
	}
}

func dependenciesForTest(model *Model) *Dependencies {
	adapter, ok := model.deps.Catalog.(*testServiceAdapter)
	if !ok {
		panic("test model catalog source is not a test service adapter")
	}

	return adapter.deps
}

func sessionNamesForTest(rows []session.Session) []string {
	names := make([]string, len(rows))
	for index, row := range rows {
		names[index] = row.Name
	}

	return names
}

func slicesEqual(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}

	return true
}

func newTestModelWithConfig(t *testing.T, cfg *config.Config) *Model {
	t.Helper()

	model, err := newModel(cfg, testState())
	if err != nil {
		t.Fatalf("newModel() error = %v", err)
	}

	return model
}

func updateTestModelWithCommand(t *testing.T, model *Model, msg tea.Msg) *Model {
	t.Helper()

	updated, cmd := model.Update(msg)

	next, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update() returned %T, want *Model", updated)
	}

	if cmd == nil {
		return next
	}

	return updateTestModel(t, next, cmd())
}

func assertRightBorder(t *testing.T, output string, wantWidth int) {
	t.Helper()

	lines := strings.Split(stripANSI(output), "\n")
	if len(lines) < 2 {
		t.Fatalf("frame output has %d lines, want bordered frame: %q", len(lines), output)
	}

	for index, line := range lines {
		if got := lipgloss.Width(line); got != wantWidth {
			t.Fatalf("line %d width = %d, want %d: %q", index, got, wantWidth, line)
		}

		if index == 0 && (!strings.HasPrefix(line, "┌") || !strings.HasSuffix(line, "┐")) {
			t.Fatalf("line %d missing top border corner: %q", index, line)
		}

		if !strings.HasSuffix(line, "│") && !strings.HasSuffix(line, "┐") && !strings.HasSuffix(line, "┘") {
			t.Fatalf("line %d missing right border: %q", index, line)
		}
	}
}

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

func mustUserHomeDir(t *testing.T) string {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Fatalf("user home dir = %q/%v, want usable home dir", home, err)
	}

	return home
}

func keyText(text string) tea.KeyPressMsg {
	runes := []rune(text)

	code := rune(0)
	if len(runes) == 1 {
		code = runes[0]
	}

	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}

func keyCode(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

func keyCtrl(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl})
}

func keyShiftTab() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift})
}

func keyCtrlCode(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl})
}

func keyAlt(text string) tea.KeyPressMsg {
	runes := []rune(text)
	code := rune(0)
	shiftedCode := rune(0)
	mod := tea.ModAlt

	if len(runes) == 1 {
		code = runes[0]
		if text != strings.ToLower(text) {
			code = []rune(strings.ToLower(text))[0]
			shiftedCode = runes[0]
			mod |= tea.ModShift
		}
	}

	return tea.KeyPressMsg(tea.Key{Code: code, ShiftedCode: shiftedCode, Text: text, Mod: mod})
}

func mouseClick(x int, y int, button tea.MouseButton) tea.MouseClickMsg {
	return tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: button})
}

func mouseWheel(x int, y int, button tea.MouseButton) tea.MouseWheelMsg {
	return tea.MouseWheelMsg(tea.Mouse{X: x, Y: y, Button: button})
}

func mouseMotion(x int, y int) tea.MouseMotionMsg {
	return tea.MouseMotionMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseNone})
}

func viewString(view tea.View) string {
	return view.Content
}

func testState() *session.State {
	return &session.State{}
}

func newModel(cfg *config.Config, st *session.State) (*Model, error) {
	return newModelWithContext(context.Background(), cfg, st, testServices(Dependencies{}))
}

func newModelWithDeps(cfg *config.Config, st *session.State, deps Dependencies) (*Model, error) {
	return newModelWithContext(context.Background(), cfg, st, testServices(deps))
}

func newModelWithTestContext(
	ctx context.Context,
	cfg *config.Config,
	st *session.State,
	deps Dependencies,
) (*Model, error) {
	return newModelWithContext(ctx, cfg, st, testServices(deps))
}

func newTestModel() *Model {
	model, err := newModel(defaultCompactTestConfig(), testState())
	if err != nil {
		panic(err)
	}

	return model
}

func newTestModelWithDeps(deps Dependencies) *Model {
	model, err := newModelWithDeps(defaultCompactTestConfig(), testState(), deps)
	if err != nil {
		panic(err)
	}

	return model
}

func defaultCompactTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Display.AgentsAgentFormat = "{tree}{harness:<6} · {agent_session}{right}{active_marker}{status_age:>3}  {status}"

	return cfg
}

func updateTestModel(t *testing.T, model *Model, msg tea.Msg) *Model {
	t.Helper()

	updated, _ := model.Update(msg)

	next, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update() returned %T, want *Model", updated)
	}

	return next
}

func newModelWithContext(
	ctx context.Context,
	cfg *config.Config,
	st *session.State,
	services Services,
) (*Model, error) {
	model, err := buildModel(ctx, cfg, st, services)
	if err != nil {
		return nil, err
	}

	if adapter, ok := services.Catalog.(*testServiceAdapter); ok {
		adapter.model = model
		adapter.deps.DeferSwitch = model.mode == modePopup
		adapter.deps.DeferAttach = model.mode == modeFullscreen
	}

	return model, nil
}

type actionTrace struct {
	results []actionResultMsg
	entered *sessionActionResultMsg
	quit    bool
}

func runActionTrace(t *testing.T, model *Model, cmd tea.Cmd) actionTrace {
	t.Helper()

	var trace actionTrace

	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]

		if next == nil {
			continue
		}

		switch msg := next().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tea.QuitMsg:
			trace.quit = true
		case actionResultMsg:
			trace.results = append(trace.results, msg)

			_, follow := model.Update(msg)
			queue = append(queue, follow)
		case sessionActionResultMsg:
			trace.entered = &msg
		case nil:
		default:
			model.Update(msg)
		}
	}

	return trace
}

func invokeTestAction(t *testing.T, model *Model, id action.ID) actionTrace {
	t.Helper()

	handler := model.invokeAction(id, nil)
	if handler == nil {
		t.Fatalf("invokeAction(%s) returned nil", id)
	}

	_, cmd := handler()

	return runActionTrace(t, model, cmd)
}

func pressTestKey(t *testing.T, model *Model, key tea.KeyPressMsg) actionTrace {
	t.Helper()

	_, cmd := model.Update(key)

	return runActionTrace(t, model, cmd)
}

func setActionConfirm(cfg *config.Config, id action.ID, confirm bool) {
	if cfg.Actions == nil {
		cfg.Actions = config.ActionsConfig{}
	}

	if cfg.Actions[id.Group()] == nil {
		cfg.Actions[id.Group()] = map[string]config.ActionConfig{}
	}

	cfg.Actions[id.Group()][id.Verb()] = config.ActionConfig{Confirm: &confirm, AllowRemote: nil}
}

func openTestKillConfirm(model *Model) {
	values := action.Values{}

	model.pending = &pendingAction{
		entry:   action.SessionKill,
		subject: action.SubjectOf(model.filtered[model.cursor]),
		values:  values,
		param:   emptyParam(),
	}
	model.dialog = dialogConfirm
}

func testBindings(cfg *config.Config) []action.Binding {
	bindings, err := action.Bindings(cfg)
	if err != nil {
		panic(err)
	}

	return bindings
}
