package tui

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func runEnterAction(t *testing.T, model *Model) sessionActionResultMsg {
	t.Helper()

	handler := model.handleEnter()
	if handler == nil {
		t.Fatal("handleEnter() returned nil")
	}

	_, cmd := handler()
	if cmd == nil {
		t.Fatal("handleEnter() returned nil command")
	}

	rawMsg := cmd()

	msg, ok := rawMsg.(sessionActionResultMsg)
	if !ok {
		t.Fatalf("command message type = %T, want sessionActionResultMsg", rawMsg)
	}

	return msg
}

func runPopupLiveEnterAction(t *testing.T, model *Model) sessionActionResultMsg {
	t.Helper()

	msg := runEnterAction(t, model)

	if msg.err != nil ||
		!msg.quit ||
		msg.switchSession != testSupportSessionDevName ||
		msg.operation != tuiOperationEnterLiveSession ||
		!msg.deferred {
		t.Fatalf("popup live enter msg = %#v, want typed deferred switch", msg)
	}

	return msg
}

func handlePopupLiveEnterResult(t *testing.T, model *Model, msg sessionActionResultMsg) tea.Cmd {
	t.Helper()

	_, quitCmd := model.handleSessionActionResult(msg)
	if quitCmd == nil {
		t.Fatal("handleSessionActionResult() returned nil command")
	}

	return quitCmd
}

func isQuitMsg(msg tea.Msg) bool {
	_, ok := msg.(tea.QuitMsg)
	return ok
}

func newModelWithSnapshotService(deps Dependencies) *Model {
	services := testServices(deps)

	adapter, ok := services.Catalog.(*testServiceAdapter)
	if !ok {
		panic("test model catalog source is not a test service adapter")
	}

	services.Snapshots = adapter

	model, err := newModelWithContext(context.Background(), config.DefaultConfig(), testState(), services)
	if err != nil {
		panic(err)
	}

	return model
}

func TestEnterLiveRowSwitchesWithoutRestore(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive}}

	deps := dependenciesForTest(model)
	deps.TmuxSessionExists = func(context.Context, string) (bool, error) { return true, nil }
	deps.TmuxIsInsideTmux = func() bool { return true }

	switched := ""
	deps.TmuxSwitchSession = func(_ context.Context, name string) error {
		switched = name
		return nil
	}
	deps.RestoreSavedSession = func(_ context.Context, name string) error {
		t.Fatalf("RestoreSavedSession(%q) called for live row", name)
		return nil
	}

	msg := runEnterAction(t, model)

	if msg.err != nil ||
		!msg.quit ||
		msg.refresh ||
		msg.deferred ||
		msg.operation != tuiOperationEnterLiveSession {
		t.Fatalf("live enter msg = %#v, want typed successful quit without refresh", msg)
	}

	if switched != testSupportSessionDevName {
		t.Fatalf("switched session = %q, want dev", switched)
	}

	updated, quitCmd := model.handleSessionActionResult(msg)
	if quitCmd == nil || !isQuitMsg(quitCmd()) {
		t.Fatalf("quit command = %v, want tea.QuitMsg", quitCmd)
	}

	next, ok := updated.(*Model)
	if !ok {
		t.Fatalf("handleSessionActionResult() model type = %T, want *Model", updated)
	}

	if next.DeferredSwitchSession() != "" || next.DeferredSwitchPane() != "" {
		t.Fatalf(
			"deferred switch = %q/%q, want none after an in-process switch",
			next.DeferredSwitchSession(),
			next.DeferredSwitchPane(),
		)
	}
}

func TestEnterAgentPaneRecordsViewedState(t *testing.T) {
	model := newTestModel()
	row := session.Session{Name: testSupportSessionDevName, Kind: session.KindLive}
	agent := session.AgentSession{PaneID: "%7"}

	activated := false

	deps := dependenciesForTest(model)
	deps.TmuxActivatePane = func(_ context.Context, sessionName string, paneID string) error {
		activated = sessionName == testSupportSessionDevName && paneID == "%7"
		return nil
	}

	saves := 0
	deps.SaveModelState = func(*session.State) error {
		saves++
		return nil
	}

	msg, ok := model.runAgentSessionAction(row, agent)().(sessionActionResultMsg)
	if !ok || msg.err != nil || !msg.quit || !activated || saves != 1 {
		t.Fatalf("agent enter = message:%#v ok:%v activated:%v saves:%d", msg, ok, activated, saves)
	}

	model = updateTestModel(t, model, msg)

	if got := model.state.GetLastAgentView(row.SemanticIdentity(), "%7"); got == 0 {
		t.Fatal("agent enter did not record viewed state")
	}
}

