package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

	cmdFn := model.handleCreate()
	if cmdFn == nil {
		t.Fatal("handleCreate() returned nil")
	}

	_, cmd := cmdFn()
	if cmd == nil {
		t.Fatal("handleEnter() returned nil focus command")
	}

	if model.dialog != dialogInputCreateDir {
		t.Fatalf("dialog = %v, want create directory dialog", model.dialog)
	}

	if model.createSession != sessionName {
		t.Fatalf("createSession = %q, want %q", model.createSession, sessionName)
	}

	if got := model.textInput.Value(); got != sessionName {
		t.Fatalf("search input = %q, want original session name", got)
	}

	if got := model.createDirInput.Value(); got != wantDir {
		t.Fatalf("create directory input = %q, want default project path", got)
	}

	if got := model.createDirInput.Position(); got != len([]rune(wantDir)) {
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

	cmdFn := model.handleCreate()
	if cmdFn == nil {
		t.Fatal("handleCreate() returned nil")
	}

	cmdFn()

	wantDir := filepath.Join(cwd, "work", "new-session")
	if got := model.createDirInput.Value(); got != wantDir {
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
	model.config.Keys.Create = "ctrl+g"
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

	if model.dialog != dialogInputCreateDir {
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

	cmdFn := model.handleCreate()
	if cmdFn == nil {
		t.Fatal("handleCreate() returned nil")
	}

	cmdFn()

	cmdFn = model.handleCreateDirDialogKeys(keyText("x"))
	if cmdFn == nil {
		t.Fatal("handleCreateDirDialogKeys(text) returned nil")
	}

	cmdFn()

	if got := model.textInput.Value(); got != testSupportNewSessionName {
		t.Fatalf("search input after create-dir edit = %q, want unchanged session query", got)
	}

	wantDir := filepath.Join(mustUserHomeDir(t), "Projects", "new-session") + "x"
	if got := model.createDirInput.Value(); got != wantDir {
		t.Fatalf("create dir input after edit = %q, want edited directory", got)
	}
}

func TestCreateShortcutShowsErrorWhenCurrentDirectoryFails(t *testing.T) {
	model := newTestModel()
	model.textInput.SetValue("new-session")

	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return "", errWorkingDirectoryFailed
	}

	cmdFn := model.handleCreate()
	if cmdFn == nil {
		t.Fatal("handleCreate() returned nil")
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
	model.dialog = dialogInputCreateDir
	model.createSession = "new-session"
	model.textInput.SetValue("new-session")
	model.createDirInput.SetValue("/work/new")

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

	_, cmd := model.submitCreateDir()()
	if cmd == nil {
		t.Fatal("submitCreateDir() returned nil command")
	}

	rawMsg := cmd()

	msg, ok := rawMsg.(sessionActionResultMsg)
	if !ok {
		t.Fatalf("command message type = %T, want sessionActionResultMsg", rawMsg)
	}

	if msg.err != nil ||
		!msg.quit ||
		msg.operation != tuiOperationEnterOrCreateSession ||
		msg.deferred {
		t.Fatalf("create dir msg = %#v, want typed successful quit", msg)
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
	model.dialog = dialogInputCreateDir
	model.createSession = "new-session"
	model.textInput.SetValue("new-session")
	model.createDirInput.SetValue("")

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

	_, cmd := model.submitCreateDir()()
	if cmd == nil {
		t.Fatal("submitCreateDir() returned nil command")
	}

	rawMsg := cmd()

	msg, ok := rawMsg.(sessionActionResultMsg)
	if !ok {
		t.Fatalf("command message type = %T, want sessionActionResultMsg", rawMsg)
	}

	if msg.err != nil || msg.operation != tuiOperationEnterOrCreateSession || msg.deferred {
		t.Fatalf("create dir msg = %#v, want typed success", msg)
	}

	if createdDir != "" {
		t.Fatalf("created dir = %q, want empty string", createdDir)
	}
}

func TestCreateDirDialogCancelRestoresSearch(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{keyCode(tea.KeyEsc), keyCtrl('c')} {
		model := newTestModel()
		model.dialog = dialogInputCreateDir
		model.createSession = "new-session"
		model.textInput.SetValue("old")
		model.createDirInput.SetValue("/tmp")
		model.createDirSuggestions = []string{"/tmp/app/"}

		cmdFn := model.handleCreateDirDialogKeys(key)
		if cmdFn == nil {
			t.Fatal("handleCreateDirDialogKeys(cancel) returned nil")
		}

		cmdFn()

		if model.dialog != dialogNone {
			t.Fatalf("dialog = %v, want none", model.dialog)
		}

		if got := model.textInput.Value(); got != testSupportNewSessionName {
			t.Fatalf("text input = %q, want restored session name", got)
		}

		if len(model.createDirSuggestions) != 0 {
			t.Fatalf("createDirSuggestions = %#v, want cleared", model.createDirSuggestions)
		}
	}
}

func TestCreateDirDialogTabCompletesSuggestionAndRefreshesChildren(t *testing.T) {
	model := newTestModel()
	model.dialog = dialogInputCreateDir
	model.createSession = "new-session"
	model.textInput.SetValue("new-session")
	model.createDirInput.SetValue("wo")
	model.createDirSuggestions = []string{"work" + string(os.PathSeparator)}

	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "work", "api"), 0o755); err != nil {
		t.Fatalf("mkdir work/api failed: %v", err)
	}

	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return cwd, nil
	}

	if cmd := model.handleCreateDirDialogKeys(keyCode(tea.KeyTab)); cmd == nil {
		t.Fatal("handleCreateDirDialogKeys(tab) returned nil")
	} else {
		cmd()
	}

	if got := model.textInput.Value(); got != testSupportNewSessionName {
		t.Fatalf("search input after tab = %q, want unchanged search", got)
	}

	if got := model.createDirInput.Value(); got != "work"+string(os.PathSeparator) {
		t.Fatalf("text input after tab = %q, want completed suggestion", got)
	}

	if got := model.createDirInput.Position(); got != len([]rune(model.createDirInput.Value())) {
		t.Fatalf("cursor after tab = %d, want end", got)
	}

	wantChild := filepath.Join("work", "api") + string(os.PathSeparator)
	if len(model.createDirSuggestions) != 1 || model.createDirSuggestions[0] != wantChild {
		t.Fatalf("createDirSuggestions after completion = %#v, want %q", model.createDirSuggestions, wantChild)
	}
}

