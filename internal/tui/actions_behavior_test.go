package tui

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/action"
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
	model, err := newModelWithContext(context.Background(), config.DefaultConfig(), testState(), testServices(deps))
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
	model.mode = modeFullscreen
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

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "unavailable") {
		t.Fatalf(
			"origin enter status = (%v, %q), want unsupported error",
			model.statusLevel,
			model.statusMessage,
		)
	}
}

func TestActionsOnRemoteRowsReportRejectedLocation(t *testing.T) {
	model := newTestModelWithDeps(Dependencies{
		AcceptsAction: func(at action.Location, _ action.ID) bool { return at.Kind == action.LocationLocal },
	})
	model.filtered = []session.Session{{
		Name:   testSupportSessionDevName,
		Kind:   session.KindLive,
		Origin: session.OriginSSH,
	}}

	for _, definition := range []action.Entry{action.SessionRename, action.SessionKill, action.SessionDelete, action.SnapshotSave} {
		descriptor := definition.Describe()

		t.Run(string(descriptor.ID), func(t *testing.T) {
			model.clearStatus()

			trace := invokeTestAction(t, model, descriptor.ID)
			if len(trace.results) != 0 {
				t.Fatalf("%s dispatched for a rejected location: %+v", descriptor.ID, trace.results)
			}

			if model.statusLevel != statusError || !strings.Contains(model.statusMessage, descriptor.Title+" unavailable") {
				t.Fatalf("%s status = (%v, %q), want unavailable error", descriptor.ID, model.statusLevel, model.statusMessage)
			}
		})
	}
}

func TestEnterSavedRowRestoresThenEntersRestoredSession(t *testing.T) {
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

	_, cmd := model.handleEnter()()
	trace := runActionTrace(t, model, cmd)

	if restored != testSupportSessionArchiveName {
		t.Fatalf("restored session = %q, want archive", restored)
	}

	if len(trace.results) != 1 || trace.results[0].err != nil {
		t.Fatalf("restore results = %+v, want one success", trace.results)
	}

	if trace.entered == nil || trace.entered.err != nil || !trace.entered.quit || trace.entered.target.Name != testSupportSessionArchiveName {
		t.Fatalf("entry after restore = %+v, want the restored session", trace.entered)
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

	_, cmd := model.handleEnter()()
	trace := runActionTrace(t, model, cmd)

	if len(trace.results) != 1 || action.CodeOf(trace.results[0].err) != action.CodeAlreadyExists || trace.entered != nil {
		t.Fatalf("saved enter trace = %+v, want an already-exists failure without entry", trace)
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "Restore session failed") {
		t.Fatalf("conflict status = (%v, %q), want restore failure", model.statusLevel, model.statusMessage)
	}
}

func TestSavedRowsCannotRenameOrKill(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Name: testSupportSessionArchiveName, Kind: session.KindSaved}}

	for _, definition := range []action.Entry{action.SessionRename, action.SessionKill} {
		descriptor := definition.Describe()

		model.clearStatus()

		trace := invokeTestAction(t, model, descriptor.ID)
		if len(trace.results) != 0 || model.dialog != dialogNone {
			t.Fatalf("%s started for a saved row: %+v dialog %v", descriptor.ID, trace.results, model.dialog)
		}

		if model.statusLevel != statusError || !strings.Contains(model.statusMessage, descriptor.Title+" unavailable") {
			t.Fatalf("%s status = (%v, %q), want saved-row error", descriptor.ID, model.statusLevel, model.statusMessage)
		}
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

	invokeTestAction(t, model, action.SessionRename.ID)

	if model.dialog != dialogInputText {
		t.Fatalf("rename dialog = %v, want input dialog", model.dialog)
	}

	deps := dependenciesForTest(model)

	renamedFrom := ""
	renamedTo := ""
	deps.TmuxRenameSession = func(_ context.Context, oldName string, newName string) error {
		renamedFrom = oldName
		renamedTo = newName

		return nil
	}
	deps.SaveModelState = func(*session.State) error { return nil }

	model.textParamInput.SetValue("renamed")
	pressTestKey(t, model, keyCode(tea.KeyEnter))

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
	setActionConfirm(model.config, action.SessionKill.ID, true)
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

	invokeTestAction(t, model, action.SessionKill.ID)

	if model.dialog != dialogConfirm || killed != "" {
		t.Fatalf("child kill before confirmation = dialog:%v killed:%q", model.dialog, killed)
	}

	pressTestKey(t, model, keyText("y"))

	if killed != testSupportSessionProjectName {
		t.Fatalf("child kill target = %q, want project", killed)
	}
}