func TestEnterLiveSessionWithActiveAgentRecordsViewedState(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{{
		Name:         testSupportSessionDevName,
		Kind:         session.KindLive,
		ActivePaneID: "%7",
		Agents:       []session.AgentSession{{PaneID: "%7", Status: session.AgentStatusIdle}},
	}}

	deps := dependenciesForTest(model)

	switched := ""
	deps.TmuxSwitchSession = func(_ context.Context, name string) error {
		switched = name
		return nil
	}

	saves := 0
	deps.SaveModelState = func(*session.State) error {
		saves++
		return nil
	}

	cmd := model.runLiveSessionAction(model.sessions[0])

	msg, ok := cmd().(sessionActionResultMsg)
	if !ok || msg.err != nil || !msg.quit || switched != testSupportSessionDevName || saves == 0 {
		t.Fatalf("live enter = message:%#v ok:%v switched:%v saves:%d", msg, ok, switched, saves)
	}

	model = updateTestModel(t, model, msg)

	identity := model.sessions[0].SemanticIdentity()

	if got := model.state.GetLastAgentView(identity, "%7"); got == 0 {
		t.Fatal("live session enter did not record active agent pane viewed state")
	}

	if got := model.state.GetLastSwitchIdentity(identity); got == 0 {
		t.Fatal("live session enter did not record the session switch")
	}
}

func TestEnterLiveRowInPopupDefersSwitchUntilAfterQuit(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive}}

	deps := dependenciesForTest(model)
	deps.TmuxSessionExists = func(context.Context, string) (bool, error) { return true, nil }
	deps.TmuxIsInsideTmux = func() bool { return true }

	switched := ""
	deps.TmuxSwitchSession = func(_ context.Context, name string) error {
		switched = name
		return nil
	}

	saveCalls := 0
	deps.SaveModelState = func(*session.State) error {
		saveCalls++
		return nil
	}

	msg := runPopupLiveEnterAction(t, model)
	quitCmd := handlePopupLiveEnterResult(t, model, msg)

	if quitMsg := quitCmd(); !isQuitMsg(quitMsg) {
		t.Fatalf("quit command message = %T, want tea.QuitMsg", quitMsg)
	}

	if switched != "" {
		t.Fatalf("TmuxSwitchSession called before quit with %q, want deferred", switched)
	}

	if got := model.DeferredSwitchSession(); got != testSupportSessionDevName {
		t.Fatalf("DeferredSwitchSession() = %q, want dev", got)
	}

	if got := model.DeferredSwitchPane(); got != "" {
		t.Fatalf("DeferredSwitchPane() = %q, want none for a session switch", got)
	}

	if saveCalls != 0 {
		t.Fatalf("SaveModelState calls = %d, want 0 before deferred switch save", saveCalls)
	}
}

func TestEnterLiveSessionOutsideTmuxAttachesThroughTerminalExec(t *testing.T) {
	model := newTestModel()
	model.mode = modeStandalone
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive}}

	deps := dependenciesForTest(model)
	deps.TmuxIsInsideTmux = func() bool { return false }
	deps.TmuxSwitchSession = func(_ context.Context, name string) error {
		t.Fatalf("TmuxSwitchSession(%q) called outside tmux, want attach", name)
		return nil
	}

	attached := ""
	deps.TmuxAttachCommand = func(ctx context.Context, sessionName string) *exec.Cmd {
		attached = sessionName

		return exec.CommandContext(ctx, "true")
	}

	msg := runEnterAction(t, model)

	if msg.err != nil || !msg.attach || !msg.quit || msg.deferred {
		t.Fatalf("standalone live enter msg = %#v, want terminal attach", msg)
	}

	_, attachCmd := model.handleSessionActionResult(msg)

	if attachCmd == nil {
		t.Fatal("handleSessionActionResult() returned no attach command")
	}

	if attached != testSupportSessionDevName {
		t.Fatalf("attach command session = %q, want dev", attached)
	}

	if model.DeferredSwitchSession() != "" {
		t.Fatalf("DeferredSwitchSession() = %q, want none while attaching", model.DeferredSwitchSession())
	}
}

