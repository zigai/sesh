package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestCreateShortcutWithSearchPromptsForCreateDirectory(t *testing.T) {
	model := newTestModel()
	sessionName := "sesh-test-default-dir"
	model.textInput.SetValue(sessionName)

	cwd := t.TempDir()
	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return cwd, nil
	}
	wantDir := filepath.Join(mustUserHomeDir(t), "Projects", sessionName)

	cmdFn := model.invokeAction(action.SessionCreate.ID, nil)
	if cmdFn == nil {
		t.Fatal("invokeAction(session.create) returned nil")
	}

	_, cmd := cmdFn()
	if cmd == nil {
		t.Fatal("handleEnter() returned nil focus command")
	}

	if model.dialog != dialogInputPath {
		t.Fatalf("dialog = %v, want create directory dialog", model.dialog)
	}

	if model.pending == nil || model.pending.values.String("name") != sessionName {
		t.Fatalf("pending create = %+v, want name %q", model.pending, sessionName)
	}

	if got := model.textInput.Value(); got != sessionName {
		t.Fatalf("search input = %q, want original session name", got)
	}

	if got := model.pathInput.Value(); got != wantDir {
		t.Fatalf("create directory input = %q, want default project path", got)
	}

	if got := model.pathInput.Position(); got != len([]rune(wantDir)) {
		t.Fatalf("create directory cursor = %d, want end", got)
	}
}

func TestCreateShortcutWithSearchUsesConfiguredCreateDirectorySuggestion(t *testing.T) {
	model := newTestModel()
	model.config.Settings.DefaultSessionPathSuggestion = "work/{session}"
	model.textInput.SetValue("new-session")

	cwd := t.TempDir()
	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return cwd, nil
	}

	cmdFn := model.invokeAction(action.SessionCreate.ID, nil)
	if cmdFn == nil {
		t.Fatal("invokeAction(session.create) returned nil")
	}

	cmdFn()

	wantDir := filepath.Join(cwd, "work", "new-session")
	if got := model.pathInput.Value(); got != wantDir {
		t.Fatalf("create directory input = %q, want configured suggestion", got)
	}
}

func TestEnterNoMatchDoesNotCreateSession(t *testing.T) {
	model := newTestModel()
	model.textInput.SetValue("new-session")
	model.filtered = nil

	if cmdFn := model.handleEnter(); cmdFn != nil {
		t.Fatal("handleEnter() returned create command, want nil")
	}

	if model.dialog != dialogNone {
		t.Fatalf("dialog = %v, want none", model.dialog)
	}
}

func TestConfiguredCreateShortcutStartsCreateDialog(t *testing.T) {
	model := newTestModel()
	model.config.Keys.Bind = []config.KeyBindConfig{{Key: "ctrl+g", Action: "session.create", In: "picker", Table: "", Params: nil}}
	model.textInput.SetValue("new-session")

	cwd := t.TempDir()
	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return cwd, nil
	}

	cmdFn := model.handleActionKeys(keyCtrl('g'))
	if cmdFn == nil {
		t.Fatal("handleActionKeys(configured create) returned nil")
	}

	cmdFn()

	if model.dialog != dialogInputPath {
		t.Fatalf("dialog = %v, want create directory dialog", model.dialog)
	}
}

func TestConfiguredShortcutMatchesModifiersWrittenInAnyOrder(t *testing.T) {
	model := newTestModel()
	model.config.Keys.Bind = []config.KeyBindConfig{{Key: "alt+ctrl+g", Action: "session.create", In: "picker", Table: "", Params: nil}}
	model.textInput.SetValue("new-session")

	cwd := t.TempDir()
	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return cwd, nil
	}

	cmdFn := model.handleActionKeys(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl | tea.ModAlt}))
	if cmdFn == nil {
		t.Fatal("handleActionKeys(ctrl+alt+g) returned nil, want the binding written as alt+ctrl+g")
	}

	cmdFn()

	if model.dialog != dialogInputPath {
		t.Fatalf("dialog = %v, want create directory dialog", model.dialog)
	}
}