func TestCreateDirDialogShiftTabCompletesPreviousSuggestion(t *testing.T) {
	model := newTestModel()
	model.dialog = dialogInputCreateDir
	model.createSession = "new-session"
	model.textInput.SetValue("new-session")
	model.createDirInput.SetValue("/work/")
	model.createDirSuggestions = []string{"/work/api/", "/work/web/"}

	if cmd := model.handleCreateDirDialogKeys(keyShiftTab()); cmd == nil {
		t.Fatal("handleCreateDirDialogKeys(shift+tab) returned nil")
	} else {
		cmd()
	}

	if got := model.createDirInput.Value(); got != "/work/web/" {
		t.Fatalf("text input after shift+tab = %q, want previous suggestion", got)
	}

	if got := model.createDirInput.Position(); got != len([]rune(model.createDirInput.Value())) {
		t.Fatalf("cursor after shift+tab = %d, want end", got)
	}
}

func TestCreateDirDialogArrowKeysSelectSuggestionAndTabCompletes(t *testing.T) {
	model := newTestModel()
	model.dialog = dialogInputCreateDir
	model.createSession = "new-session"
	model.textInput.SetValue("new-session")
	model.createDirInput.SetValue("/work/")
	model.createDirSuggestions = []string{"/work/api/", "/work/web/"}

	if cmd := model.handleCreateDirDialogKeys(keyCode(tea.KeyDown)); cmd == nil {
		t.Fatal("handleCreateDirDialogKeys(down) returned nil")
	} else {
		cmd()
	}

	if model.createDirSuggestionIndex != 1 {
		t.Fatalf("createDirSuggestionIndex after down = %d, want 1", model.createDirSuggestionIndex)
	}

	if got := model.createDirInput.Value(); got != "/work/" {
		t.Fatalf("text input after down = %q, want unchanged path", got)
	}

	if cmd := model.handleCreateDirDialogKeys(keyCode(tea.KeyTab)); cmd == nil {
		t.Fatal("handleCreateDirDialogKeys(tab) returned nil")
	} else {
		cmd()
	}

	if got := model.createDirInput.Value(); got != "/work/web/" {
		t.Fatalf("text input after tab = %q, want selected suggestion", got)
	}

	if got := model.createDirInput.Position(); got != len([]rune(model.createDirInput.Value())) {
		t.Fatalf("cursor after tab = %d, want end", got)
	}

	if cmd := model.handleCreateDirDialogKeys(keyCode(tea.KeyUp)); cmd == nil {
		t.Fatal("handleCreateDirDialogKeys(up) returned nil")
	} else {
		cmd()
	}

	if model.createDirSuggestionIndex != 0 {
		t.Fatalf("createDirSuggestionIndex after up = %d, want 0", model.createDirSuggestionIndex)
	}
}