func TestEnterAgentRowInPopupDefersPaneSwitchUntilAfterQuit(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		Name:   testSupportSessionDevName,
		Kind:   session.KindLive,
		Agents: []session.AgentSession{{RegistryID: testSupportAgentRegistryID, PaneID: "%7"}},
	}}
	model.agentCursor = 0

	deps := dependenciesForTest(model)

	activated := false
	deps.TmuxActivatePane = func(context.Context, string, string) error {
		activated = true
		return nil
	}

	msg := runEnterAction(t, model)

	if msg.err != nil || !msg.deferred || !msg.quit || msg.operation != tuiOperationActivateAgentPane {
		t.Fatalf("popup agent enter msg = %#v, want deferred pane switch", msg)
	}

	if msg.switchSession != testSupportSessionDevName || msg.switchPane != "%7" {
		t.Fatalf("popup agent enter switch = %q/%q, want dev/%%7", msg.switchSession, msg.switchPane)
	}

	quitCmd := handlePopupLiveEnterResult(t, model, msg)

	if quitMsg := quitCmd(); !isQuitMsg(quitMsg) {
		t.Fatalf("quit command message = %T, want tea.QuitMsg", quitMsg)
	}

	if activated {
		t.Fatal("TmuxActivatePane called before quit for a deferred popup switch")
	}

	if got := model.DeferredSwitchSession(); got != testSupportSessionDevName {
		t.Fatalf("DeferredSwitchSession() = %q, want dev", got)
	}

	if got := model.DeferredSwitchPane(); got != "%7" {
		t.Fatalf("DeferredSwitchPane() = %q, want %%7", got)
	}
}

func TestEnterRemoteRowReportsUnsupportedStatus(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		ID:           "$7",
		Name:         testSupportSessionDevName,
		Kind:         session.KindLive,
		Origin:       session.OriginSSH,
		RemoteStatus: session.RemoteStatusAvailable,
		RuntimeIdentity: session.RuntimeIdentity{
			ConnectionID:    testSupportSessionWorkName,
			ConnectionLabel: "Work",
		},
		Agents: []session.AgentSession{{RegistryID: testSupportAgentRegistryID, PaneID: "%9"}},
	}}
	model.agentCursor = 0

	deps := dependenciesForTest(model)
	deps.TmuxActivatePane = func(_ context.Context, sessionName string, paneID string) error {
		t.Fatalf("TmuxActivatePane(%q, %q) called for a remote row", sessionName, paneID)
		return nil
	}
	deps.TmuxSwitchSession = func(_ context.Context, name string) error {
		t.Fatalf("TmuxSwitchSession(%q) called for a remote row", name)
		return nil
	}

	handler := model.handleEnter()
	if handler == nil {
		t.Fatal("handleEnter() returned nil")
	}

	_, cmd := handler()

	if cmd != nil {
		t.Fatal("handleEnter() returned a command for a remote row")
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "not supported for remote sessions") {
		t.Fatalf(
			"remote enter status = (%v, %q), want unsupported error",
			model.statusLevel,
			model.statusMessage,
		)
	}

	if model.DeferredSwitchPane() != "" || model.DeferredSwitchSession() != "" {
		t.Fatalf(
			"remote enter deferred switch = %q/%q, want none",
			model.DeferredSwitchSession(),
			model.DeferredSwitchPane(),
		)
	}
}

func TestEnterOriginBridgeRowReportsUnsupportedStatus(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{
		Name:   "notes",
		Kind:   session.KindLive,
		Origin: session.OriginBridge,
		RuntimeIdentity: session.RuntimeIdentity{
			ConnectionID:    "origin",
			ConnectionLabel: "Laptop",
		},
	}}
	model.cursor = 0

	deps := dependenciesForTest(model)
	deps.TmuxSwitchSession = func(_ context.Context, name string) error {
		t.Fatalf("TmuxSwitchSession(%q) called for an origin row", name)
		return nil
	}

	handler := model.handleEnter()
	if handler == nil {
		t.Fatal("handleEnter() returned nil")
	}

	_, cmd := handler()

	if cmd != nil {
		t.Fatal("handleEnter() returned a command for an origin row")
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "origin sessions") {
		t.Fatalf(
			"origin enter status = (%v, %q), want unsupported error",
			model.statusLevel,
			model.statusMessage,
		)
	}
}

func TestRemoteManagementActionsReportUnsupportedStatus(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{
		Name:   testSupportSessionDevName,
		Kind:   session.KindLive,
		Origin: session.OriginSSH,
	}}

	actions := []struct {
		name string
		run  func() func() (tea.Model, tea.Cmd)
	}{
		{name: "Rename", run: model.handleRename},
		{name: "Kill", run: model.handleKill},
		{name: "Delete", run: model.handleDelete},
		{name: "Snapshot", run: model.handleSnapshot},
	}

	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			model.clearStatus()

			handler := action.run()
			if handler == nil {
				t.Fatalf("%s action returned nil", action.name)
			}

			handler()

			if model.statusLevel != statusError ||
				!strings.Contains(model.statusMessage, action.name+" is not supported for remote sessions") {
				t.Fatalf(
					"%s status = (%v, %q), want unsupported remote error",
					action.name,
					model.statusLevel,
					model.statusMessage,
				)
			}
		})
	}
}