func TestCreateDirDialogEditsDoNotChangeSearchInput(t *testing.T) {
	model := newTestModel()
	model.textInput.SetValue("new-session")

	cwd := t.TempDir()
	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return cwd, nil
	}

	cmdFn := model.invokeAction(action.SessionCreate.ID, nil)
	if cmdFn == nil {
		t.Fatal("invokeAction(session.create) returned nil")
	}

	cmdFn()

	cmdFn = model.handlePathDialogKeys(keyText("x"))
	if cmdFn == nil {
		t.Fatal("handlePathDialogKeys(text) returned nil")
	}

	cmdFn()

	if got := model.textInput.Value(); got != testSupportNewSessionName {
		t.Fatalf("search input after create-dir edit = %q, want unchanged session query", got)
	}

	wantDir := filepath.Join(mustUserHomeDir(t), "Projects", "new-session") + "x"
	if got := model.pathInput.Value(); got != wantDir {
		t.Fatalf("create dir input after edit = %q, want edited directory", got)
	}
}

func TestCreateShortcutShowsErrorWhenCurrentDirectoryFails(t *testing.T) {
	model := newTestModel()
	model.textInput.SetValue("new-session")

	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return "", errWorkingDirectoryFailed
	}

	cmdFn := model.invokeAction(action.SessionCreate.ID, nil)
	if cmdFn == nil {
		t.Fatal("invokeAction(session.create) returned nil")
	}

	cmdFn()

	if model.dialog != dialogNone {
		t.Fatalf("dialog = %v, want none", model.dialog)
	}

	if got := model.textInput.Value(); got != testSupportNewSessionName {
		t.Fatalf("search input = %q, want original session name", got)
	}

	if model.statusLevel != statusError || !strings.Contains(model.statusMessage, "cwd failed") {
		t.Fatalf("status = (%v, %q), want cwd error", model.statusLevel, model.statusMessage)
	}
}

func TestSubmitCreateDirCreatesSessionWithEditedDirectory(t *testing.T) {
	model := newTestModel()
	openTestCreateDirectoryPrompt(model, "new-session")
	model.textInput.SetValue("new-session")
	model.pathInput.SetValue("/work/new")

	dependenciesForTest(model).TmuxSessionExists = func(_ context.Context, name string) (bool, error) {
		return false, nil
	}
	createdName := ""
	createdDir := ""
	dependenciesForTest(model).TmuxCreateSession = func(_ context.Context, name string, path string) error {
		createdName = name
		createdDir = path

		return nil
	}
	dependenciesForTest(model).TmuxIsInsideTmux = func() bool {
		return true
	}
	switched := ""
	dependenciesForTest(model).TmuxSwitchSession = func(_ context.Context, name string) error {
		switched = name
		return nil
	}

	_, cmd := model.handlePathDialogKeys(keyCode(tea.KeyEnter))()
	trace := runActionTrace(t, model, cmd)

	if trace.entered == nil || trace.entered.err != nil || !trace.entered.quit || trace.entered.deferred {
		t.Fatalf("create entry = %+v, want successful quit", trace.entered)
	}

	if createdName != "new-session" || createdDir != "/work/new" {
		t.Fatalf("created session = %q/%q, want new-session /work/new", createdName, createdDir)
	}

	if switched != "new-session" {
		t.Fatalf("switched session = %q, want new-session", switched)
	}

	if model.dialog != dialogNone || model.textInput.Value() != "new-session" {
		t.Fatalf("post-submit dialog/input = %v/%q, want none/original search", model.dialog, model.textInput.Value())
	}
}