func TestCreateDirDialogEnterAppliesSelectedSuggestionBeforeSubmit(t *testing.T) {
	model := newTestModel()
	model.dialog = dialogInputCreateDir
	model.createSession = "new-session"
	model.textInput.SetValue("new-session")
	model.createDirInput.SetValue("/work/")
	model.createDirSuggestions = []string{"/work/api/", "/work/web/"}

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

	if cmd := model.handleCreateDirDialogKeys(keyCode(tea.KeyDown)); cmd == nil {
		t.Fatal("handleCreateDirDialogKeys(down) returned nil")
	} else {
		cmd()
	}

	_, cmd := model.handleCreateDirDialogKeys(keyCode(tea.KeyEnter))()
	if cmd == nil {
		t.Fatal("handleCreateDirDialogKeys(enter) returned nil command")
	}

	rawMsg := cmd()

	msg, ok := rawMsg.(sessionActionResultMsg)
	if !ok {
		t.Fatalf("command message type = %T, want sessionActionResultMsg", rawMsg)
	}

	if msg.err != nil {
		t.Fatalf("create dir msg error = %v", msg.err)
	}

	if createdDir != "/work/web/" {
		t.Fatalf("created dir = %q, want selected suggestion", createdDir)
	}
}

func TestCreateDirDialogSuggestionListScrolls(t *testing.T) {
	model := newTestModel()
	model.width = 90
	model.height = 24
	model.dialog = dialogInputCreateDir
	model.createSession = "new-session"
	model.createDirInput.SetValue("/work/")
	model.createDirSuggestions = []string{"/work/a/", "/work/b/", "/work/c/", "/work/d/", "/work/e/", "/work/f/", "/work/g/"}

	for range 6 {
		if cmd := model.handleCreateDirDialogKeys(keyCode(tea.KeyDown)); cmd == nil {
			t.Fatal("handleCreateDirDialogKeys(down) returned nil")
		} else {
			cmd()
		}
	}

	if model.createDirSuggestionIndex != 6 {
		t.Fatalf("createDirSuggestionIndex = %d, want 6", model.createDirSuggestionIndex)
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
	model.dialog = dialogInputCreateDir
	model.createSession = "new-session"
	model.createDirInput.SetValue("/work/")
	model.createDirSuggestions = []string{"/work/a/", "/work/b/", "/work/c/", "/work/d/", "/work/e/"}

	for range 4 {
		if cmd := model.handleCreateDirDialogKeys(keyCode(tea.KeyDown)); cmd == nil {
			t.Fatal("handleCreateDirDialogKeys(down) returned nil")
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
	model.dialog = dialogInputCreateDir
	model.createSession = "new-session"
	model.createDirInput.SetValue("/work/")

	renderedLineCount := func(suggestions []string) int {
		model.createDirSuggestions = suggestions
		model.createDirSuggestionIndex = 0
		model.createDirSuggestionTop = 0

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
	model.dialog = dialogInputCreateDir
	model.createSession = "new-session"
	model.textInput.SetValue("new-session")
	model.createDirInput.SetValue("/work/project/api/")

	cwd := t.TempDir()
	dependenciesForTest(model).GetWorkingDirectory = func() (string, error) {
		return cwd, nil
	}

	if cmd := model.handleCreateDirDialogKeys(keyCtrl('w')); cmd == nil {
		t.Fatal("handleCreateDirDialogKeys(ctrl+w) returned nil")
	} else {
		cmd()
	}

	if got := model.createDirInput.Value(); got != "/work/project/" {
		t.Fatalf("text input after ctrl+w = %q, want one path node deleted", got)
	}
}

func TestRenameDialogUsesCommonShellEditingKeys(t *testing.T) {
	model := newTestModel()
	model.dialog = dialogInputRename
	model.renameInput.SetValue("alpha beta")
	model.renameInput.Focus()

	if cmd := model.handleRenameDialogKeys(keyCtrl('a')); cmd == nil {
		t.Fatal("handleRenameDialogKeys(ctrl+a) returned nil")
	} else {
		cmd()
	}

	if cmd := model.handleRenameDialogKeys(keyText("X")); cmd == nil {
		t.Fatal("handleRenameDialogKeys(rune) returned nil")
	} else {
		cmd()
	}

	if got := model.renameInput.Value(); got != "Xalpha beta" {
		t.Fatalf("rename input after ctrl+a insert = %q, want Xalpha beta", got)
	}

	if cmd := model.handleRenameDialogKeys(keyCtrl('e')); cmd == nil {
		t.Fatal("handleRenameDialogKeys(ctrl+e) returned nil")
	} else {
		cmd()
	}

	if cmd := model.handleRenameDialogKeys(keyCtrl('w')); cmd == nil {
		t.Fatal("handleRenameDialogKeys(ctrl+w) returned nil")
	} else {
		cmd()
	}

	if got := model.renameInput.Value(); got != "Xalpha " {
		t.Fatalf("rename input after ctrl+w = %q, want deleted previous word", got)
	}
}

func TestRenameDialogCtrlCCancels(t *testing.T) {
	model := newTestModel()
	model.dialog = dialogInputRename
	model.renameInput.SetValue("renamed")
	model.renameInput.Focus()

	handler := model.handleRenameDialogKeys(keyCtrl('c'))
	if handler == nil {
		t.Fatal("handleRenameDialogKeys(ctrl+c) returned nil")
	}

	_, cmd := handler()

	if model.dialog != dialogNone || model.renameInput.Value() != "" {
		t.Fatalf("rename dialog after ctrl+c = %v/%q, want canceled and cleared", model.dialog, model.renameInput.Value())
	}

	if cmd == nil {
		t.Fatal("rename dialog ctrl+c did not restore search input focus")
	}
}

func TestRenameDialogKeepsSearchInputIndependent(t *testing.T) {
	model := newTestModel()
	model.textInput.SetValue("project")
	model.filtered = []session.Session{{Name: "project", Kind: session.KindLive}}

	handler := model.handleRename()
	if handler == nil {
		t.Fatal("handleRename() returned nil")
	}

	_, focusCmd := handler()
	if focusCmd == nil {
		t.Fatal("handleRename() did not focus the rename input")
	}

	for _, char := range "test-rename" {
		model = updateTestModel(t, model, keyText(string(char)))
	}

	if got := model.textInput.Value(); got != "project" {
		t.Fatalf("search input = %q, want preserved project query", got)
	}

	if got := model.renameInput.Value(); got != "test-rename" {
		t.Fatalf("rename input = %q, want test-rename", got)
	}

	model = updateTestModel(t, model, keyCode(tea.KeyEsc))
	if model.dialog != dialogNone || model.textInput.Value() != "project" || model.renameInput.Value() != "" {
		t.Fatalf(
			"rename cancel state = dialog:%v search:%q rename:%q",
			model.dialog,
			model.textInput.Value(),
			model.renameInput.Value(),
		)
	}
}