func TestEnterSavedRowRestoresSwitchesAndRequestsRefresh(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: testSupportSessionArchiveName, Kind: session.KindSaved}}

	deps := dependenciesForTest(model)
	deps.TmuxSessionExists = func(context.Context, string) (bool, error) { return false, nil }
	deps.TmuxIsInsideTmux = func() bool { return true }

	restored := ""
	deps.RestoreSavedSession = func(_ context.Context, name string) error {
		restored = name
		return nil
	}

	switched := ""
	deps.TmuxSwitchSession = func(_ context.Context, name string) error {
		switched = name
		return nil
	}

	msg := runEnterAction(t, model)

	if restored != testSupportSessionArchiveName {
		t.Fatalf("restored session = %q, want archive", restored)
	}

	if msg.err != nil ||
		!msg.quit ||
		!msg.refresh ||
		msg.deferred ||
		msg.operation != tuiOperationRestoreSavedSession {
		t.Fatalf("saved enter msg = %#v, want typed successful quit with refresh", msg)
	}

	if switched != testSupportSessionArchiveName {
		t.Fatalf("switched session = %q, want archive", switched)
	}
}

func TestHandleSessionActionResultRefreshesAfterSavedRestore(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{{Name: testSupportSessionArchiveName, Kind: session.KindSaved}}
	model.filtered = model.sessions

	deps := dependenciesForTest(model)
	deps.SaveModelState = func(*session.State) error { return nil }
	deps.LoadCatalog = func(context.Context) (session.CatalogResult, error) {
		return session.CatalogResult{
			Sessions: []session.Session{{Name: testSupportSessionArchiveName, Kind: session.KindLive}},
			Status:   session.StatusComplete,
		}, nil
	}

	updatedModel, cmd := model.handleSessionActionResult(sessionActionResultMsg{
		sessionName: testSupportSessionArchiveName,
		refresh:     true,
	})

	if cmd != nil {
		t.Fatal("handleSessionActionResult() returned command for non-quit refresh")
	}

	updated, ok := updatedModel.(*Model)
	if !ok {
		t.Fatalf("handleSessionActionResult() model type = %T, want *Model", updatedModel)
	}

	if len(updated.sessions) != 1 {
		t.Fatalf("sessions after refresh = %#v, want one live archive row", updated.sessions)
	}

	if got := updated.sessions[0]; got.Name != testSupportSessionArchiveName || got.Kind != session.KindLive {
		t.Fatalf("refreshed row = %#v, want live archive row", got)
	}
}

func TestSavedSessionEnterGuardsLiveConflict(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: testSupportSessionArchiveName, Kind: session.KindSaved}}

	deps := dependenciesForTest(model)
	deps.TmuxSessionExists = func(context.Context, string) (bool, error) { return true, nil }
	deps.RestoreSavedSession = func(_ context.Context, name string) error {
		t.Fatalf("RestoreSavedSession(%q) called while a live session exists", name)
		return nil
	}

	msg := runEnterAction(t, model)

	if msg.err == nil ||
		!strings.Contains(msg.err.Error(), "already live") ||
		msg.operation != tuiOperationRestoreSavedSession {
		t.Fatalf("saved enter result = %#v, want typed live conflict", msg)
	}

	updated := updateTestModel(t, model, msg)

	if updated.statusLevel != statusError || !strings.Contains(updated.statusMessage, "already live") {
		t.Fatalf(
			"conflict status = (%v, %q), want already-live error",
			updated.statusLevel,
			updated.statusMessage,
		)
	}
}

func TestSavedRowsCannotRenameOrKill(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: testSupportSessionArchiveName, Kind: session.KindSaved}}

	if cmd := model.handleRename(); cmd == nil {
		t.Fatal("handleRename() returned nil")
	} else {
		cmd()
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "Rename unavailable") {
		t.Fatalf("rename status = (%v, %q), want saved-row error", model.statusLevel, model.statusMessage)
	}

	model.clearStatus()

	if cmd := model.handleKill(); cmd == nil {
		t.Fatal("handleKill() returned nil")
	} else {
		cmd()
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "Kill unavailable") {
		t.Fatalf("kill status = (%v, %q), want saved-row error", model.statusLevel, model.statusMessage)
	}
}