func TestSubmitCreateDirAllowsEmptyDirectory(t *testing.T) {
	model := newTestModel()
	openTestCreateDirectoryPrompt(model, "new-session")
	model.textInput.SetValue("new-session")
	model.pathInput.SetValue("")

	dependenciesForTest(model).TmuxSessionExists = func(_ context.Context, name string) (bool, error) {
		return false, nil
	}
	createdDir := "unset"
	dependenciesForTest(model).TmuxCreateSession = func(_ context.Context, name string, path string) error {
		createdDir = path
		return nil
	}
	dependenciesForTest(model).TmuxIsInsideTmux = func() bool {
		return true
	}
	dependenciesForTest(model).TmuxSwitchSession = func(_ context.Context, name string) error {
		return nil
	}

	_, cmd := model.handlePathDialogKeys(keyCode(tea.KeyEnter))()
	trace := runActionTrace(t, model, cmd)

	if trace.entered == nil || trace.entered.err != nil {
		t.Fatalf("create entry = %+v, want success", trace.entered)
	}

	if createdDir != "" {
		t.Fatalf("created dir = %q, want empty string", createdDir)
	}
}

func TestCreateDirDialogCancelKeepsSearch(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{keyCode(tea.KeyEsc), keyCtrl('c')} {
		model := newTestModel()
		openTestCreateDirectoryPrompt(model, "new-session")
		model.textInput.SetValue("new-session")
		model.pathInput.SetValue("/tmp")
		model.pathSuggestions = []string{"/tmp/app/"}

		cmdFn := model.handlePathDialogKeys(key)
		if cmdFn == nil {
			t.Fatal("handlePathDialogKeys(cancel) returned nil")
		}

		cmdFn()

		if model.dialog != dialogNone || model.pending != nil {
			t.Fatalf("dialog = %v pending = %+v, want closed", model.dialog, model.pending)
		}

		if got := model.textInput.Value(); got != testSupportNewSessionName {
			t.Fatalf("text input = %q, want the search kept", got)
		}

		if len(model.pathSuggestions) != 0 {
			t.Fatalf("pathSuggestions = %#v, want cleared", model.pathSuggestions)
		}
	}
}

func TestCreateDirDialogTabCompletesSuggestionAndRefreshesChildren(t *testing.T) {
	model := newTestModel()
	openTestCreateDirectoryPrompt(model, "new-session")
	model.textInput.SetValue("new-session")
	model.pathInput.SetValue("wo")
	model.pathSuggestions = []string{"work" + string(os.PathSeparator)}

	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "work", "api"), 0o755); err != nil {
		t.Fatalf("mkdir work/api failed: %v", err)
	}

	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return cwd, nil
	}

	if cmd := model.handlePathDialogKeys(keyCode(tea.KeyTab)); cmd == nil {
		t.Fatal("handlePathDialogKeys(tab) returned nil")
	} else {
		cmd()
	}

	if got := model.textInput.Value(); got != testSupportNewSessionName {
		t.Fatalf("search input after tab = %q, want unchanged search", got)
	}

	if got := model.pathInput.Value(); got != "work"+string(os.PathSeparator) {
		t.Fatalf("text input after tab = %q, want completed suggestion", got)
	}

	if got := model.pathInput.Position(); got != len([]rune(model.pathInput.Value())) {
		t.Fatalf("cursor after tab = %d, want end", got)
	}

	wantChild := filepath.Join("work", "api") + string(os.PathSeparator)
	if len(model.pathSuggestions) != 1 || model.pathSuggestions[0] != wantChild {
		t.Fatalf("createDirSuggestions after completion = %#v, want %q", model.pathSuggestions, wantChild)
	}
}

func TestCreateDirDialogShiftTabCompletesPreviousSuggestion(t *testing.T) {
	model := newTestModel()
	openTestCreateDirectoryPrompt(model, "new-session")
	model.textInput.SetValue("new-session")
	model.pathInput.SetValue("/work/")
	model.pathSuggestions = []string{"/work/api/", "/work/web/"}

	if cmd := model.handlePathDialogKeys(keyShiftTab()); cmd == nil {
		t.Fatal("handlePathDialogKeys(shift+tab) returned nil")
	} else {
		cmd()
	}

	if got := model.pathInput.Value(); got != "/work/web/" {
		t.Fatalf("text input after shift+tab = %q, want previous suggestion", got)
	}

	if got := model.pathInput.Position(); got != len([]rune(model.pathInput.Value())) {
		t.Fatalf("cursor after shift+tab = %d, want end", got)
	}
}