func TestAgentChildKillPreservesAttachedAndErrorSafety(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	setActionConfirm(model.config, action.SessionKill.ID, false)
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

	invokeTestAction(t, model, action.SessionKill.ID)

	if killCalls != 0 || model.dialog != dialogNone {
		t.Fatalf("attached child kill = calls:%d dialog:%v, want no action", killCalls, model.dialog)
	}

	model.filtered[0].Attached = false
	invokeTestAction(t, model, action.SessionKill.ID)

	if killCalls != 1 || model.statusLevel != statusError || !strings.Contains(model.statusMessage, "Kill session failed") {
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

	invokeTestAction(t, model, action.SessionDelete.ID)

	if model.dialog != dialogConfirm {
		t.Fatalf("dialog = %v, want confirm delete", model.dialog)
	}

	pressTestKey(t, model, keyText("y"))

	if deleted != testSupportSessionArchiveName {
		t.Fatalf("DeleteSavedSession() name = %q, want archive", deleted)
	}

	if model.state.GetLastSwitchIdentity(identity) != 0 {
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

	invokeTestAction(t, model, action.SessionDelete.ID)
	pressTestKey(t, model, keyText("y"))

	if killed != testSupportSessionDevName {
		t.Fatalf("TmuxKillSession() name = %q, want dev", killed)
	}

	if deleted != testSupportSessionDevName {
		t.Fatalf("DeleteSavedSession() name = %q, want dev", deleted)
	}

	if model.state.GetLastSwitchIdentity(identity) != 0 {
		t.Fatal("dev switch history was not removed")
	}
}

func TestKillConfirmationCanBeDisabled(t *testing.T) {
	model := newTestModel()
	setActionConfirm(model.config, action.SessionKill.ID, false)
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive}}

	killed := ""

	deps := dependenciesForTest(model)
	deps.TmuxKillSession = func(_ context.Context, name string) error {
		killed = name
		return nil
	}

	invokeTestAction(t, model, action.SessionKill.ID)

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
	setActionConfirm(model.config, action.SessionDelete.ID, false)
	model.filtered = []session.Session{{Name: testSupportSessionArchiveName, Kind: session.KindSaved}}

	deleted := ""

	deps := dependenciesForTest(model)
	deps.DeleteSavedSession = func(_ context.Context, name string) (int, error) {
		deleted = name
		return 1, nil
	}
	deps.SaveModelState = func(*session.State) error { return nil }

	invokeTestAction(t, model, action.SessionDelete.ID)

	if deleted != testSupportSessionArchiveName {
		t.Fatalf("DeleteSavedSession() name = %q, want archive", deleted)
	}

	if model.dialog != dialogNone {
		t.Fatalf("dialog = %v, want none when confirmation is disabled", model.dialog)
	}
}