func TestAgentChildRenameTargetsOwningSession(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		Name: testSupportSessionProjectName,
		Kind: session.KindLive,
		Agents: []session.AgentSession{{
			RegistryID: testSupportAgentRegistryID, PaneID: "%7",
		}},
	}}
	model.agentCursor = 0
	model.textInput.SetValue(testSupportSessionProjectName)

	if handler := model.handleRename(); handler == nil {
		t.Fatal("handleRename() returned nil for child selection")
	} else {
		handler()
	}

	if model.dialog != dialogInputRename {
		t.Fatalf("rename dialog = %v, want input dialog", model.dialog)
	}

	deps := dependenciesForTest(model)
	deps.TmuxSessionExists = func(context.Context, string) (bool, error) { return false, nil }

	renamedFrom := ""
	renamedTo := ""
	deps.TmuxRenameSession = func(_ context.Context, oldName string, newName string) error {
		renamedFrom = oldName
		renamedTo = newName

		return nil
	}
	deps.SaveModelState = func(*session.State) error { return nil }

	model.renameInput.SetValue("renamed")

	if handler := model.submitRename(); handler == nil {
		t.Fatal("submitRename() returned nil")
	}

	if renamedFrom != testSupportSessionProjectName || renamedTo != "renamed" {
		t.Fatalf("child rename target = %q -> %q, want project -> renamed", renamedFrom, renamedTo)
	}

	if model.textInput.Value() != testSupportSessionProjectName {
		t.Fatalf("search query after rename = %q, want project", model.textInput.Value())
	}
}

func TestAgentChildKillTargetsOwningSessionWithParentSafetyRules(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.config.Settings.ConfirmKill = true
	model.filtered = []session.Session{{
		Name: testSupportSessionProjectName,
		Kind: session.KindLive,
		Agents: []session.AgentSession{{
			RegistryID: testSupportAgentRegistryID, PaneID: "%7",
		}},
	}}
	model.agentCursor = 0

	killed := ""

	deps := dependenciesForTest(model)
	deps.TmuxKillSession = func(_ context.Context, name string) error {
		killed = name
		return nil
	}

	if handler := model.handleKill(); handler == nil {
		t.Fatal("handleKill() returned nil for child selection")
	} else {
		handler()
	}

	if model.dialog != dialogConfirmKill || killed != "" {
		t.Fatalf("child kill before confirmation = dialog:%v killed:%q", model.dialog, killed)
	}

	if handler := model.handleConfirmActionKeys(keyText("y"), "Kill failed", tuiOperationKillSession); handler == nil {
		t.Fatal("confirmed child kill returned nil")
	}

	if killed != testSupportSessionProjectName {
		t.Fatalf("child kill target = %q, want project", killed)
	}
}

func TestAgentChildKillPreservesAttachedAndErrorSafety(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.config.Settings.ConfirmKill = false
	model.filtered = []session.Session{{
		Name:     "attached",
		Kind:     session.KindLive,
		Attached: true,
		Agents:   []session.AgentSession{{RegistryID: testSupportAgentRegistryID, PaneID: "%7"}},
	}}
	model.agentCursor = 0

	killCalls := 0

	deps := dependenciesForTest(model)
	deps.TmuxKillSession = func(_ context.Context, _ string) error {
		killCalls++
		return errTmuxUnavailable
	}

	model.handleKill()()

	if killCalls != 0 || model.dialog != dialogNone {
		t.Fatalf("attached child kill = calls:%d dialog:%v, want no action", killCalls, model.dialog)
	}

	model.filtered[0].Attached = false
	model.handleKill()()

	if killCalls != 1 || model.statusLevel != statusError || !strings.Contains(model.statusMessage, "Kill failed") {
		t.Fatalf("failed child kill = calls:%d status:(%v,%q)", killCalls, model.statusLevel, model.statusMessage)
	}
}

func TestDeleteSavedRowRemovesState(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{{Name: testSupportSessionArchiveName, Kind: session.KindSaved}}
	model.filtered = model.sessions

	identity := model.filtered[0].SemanticIdentity()
	model.state.RecordSwitchIdentity(identity)

	deleted := ""

	deps := dependenciesForTest(model)
	deps.DeleteSavedSession = func(_ context.Context, name string) (int, error) {
		deleted = name
		return 1, nil
	}
	deps.SaveModelState = func(*session.State) error { return nil }
	deps.TmuxKillSession = func(_ context.Context, name string) error {
		t.Fatalf("TmuxKillSession(%q) called for a saved row", name)
		return nil
	}

	if cmd := model.handleDelete(); cmd == nil {
		t.Fatal("handleDelete() returned nil")
	} else {
		cmd()
	}

	if model.dialog != dialogConfirmDelete {
		t.Fatalf("dialog = %v, want confirm delete", model.dialog)
	}

	updated := updateTestModel(t, model, keyText("y"))

	if deleted != testSupportSessionArchiveName {
		t.Fatalf("DeleteSavedSession() name = %q, want archive", deleted)
	}

	if updated.state.GetLastSwitchIdentity(identity) != 0 {
		t.Fatal("archive switch history was not removed")
	}
}