func TestCreateDirDialogArrowKeysSelectSuggestionAndTabCompletes(t *testing.T) {
	model := newTestModel()
	openTestCreateDirectoryPrompt(model, "new-session")
	model.textInput.SetValue("new-session")
	model.pathInput.SetValue("/work/")
	model.pathSuggestions = []string{"/work/api/", "/work/web/"}

	if cmd := model.handlePathDialogKeys(keyCode(tea.KeyDown)); cmd == nil {
		t.Fatal("handlePathDialogKeys(down) returned nil")
	} else {
		cmd()
	}

	if model.pathSuggestionIndex != 1 {
		t.Fatalf("createDirSuggestionIndex after down = %d, want 1", model.pathSuggestionIndex)
	}

	if got := model.pathInput.Value(); got != "/work/" {
		t.Fatalf("text input after down = %q, want unchanged path", got)
	}

	if cmd := model.handlePathDialogKeys(keyCode(tea.KeyTab)); cmd == nil {
		t.Fatal("handlePathDialogKeys(tab) returned nil")
	} else {
		cmd()
	}

	if got := model.pathInput.Value(); got != "/work/web/" {
		t.Fatalf("text input after tab = %q, want selected suggestion", got)
	}

	if got := model.pathInput.Position(); got != len([]rune(model.pathInput.Value())) {
		t.Fatalf("cursor after tab = %d, want end", got)
	}

	if cmd := model.handlePathDialogKeys(keyCode(tea.KeyUp)); cmd == nil {
		t.Fatal("handlePathDialogKeys(up) returned nil")
	} else {
		cmd()
	}

	if model.pathSuggestionIndex != 0 {
		t.Fatalf("createDirSuggestionIndex after up = %d, want 0", model.pathSuggestionIndex)
	}
}

func TestCreateDirDialogEnterAppliesSelectedSuggestionBeforeSubmit(t *testing.T) {
	model := newTestModel()
	openTestCreateDirectoryPrompt(model, "new-session")
	model.textInput.SetValue("new-session")
	model.pathInput.SetValue("/work/")
	model.pathSuggestions = []string{"/work/api/", "/work/web/"}

	dependenciesForTest(model).TmuxSessionExists = func(_ context.Context, name string) (bool, error) {
		return false, nil
	}
	createdDir := ""
	dependenciesForTest(model).TmuxCreateSession = func(_ context.Context, name string, path string) error {
		createdDir = path

		return nil
	}
	dependenciesForTest(model).TmuxIsInsideTmux = func() bool {
		return true
	}
	dependenciesForTest(model).TmuxSwitchSession = func(_ context.Context, name string) error {
		return nil
	}

	if cmd := model.handlePathDialogKeys(keyCode(tea.KeyDown)); cmd == nil {
		t.Fatal("handlePathDialogKeys(down) returned nil")
	} else {
		cmd()
	}

	_, cmd := model.handlePathDialogKeys(keyCode(tea.KeyEnter))()
	trace := runActionTrace(t, model, cmd)

	if trace.entered == nil || trace.entered.err != nil {
		t.Fatalf("create entry = %+v, want success", trace.entered)
	}

	if createdDir != "/work/web/" {
		t.Fatalf("created dir = %q, want selected suggestion", createdDir)
	}
}

