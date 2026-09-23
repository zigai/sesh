package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestMain(m *testing.M) {
	_ = os.Unsetenv("TMUX_POPUP")

	directory, err := os.MkdirTemp("", "sesh-tui-tests-")
	if err != nil {
		panic(err)
	}

	for name, value := range map[string]string{
		"HOME": directory, "XDG_CACHE_HOME": filepath.Join(directory, "cache"),
	} {
		if err := os.Setenv(name, value); err != nil {
			panic(err)
		}
	}

	code := m.Run()

	if err := os.RemoveAll(directory); err != nil {
		panic(err)
	}

	os.Exit(code)
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
)

type Dependencies struct {
	OmitEnterer        bool
	OmitRenamer        bool
	OmitKiller         bool
	OmitDeleter        bool
	OmitPanes          bool
	OmitTerminalAttach bool
	OmitSnapshots      bool
	OmitRemoteActions  bool
	OmitRemoteAttach   bool
	OmitOriginBridge   bool
	DeferSwitch        bool
	DeferAttach        bool

	LoadCatalog             func(context.Context) (session.CatalogResult, error)
	EnterSession            func(context.Context, session.Session) (EnterOutcome, error)
	CreateAndEnterSession   func(context.Context, string, string) (EnterOutcome, error)
	RestoreAndEnterSession  func(context.Context, session.Session) (EnterOutcome, error)
	RenameSession           func(context.Context, session.Session, string) error
	KillSession             func(context.Context, session.Session) error
	DeleteSessionEverywhere func(context.Context, session.Session, bool) (DeleteResult, error)
	AttachSession           func(context.Context, session.Session) (tea.ExecCommand, error)
	SnapshotSessionTarget   func(context.Context, session.Session) error
	TmuxActivatePaneTarget  func(context.Context, session.Session, string) error

	TmuxSessionExists       func(context.Context, string) (bool, error)
	TmuxCreateSession       func(context.Context, string, string) error
	TmuxSwitchSession       func(context.Context, string) error
	TmuxAttachCommand       func(context.Context, string) *exec.Cmd
	TmuxActivatePane        func(context.Context, string, string) error
	TmuxRenameSession       func(context.Context, string, string) error
	TmuxKillSession         func(context.Context, string) error
	TmuxIsInsideTmux        func() bool
	LoadAgentSummaries      func(context.Context) ([]session.AgentSummary, error)
	WatchAgentSummaries     func(context.Context, func(uint64, []session.AgentSummary, error) error) error
	TmuxPaneLocations       func(context.Context) ([]PaneLocation, error)
	LoadRemoteCatalog       func() (RemoteCatalog, error)
	WatchRemoteCatalog      func(context.Context, func(RemoteCatalog, error)) error
	RunRemoteAction         func(context.Context, *config.Config, session.Session, RemoteActionRequest) error
	IsRemoteActionPermitted func(*config.Config, session.Session, string) bool
	AttachRemoteSession     func(context.Context, session.Session, string) (tea.ExecCommand, error)
	LoadOriginSessions      func(context.Context) (string, []session.Session, error)
	RequestOriginSwitch     func(context.Context, session.Session) error
	DeleteSavedSession      func(context.Context, string) (int, error)
	RestoreSavedSession     func(context.Context, string) error
	SnapshotSession         func(context.Context, string) error
	GetWorkingDirectory     func() (string, error)
	SaveModelState          func(*session.State) error
	UpdateModelStateBlock   func()
}

type testServiceAdapter struct {
	deps  *Dependencies
	model *Model
}

type execCommandWrapper struct {
	cmd *exec.Cmd
}

func (w *execCommandWrapper) Run() error {
	if err := w.cmd.Run(); err != nil {
		return fmt.Errorf("running test exec command: %w", err)
	}

	return nil
}

func (w *execCommandWrapper) SetStdin(r io.Reader) {
	w.cmd.Stdin = r
}

func (w *execCommandWrapper) SetStdout(w2 io.Writer) {
	w.cmd.Stdout = w2
}