func TestDeleteLiveDetachedRowKillsAndDeletesSavedRecord(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive}}
	model.filtered = model.sessions

	identity := model.filtered[0].SemanticIdentity()
	model.state.RecordSwitchIdentity(identity)

	killed := ""
	deleted := ""

	deps := dependenciesForTest(model)
	deps.TmuxSessionExists = func(context.Context, string) (bool, error) { return true, nil }
	deps.TmuxKillSession = func(_ context.Context, name string) error {
		killed = name
		return nil
	}
	deps.DeleteSavedSession = func(_ context.Context, name string) (int, error) {
		deleted = name
		return 1, nil
	}
	deps.SaveModelState = func(*session.State) error { return nil }

	if cmd := model.handleDelete(); cmd == nil {
		t.Fatal("handleDelete() returned nil")
	} else {
		cmd()
	}

	updated := updateTestModel(t, model, keyText("y"))

	if killed != testSupportSessionDevName {
		t.Fatalf("TmuxKillSession() name = %q, want dev", killed)
	}

	if deleted != testSupportSessionDevName {
		t.Fatalf("DeleteSavedSession() name = %q, want dev", deleted)
	}

	if updated.state.GetLastSwitchIdentity(identity) != 0 {
		t.Fatal("dev switch history was not removed")
	}
}

func TestKillConfirmationCanBeDisabled(t *testing.T) {
	model := newTestModel()
	model.config.Settings.ConfirmKill = false
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive}}

	killed := ""

	deps := dependenciesForTest(model)
	deps.TmuxKillSession = func(_ context.Context, name string) error {
		killed = name
		return nil
	}

	if cmd := model.handleKill(); cmd == nil {
		t.Fatal("handleKill() returned nil")
	} else {
		cmd()
	}

	if killed != testSupportSessionDevName {
		t.Fatalf("TmuxKillSession() name = %q, want dev", killed)
	}

	if model.dialog != dialogNone {
		t.Fatalf("dialog = %v, want none when confirmation is disabled", model.dialog)
	}

	if model.statusLevel != statusNone {
		t.Fatalf("status = (%v, %q), want no error after an unconfirmed kill", model.statusLevel, model.statusMessage)
	}
}

func TestDeleteConfirmationCanBeDisabled(t *testing.T) {
	model := newTestModel()
	model.config.Settings.ConfirmDelete = false
	model.filtered = []session.Session{{Name: testSupportSessionArchiveName, Kind: session.KindSaved}}

	deleted := ""

	deps := dependenciesForTest(model)
	deps.DeleteSavedSession = func(_ context.Context, name string) (int, error) {
		deleted = name
		return 1, nil
	}
	deps.SaveModelState = func(*session.State) error { return nil }

	if cmd := model.handleDelete(); cmd == nil {
		t.Fatal("handleDelete() returned nil")
	} else {
		cmd()
	}

	if deleted != testSupportSessionArchiveName {
		t.Fatalf("DeleteSavedSession() name = %q, want archive", deleted)
	}

	if model.dialog != dialogNone {
		t.Fatalf("dialog = %v, want none when confirmation is disabled", model.dialog)
	}
}

func TestDeleteSelectedRowMovesSelectionToPreviousEntry(t *testing.T) {
	model := newTestModel()
	model.config.Settings.ConfirmDelete = false
	model.sessions = []session.Session{
		{Name: testSupportSessionAlphaName, Kind: session.KindSaved},
		{Name: "beta", Kind: session.KindSaved},
		{Name: "gamma", Kind: session.KindSaved},
	}
	model.filtered = model.sessions
	model.cursor = 1

	deps := dependenciesForTest(model)
	deps.DeleteSavedSession = func(_ context.Context, name string) (int, error) {
		if name != "beta" {
			t.Fatalf("DeleteSavedSession() name = %q, want beta", name)
		}

		return 1, nil
	}
	deps.SaveModelState = func(*session.State) error { return nil }
	deps.LoadCatalog = func(context.Context) (session.CatalogResult, error) {
		return session.CatalogResult{
			Sessions: []session.Session{
				{Name: testSupportSessionAlphaName, Kind: session.KindSaved},
				{Name: "gamma", Kind: session.KindSaved},
			},
			Status: session.StatusComplete,
		}, nil
	}

	action := model.handleDelete()
	if action == nil {
		t.Fatal("handleDelete() returned nil")
	}

	_, cmd := action()

	if model.cursor != 0 {
		t.Fatalf("cursor after delete = %d, want previous row index 0", model.cursor)
	}

	if cmd == nil {
		t.Fatal("delete command = nil, want refresh command")
	}

	updated := updateTestModel(t, model, cmd())
	if updated.cursor != 0 {
		t.Fatalf("cursor after refresh = %d, want previous row index 0", updated.cursor)
	}

	if len(updated.filtered) != 2 {
		t.Fatalf("filtered after refresh = %#v, want two rows", updated.filtered)
	}

	if got := updated.filtered[updated.cursor].Name; got != testSupportSessionAlphaName {
		t.Fatalf("selected row after refresh = %q, want alpha", got)
	}
}