func TestCreateDirDialogSuggestionListScrolls(t *testing.T) {
	model := newTestModel()
	model.width = 90
	model.height = 24
	openTestCreateDirectoryPrompt(model, "new-session")
	model.pathInput.SetValue("/work/")
	model.pathSuggestions = []string{"/work/a/", "/work/b/", "/work/c/", "/work/d/", "/work/e/", "/work/f/", "/work/g/"}

	for range 6 {
		if cmd := model.handlePathDialogKeys(keyCode(tea.KeyDown)); cmd == nil {
			t.Fatal("handlePathDialogKeys(down) returned nil")
		} else {
			cmd()
		}
	}

	if model.pathSuggestionIndex != 6 {
		t.Fatalf("createDirSuggestionIndex = %d, want 6", model.pathSuggestionIndex)
	}

	output := stripANSI(model.renderDialog())
	for _, want := range []string{"/work/c/", "/work/d/", "/work/e/", "/work/f/", "/work/g/"} {
		if !strings.Contains(output, want) {
			t.Fatalf("create dir dialog = %q, expected visible suggestion %q", output, want)
		}
	}

	for _, hidden := range []string{"/work/a/", "/work/b/"} {
		if strings.Contains(output, hidden) {
			t.Fatalf("create dir dialog = %q, should not include scrolled-out suggestion %q", output, hidden)
		}
	}
}

func TestCreateDirDialogSuggestionListUsesConfiguredVisibleCount(t *testing.T) {
	model := newTestModel()
	model.width = 90
	model.height = 24
	model.config.Settings.DirectorySuggestions = 3
	openTestCreateDirectoryPrompt(model, "new-session")
	model.pathInput.SetValue("/work/")
	model.pathSuggestions = []string{"/work/a/", "/work/b/", "/work/c/", "/work/d/", "/work/e/"}

	for range 4 {
		if cmd := model.handlePathDialogKeys(keyCode(tea.KeyDown)); cmd == nil {
			t.Fatal("handlePathDialogKeys(down) returned nil")
		} else {
			cmd()
		}
	}

	output := stripANSI(model.renderDialog())
	for _, want := range []string{"/work/c/", "/work/d/", "/work/e/"} {
		if !strings.Contains(output, want) {
			t.Fatalf("create dir dialog = %q, expected visible suggestion %q", output, want)
		}
	}

	for _, hidden := range []string{"/work/a/", "/work/b/"} {
		if strings.Contains(output, hidden) {
			t.Fatalf("create dir dialog = %q, should not include suggestion outside configured visible count %q", output, hidden)
		}
	}
}

func TestCreateDirDialogHeightIsStableAcrossSuggestionCounts(t *testing.T) {
	model := newTestModel()
	model.width = 90
	model.height = 24
	model.config.Settings.DirectorySuggestions = 4
	openTestCreateDirectoryPrompt(model, "new-session")
	model.pathInput.SetValue("/work/")

	renderedLineCount := func(suggestions []string) int {
		model.pathSuggestions = suggestions
		model.pathSuggestionIndex = 0
		model.pathSuggestionTop = 0

		return len(strings.Split(stripANSI(model.renderDialog()), "\n"))
	}

	noSuggestions := renderedLineCount(nil)
	oneSuggestion := renderedLineCount([]string{"/work/api/"})
	fullSuggestions := renderedLineCount([]string{"/work/api/", "/work/web/", "/work/docs/", "/work/tools/"})

	if noSuggestions != oneSuggestion || oneSuggestion != fullSuggestions {
		t.Fatalf(
			"create dir dialog line counts = no:%d one:%d full:%d, want stable height",
			noSuggestions,
			oneSuggestion,
			fullSuggestions,
		)
	}
}

func TestCreateDirDialogCtrlWDeletesOnePathNode(t *testing.T) {
	model := newTestModel()
	openTestCreateDirectoryPrompt(model, "new-session")
	model.textInput.SetValue("new-session")
	model.pathInput.SetValue("/work/project/api/")

	cwd := t.TempDir()
	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return cwd, nil
	}

	if cmd := model.handlePathDialogKeys(keyCtrl('w')); cmd == nil {
		t.Fatal("handlePathDialogKeys(ctrl+w) returned nil")
	} else {
		cmd()
	}

	if got := model.pathInput.Value(); got != "/work/project/" {
		t.Fatalf("text input after ctrl+w = %q, want one path node deleted", got)
	}
}