func (w *execCommandWrapper) SetStderr(w2 io.Writer) {
	w.cmd.Stderr = w2
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

func (adapter *testServiceAdapter) CreateAndEnterSession(ctx context.Context, name, dir string) (EnterOutcome, error) {
	if adapter.deps.CreateAndEnterSession != nil {
		return adapter.deps.CreateAndEnterSession(ctx, name, dir)
	}

	if err := adapter.ensureSessionCreated(ctx, name, dir); err != nil {
		return EnterOutcome{SessionName: name, Action: EnterCompleted, Restored: false}, err
	}

	if adapter.isPopup() {
		return EnterOutcome{SessionName: name, Action: EnterNeedsSwitch, Restored: false}, nil
	}

	if adapter.deps.TmuxSwitchSession != nil {
		_ = adapter.deps.TmuxSwitchSession(ctx, name)
	}

	return EnterOutcome{SessionName: name, Action: EnterCompleted, Restored: false}, nil
}

func (adapter *testServiceAdapter) ensureSessionCreated(ctx context.Context, name string, dir string) error {
	if adapter.deps.TmuxSessionExists == nil {
		if adapter.deps.TmuxCreateSession == nil {
			return nil
		}

		return adapter.deps.TmuxCreateSession(ctx, name, dir)
	}

	exists, err := adapter.deps.TmuxSessionExists(ctx, name)
	if err != nil || exists || adapter.deps.TmuxCreateSession == nil {
		return err
	}

	return adapter.deps.TmuxCreateSession(ctx, name, dir)
}

func (adapter *testServiceAdapter) RestoreAndEnterSession(ctx context.Context, target session.Session) (EnterOutcome, error) {
	if adapter.deps.RestoreAndEnterSession != nil {
		return adapter.deps.RestoreAndEnterSession(ctx, target)
	}

	if adapter.deps.TmuxSessionExists != nil {
		exists, err := adapter.deps.TmuxSessionExists(ctx, target.Name)
		if err != nil {
			return EnterOutcome{SessionName: target.Name, Action: EnterCompleted, Restored: false}, err
		}

		if exists {
			return EnterOutcome{SessionName: target.Name, Action: EnterCompleted, Restored: false}, session.ErrSessionAlreadyExists
		}
	}

	if adapter.deps.RestoreSavedSession != nil {
		if err := adapter.deps.RestoreSavedSession(ctx, target.Name); err != nil {
			return EnterOutcome{SessionName: target.Name, Action: EnterCompleted, Restored: false}, err
		}
	}

	if adapter.isPopup() {
		return EnterOutcome{SessionName: target.Name, Action: EnterNeedsSwitch, Restored: true}, nil
	}

	if adapter.deps.TmuxSwitchSession != nil {
		_ = adapter.deps.TmuxSwitchSession(ctx, target.Name)
	}

	return EnterOutcome{SessionName: target.Name, Action: EnterCompleted, Restored: true}, nil
}

func (adapter *testServiceAdapter) RenameSession(ctx context.Context, target session.Session, newName string) error {
	if adapter.deps.RenameSession != nil {
		return adapter.deps.RenameSession(ctx, target, newName)
	}

	if adapter.deps.TmuxRenameSession != nil {
		return adapter.deps.TmuxRenameSession(ctx, target.Name, newName)
	}

	return nil
}

func (adapter *testServiceAdapter) KillSession(ctx context.Context, target session.Session) error {
	if adapter.deps.KillSession != nil {
		return adapter.deps.KillSession(ctx, target)
	}

	if adapter.deps.TmuxKillSession != nil {
		return adapter.deps.TmuxKillSession(ctx, target.Name)
	}

	return nil
}

func (adapter *testServiceAdapter) DeleteSessionEverywhere(ctx context.Context, target session.Session, force bool) (DeleteResult, error) {
	if adapter.deps.DeleteSessionEverywhere != nil {
		return adapter.deps.DeleteSessionEverywhere(ctx, target, force)
	}

	liveKilled := false
	savedDeleted := false

	if adapter.deps.TmuxKillSession != nil && !target.IsSaved() {
		_ = adapter.deps.TmuxKillSession(ctx, target.Name)
		liveKilled = true
	}

	if adapter.deps.DeleteSavedSession != nil {
		_, _ = adapter.deps.DeleteSavedSession(ctx, target.Name)
		savedDeleted = true
	}

	return DeleteResult{LiveKilled: liveKilled, SavedDeleted: savedDeleted}, nil
}

func (adapter *testServiceAdapter) AttachSession(ctx context.Context, target session.Session) (tea.ExecCommand, error) {
	if adapter.deps.AttachSession != nil {
		return adapter.deps.AttachSession(ctx, target)
	}

	if adapter.deps.TmuxAttachCommand != nil {
		cmd := adapter.deps.TmuxAttachCommand(ctx, target.Name)
		if cmd != nil {
			return &execCommandWrapper{cmd: cmd}, nil
		}
	}

	return nil, nil //nolint:nilnil // A nil command explicitly requests application-owned attachment.
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

func (adapter *testServiceAdapter) SnapshotSession(ctx context.Context, target session.Session) error {
	if adapter.deps.SnapshotSessionTarget != nil {
		return adapter.deps.SnapshotSessionTarget(ctx, target)
	}

	if adapter.deps.SnapshotSession != nil {
		return adapter.deps.SnapshotSession(ctx, target.Name)
	}

	return nil
}

func (adapter *testServiceAdapter) LoadAgentSummaries(ctx context.Context) ([]session.AgentSummary, error) {
	return adapter.deps.LoadAgentSummaries(ctx)
}

func (adapter *testServiceAdapter) WatchAgentSummaries(
	ctx context.Context,
	yield func(uint64, []session.AgentSummary, error) error,
) error {
	return adapter.deps.WatchAgentSummaries(ctx, yield)
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

func (adapter *testServiceAdapter) RunRemoteAction(ctx context.Context, cfg *config.Config, target session.Session, request RemoteActionRequest) error {
	if adapter.deps.RunRemoteAction != nil {
		return adapter.deps.RunRemoteAction(ctx, cfg, target, request)
	}

	return nil
}

func (adapter *testServiceAdapter) IsActionPermitted(cfg *config.Config, target session.Session, action string) bool {
	if adapter.deps.IsRemoteActionPermitted != nil {
		return adapter.deps.IsRemoteActionPermitted(cfg, target, action)
	}

	return true
}

func (adapter *testServiceAdapter) AttachRemoteSession(ctx context.Context, target session.Session, paneID string) (tea.ExecCommand, error) {
	if adapter.deps.AttachRemoteSession != nil {
		return adapter.deps.AttachRemoteSession(ctx, target, paneID)
	}

	return nil, nil //nolint:nilnil // A nil command explicitly requests application-owned attachment.
}

func (adapter *testServiceAdapter) LoadOriginSessions(ctx context.Context) (string, []session.Session, error) {
	if adapter.deps.LoadOriginSessions != nil {
		return adapter.deps.LoadOriginSessions(ctx)
	}

	return "", nil, nil
}

func (adapter *testServiceAdapter) RequestOriginSwitch(ctx context.Context, target session.Session) error {
	if adapter.deps.RequestOriginSwitch != nil {
		return adapter.deps.RequestOriginSwitch(ctx, target)
	}

	return nil
}

func (adapter *testServiceAdapter) GetWorkingDirectory() (string, error) {
	return adapter.deps.GetWorkingDirectory()
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
		Renamer:          adapter,
		Killer:           adapter,
		Deleter:          adapter,
		Panes:            adapter,
		TerminalAttach:   adapter,
		Agents:           adapter,
		AgentWatcher:     nil,
		Snapshots:        nil,
		Remote:           adapter,
		RemoteWatcher:    nil,
		RemoteActions:    nil,
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

	if deps.OmitRenamer {
		services.Renamer = nil
	}

	if deps.OmitKiller {
		services.Killer = nil
	}

	if deps.OmitDeleter {
		services.Deleter = nil
	}

	if deps.OmitPanes {
		services.Panes = nil
	}

	if deps.OmitTerminalAttach {
		services.TerminalAttach = nil
	}
}

func addOptionalTestServices(services *Services, deps Dependencies, adapter *testServiceAdapter) {
	if deps.AttachSession != nil || deps.TmuxAttachCommand != nil {
		services.TerminalAttach = adapter
	}

	if deps.SnapshotSession != nil || deps.SnapshotSessionTarget != nil {
		services.Snapshots = adapter
	}

	if deps.WatchAgentSummaries != nil {
		services.AgentWatcher = adapter
	}

	if deps.WatchRemoteCatalog != nil {
		services.RemoteWatcher = adapter
	}

	if deps.RunRemoteAction != nil {
		services.RemoteActions = adapter
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
		adapter.deps.DeferAttach = model.mode == modeStandalone
	}

	return model, nil
}