func TestDeleteAttachedLiveRowIsUnavailable(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive, Attached: true}}

	deps := dependenciesForTest(model)
	deps.DeleteSavedSession = func(_ context.Context, name string) (int, error) {
		t.Fatalf("DeleteSavedSession(%q) called for an attached row", name)
		return 0, nil
	}

	if cmd := model.handleDelete(); cmd == nil {
		t.Fatal("handleDelete() returned nil")
	} else {
		cmd()
	}

	if model.dialog != dialogNone {
		t.Fatalf("dialog = %v, want none", model.dialog)
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "attached") {
		t.Fatalf("delete status = (%v, %q), want attached error", model.statusLevel, model.statusMessage)
	}
}

func TestSubmitRenameShowsInlineError(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{Name: "old", Agents: []session.AgentSession{{RegistryID: testSupportAgentRegistryID}}}}
	model.cursor = 0
	model.agentCursor = 0
	model.dialog = dialogInputRename
	model.renameInput.SetValue("new")

	deps := dependenciesForTest(model)
	deps.TmuxRenameSession = func(_ context.Context, target string, newName string) error {
		return errRenameFailed
	}

	modelCmd := model.submitRename()
	modelCmd()

	if model.dialog != dialogInputRename {
		t.Fatalf("dialog = %v, want dialogInputRename", model.dialog)
	}

	if model.statusLevel != statusError {
		t.Fatalf("statusLevel = %v, want statusError", model.statusLevel)
	}

	if !strings.Contains(model.statusMessage, "rename failed") {
		t.Fatalf("statusMessage = %q, want rename error", model.statusMessage)
	}
}

func TestAgentChildDeleteRemainsUnavailable(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{
		Name:   testSupportSessionProjectName,
		Kind:   session.KindLive,
		Agents: []session.AgentSession{{RegistryID: testSupportAgentRegistryID, PaneID: "%7"}},
	}}
	model.agentCursor = 0

	if handler := model.handleDelete(); handler == nil {
		t.Fatal("handleDelete() returned nil")
	} else {
		handler()
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "agent rows") {
		t.Fatalf("child delete status = (%v, %q), want unavailable error", model.statusLevel, model.statusMessage)
	}
}

func TestHandleSessionActionResultSaveFailureKeepsPopupOpen(t *testing.T) {
	model := newTestModel()

	updatedModel, cmd := model.handleSessionActionResult(sessionActionResultMsg{
		sessionName: testSupportSessionDevName,
		quit:        true,
		err:         errSaveFailed,
	})

	if cmd != nil {
		t.Fatal("handleSessionActionResult() returned a quit command on save failure")
	}

	updated, ok := updatedModel.(*Model)
	if !ok {
		t.Fatalf("handleSessionActionResult() model type = %T, want *Model", updatedModel)
	}

	if updated.statusLevel != statusError {
		t.Fatalf("statusLevel = %v, want statusError", updated.statusLevel)
	}

	if !strings.Contains(updated.statusMessage, "save failed") {
		t.Fatalf("statusMessage = %q, want save error", updated.statusMessage)
	}
}

func runSnapshotCaptureSubtest(t *testing.T) {
	t.Helper()

	captured := ""
	catalogLoads := 0

	model := newModelWithSnapshotService(Dependencies{
		SnapshotSession: func(_ context.Context, sessionName string) error {
			captured = sessionName

			return nil
		},
		LoadCatalog: func(context.Context) (session.CatalogResult, error) {
			catalogLoads++

			return session.CatalogResult{
				Sessions: []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive}},
				Status:   session.StatusComplete,
			}, nil
		},
	})
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive, Origin: session.OriginLocal}}

	handler := model.handleSnapshot()
	if handler == nil {
		t.Fatal("handleSnapshot() returned nil")
	}

	_, cmd := handler()
	if cmd == nil {
		t.Fatal("handleSnapshot() returned nil command")
	}

	rawMsg := cmd()

	msg, ok := rawMsg.(sessionActionResultMsg)
	if !ok || msg.err != nil || msg.quit || !msg.refresh || msg.operation != tuiOperationSnapshotSession {
		t.Fatalf("snapshot result = %#v ok:%v, want refresh without quit", rawMsg, ok)
	}

	if captured != testSupportSessionDevName {
		t.Fatalf("SnapshotSession() name = %q, want dev", captured)
	}

	updated := updateTestModel(t, model, msg)

	if catalogLoads != 1 {
		t.Fatalf("catalog loads = %d, want 1 after snapshot refresh", catalogLoads)
	}

	if updated.statusLevel != statusNone {
		t.Fatalf("status = (%v, %q), want cleared after snapshot", updated.statusLevel, updated.statusMessage)
	}
}