func TestRenameDialogUsesCommonShellEditingKeys(t *testing.T) {
	model := newTestModel()
	model.dialog = dialogInputText
	model.textParamInput.SetValue("alpha beta")
	model.textParamInput.Focus()

	if cmd := model.handleTextDialogKeys(keyCtrl('a')); cmd == nil {
		t.Fatal("handleTextDialogKeys(ctrl+a) returned nil")
	} else {
		cmd()
	}

	if cmd := model.handleTextDialogKeys(keyText("X")); cmd == nil {
		t.Fatal("handleTextDialogKeys(rune) returned nil")
	} else {
		cmd()
	}

	if got := model.textParamInput.Value(); got != "Xalpha beta" {
		t.Fatalf("rename input after ctrl+a insert = %q, want Xalpha beta", got)
	}

	if cmd := model.handleTextDialogKeys(keyCtrl('e')); cmd == nil {
		t.Fatal("handleTextDialogKeys(ctrl+e) returned nil")
	} else {
		cmd()
	}

	if cmd := model.handleTextDialogKeys(keyCtrl('w')); cmd == nil {
		t.Fatal("handleTextDialogKeys(ctrl+w) returned nil")
	} else {
		cmd()
	}

	if got := model.textParamInput.Value(); got != "Xalpha " {
		t.Fatalf("rename input after ctrl+w = %q, want deleted previous word", got)
	}
}

func TestRenameDialogCtrlCCancels(t *testing.T) {
	model := newTestModel()
	model.dialog = dialogInputText
	model.textParamInput.SetValue("renamed")
	model.textParamInput.Focus()

	handler := model.handleTextDialogKeys(keyCtrl('c'))
	if handler == nil {
		t.Fatal("handleTextDialogKeys(ctrl+c) returned nil")
	}

	_, cmd := handler()

	if model.dialog != dialogNone || model.textParamInput.Value() != "" {
		t.Fatalf("rename dialog after ctrl+c = %v/%q, want canceled and cleared", model.dialog, model.textParamInput.Value())
	}

	if cmd == nil {
		t.Fatal("rename dialog ctrl+c did not restore search input focus")
	}
}

func TestRenameDialogKeepsSearchInputIndependent(t *testing.T) {
	model := newTestModel()
	model.textInput.SetValue("project")
	model.filtered = []session.Session{{Name: "project", Kind: session.KindLive}}

	handler := model.invokeAction(action.SessionRename.ID, nil)
	if handler == nil {
		t.Fatal("invokeAction(session.rename) returned nil")
	}

	_, focusCmd := handler()
	if focusCmd == nil {
		t.Fatal("invokeAction(session.rename) did not focus the rename input")
	}

	for _, char := range "test-rename" {
		model = updateTestModel(t, model, keyText(string(char)))
	}

	if got := model.textInput.Value(); got != "project" {
		t.Fatalf("search input = %q, want preserved project query", got)
	}

	if got := model.textParamInput.Value(); got != "test-rename" {
		t.Fatalf("rename input = %q, want test-rename", got)
	}

	model = updateTestModel(t, model, keyCode(tea.KeyEsc))
	if model.dialog != dialogNone || model.textInput.Value() != "project" || model.textParamInput.Value() != "" {
		t.Fatalf(
			"rename cancel state = dialog:%v search:%q rename:%q",
			model.dialog,
			model.textInput.Value(),
			model.textParamInput.Value(),
		)
	}
}

func openTestCreateDirectoryPrompt(model *Model, name string) {
	param, _ := action.SessionCreate.Param("dir")
	values := action.Values{}
	values.Set("name", name)

	model.pending = &pendingAction{
		entry:   action.SessionCreate,
		subject: action.Subject{At: action.Local(), Sessions: nil, Pane: ""},
		values:  values,
		param:   param,
	}
	model.dialog = dialogInputPath
}