func TestDeleteSelectedRowMovesSelectionToPreviousEntry(t *testing.T) {
	model := newTestModel()
	setActionConfirm(model.config, action.SessionDelete.ID, false)
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

	trace := invokeTestAction(t, model, action.SessionDelete.ID)
	if len(trace.results) != 1 || trace.results[0].err != nil {
		t.Fatalf("delete results = %+v, want one success", trace.results)
	}

	if model.cursor != 0 {
		t.Fatalf("cursor after refresh = %d, want previous row index 0", model.cursor)
	}

	if len(model.filtered) != 2 {
		t.Fatalf("filtered after refresh = %#v, want two rows", model.filtered)
	}

	if got := model.filtered[model.cursor].Name; got != testSupportSessionAlphaName {
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

	invokeTestAction(t, model, action.SessionDelete.ID)

	if model.dialog != dialogNone {
		t.Fatalf("dialog = %v, want none", model.dialog)
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "attached") {
		t.Fatalf("delete status = (%v, %q), want attached error", model.statusLevel, model.statusMessage)
	}
}

func TestRenameFailureShowsStatus(t *testing.T) {
	model := newTestModel()
	model.displayMode = displayModeAgents
	model.filtered = []session.Session{{Name: "old", Kind: session.KindLive, Agents: []session.AgentSession{{RegistryID: testSupportAgentRegistryID}}}}
	model.cursor = 0
	model.agentCursor = 0

	deps := dependenciesForTest(model)
	deps.TmuxRenameSession = func(_ context.Context, target string, newName string) error {
		return errRenameFailed
	}

	invokeTestAction(t, model, action.SessionRename.ID)
	model.textParamInput.SetValue("new")
	trace := pressTestKey(t, model, keyCode(tea.KeyEnter))

	if len(trace.results) != 1 || trace.results[0].err == nil {
		t.Fatalf("rename results = %+v, want one failure", trace.results)
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "rename failed") {
		t.Fatalf("status = (%v, %q), want rename error", model.statusLevel, model.statusMessage)
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

	trace := invokeTestAction(t, model, action.SnapshotSave.ID)
	if len(trace.results) != 1 || trace.results[0].err != nil || trace.quit {
		t.Fatalf("snapshot trace = %+v, want one success without quit", trace)
	}

	if captured != testSupportSessionDevName {
		t.Fatalf("SnapshotSession() name = %q, want dev", captured)
	}

	if catalogLoads != 1 {
		t.Fatalf("catalog loads = %d, want 1 after snapshot refresh", catalogLoads)
	}

	if model.statusLevel != statusNone {
		t.Fatalf("status = (%v, %q), want cleared after snapshot", model.statusLevel, model.statusMessage)
	}
}

func runSnapshotFailureSubtest(t *testing.T) {
	t.Helper()

	model := newModelWithSnapshotService(Dependencies{
		SnapshotSession: func(context.Context, string) error { return errSnapshotFailed },
	})
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive, Origin: session.OriginLocal}}

	trace := invokeTestAction(t, model, action.SnapshotSave.ID)
	if len(trace.results) != 1 || trace.results[0].err == nil {
		t.Fatalf("snapshot failure trace = %+v, want one failure", trace)
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "snapshot failed") {
		t.Fatalf(
			"snapshot failure status = (%v, %q), want snapshot error",
			model.statusLevel,
			model.statusMessage,
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

func TestActionsWithoutDispatcherReportUnavailable(t *testing.T) {
	model := newTestModelWithDeps(Dependencies{OmitActions: true})
	model.filtered = []session.Session{{Name: testSupportSessionDevName, Kind: session.KindLive, Origin: session.OriginLocal}}

	invokeTestAction(t, model, action.SnapshotSave.ID)

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "no action dispatcher") {
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

	invokeTestAction(t, model, action.SessionCreate.ID)

	if model.dialog != dialogInputPath {
		t.Fatalf("dialog = %v, want directory input", model.dialog)
	}

	edited := filepath.Join(workingDir, "deep", testSupportSessionWorkName)
	model.pathInput.SetValue(edited)

	assertCreatedSessionResult(t, model, edited, &createdName, &createdDir)
}

func assertCreatedSessionResult(t *testing.T, model *Model, edited string, createdName, createdDir *string) {
	t.Helper()

	model.pathSuggestions = nil
	trace := pressTestKey(t, model, keyCode(tea.KeyEnter))

	if len(trace.results) != 1 || trace.results[0].err != nil {
		t.Fatalf("create results = %+v, want one success", trace.results)
	}

	if trace.entered == nil || trace.entered.err != nil || !trace.entered.quit || trace.entered.target.Name != "news" {
		t.Fatalf("entry after create = %+v, want the new session", trace.entered)
	}

	if *createdName != "news" || *createdDir != edited {
		t.Fatalf("created session = %q in %q, want news in %q", *createdName, *createdDir, edited)
	}

	if model.dialog != dialogNone || model.pending != nil {
		t.Fatalf("create dialog state = (%v, %+v), want closed", model.dialog, model.pending)
	}

	if model.textInput.Value() != "news" {
		t.Fatalf("search query after create = %q, want news", model.textInput.Value())
	}
}