func runSnapshotFailureSubtest(t *testing.T) {
	t.Helper()

	model := newModelWithSnapshotService(Dependencies{
		SnapshotSession: func(context.Context, string) error { return errSnapshotFailed },
	})
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive, Origin: session.OriginLocal}}

	handler := model.handleSnapshot()
	if handler == nil {
		t.Fatal("handleSnapshot() returned nil")
	}

	_, cmd := handler()
	if cmd == nil {
		t.Fatal("handleSnapshot() returned nil command")
	}

	msg, ok := cmd().(sessionActionResultMsg)
	if !ok || msg.err == nil {
		t.Fatalf("snapshot failure result = %#v ok:%v, want error", msg, ok)
	}

	updated := updateTestModel(t, model, msg)

	if updated.statusLevel != statusError || !strings.Contains(updated.statusMessage, "snapshot failed") {
		t.Fatalf(
			"snapshot failure status = (%v, %q), want snapshot error",
			updated.statusLevel,
			updated.statusMessage,
		)
	}
}

func TestSnapshotActionCapturesSessionAndRefreshes(t *testing.T) {
	t.Run("captures", func(t *testing.T) {
		runSnapshotCaptureSubtest(t)
	})

	t.Run("reports failure", func(t *testing.T) {
		runSnapshotFailureSubtest(t)
	})
}

func TestSnapshotActionWithoutServiceReportsUnavailable(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive, Origin: session.OriginLocal}}

	if handler := model.handleSnapshot(); handler == nil {
		t.Fatal("handleSnapshot() returned nil")
	} else {
		handler()
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "no snapshot service") {
		t.Fatalf("snapshot status = (%v, %q), want unavailable error", model.statusLevel, model.statusMessage)
	}
}

func TestCreateDialogSubmitsEditedDirectoryToNewSession(t *testing.T) {
	workingDir := t.TempDir()

	model := newTestModelWithDeps(Dependencies{
		GetWorkingDirectory: func() (string, error) { return workingDir, nil },
	})
	model.config.Settings.DefaultSessionPathSuggestion = ""
	model.textInput.SetValue("news")

	createdName := ""
	createdDir := ""

	deps := dependenciesForTest(model)
	deps.TmuxCreateSession = func(_ context.Context, name string, dir string) error {
		createdName = name
		createdDir = dir

		return nil
	}
	deps.RestoreSavedSession = func(_ context.Context, name string) error {
		t.Fatalf("RestoreSavedSession(%q) called for a new session", name)
		return nil
	}

	if handler := model.handleCreate(); handler == nil {
		t.Fatal("handleCreate() returned nil")
	} else {
		handler()
	}

	if model.dialog != dialogInputCreateDir {
		t.Fatalf("dialog = %v, want create-directory input", model.dialog)
	}

	edited := filepath.Join(workingDir, "deep", testSupportSessionWorkName)
	model.createDirInput.SetValue(edited)

	assertCreatedSessionResult(t, model, edited, &createdName, &createdDir)
}

func assertCreatedSessionResult(t *testing.T, model *Model, edited string, createdName, createdDir *string) {
	t.Helper()

	_, cmd := model.submitCreateDir()()
	if cmd == nil {
		t.Fatal("submitCreateDir() returned nil command")
	}

	rawMsg := cmd()

	msg, ok := rawMsg.(sessionActionResultMsg)
	if !ok || msg.err != nil || !msg.quit || msg.operation != tuiOperationEnterOrCreateSession {
		t.Fatalf("create result = %#v ok:%v, want successful entry", rawMsg, ok)
	}

	if *createdName != "news" || *createdDir != edited {
		t.Fatalf("created session = %q in %q, want news in %q", *createdName, *createdDir, edited)
	}

	if model.dialog != dialogNone || model.createSession != "" {
		t.Fatalf("create dialog state = (%v, %q), want closed", model.dialog, model.createSession)
	}

	if model.textInput.Value() != "news" {
		t.Fatalf("search query after create = %q, want news", model.textInput.Value())
	}
}
