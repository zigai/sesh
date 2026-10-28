package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/session"
)

func localKeyIdentity(name string) session.Identity {
	return session.Identity{Origin: string(session.OriginLocal), Name: name}
}

func keyEntry(name, key string) session.KeyAssignment {
	return session.KeyAssignment{Session: localKeyIdentity(name), Key: key}
}

func defaultSessionKeyRules() sessionKeyRules {
	return newSessionKeyRules(config.DefaultConfig(), testBindings(config.DefaultConfig()))
}

func altSessionKeyConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Keys.SessionPrefix = "alt"

	return cfg
}

func keyOf(entries []session.KeyAssignment, name string) string {
	for _, entry := range entries {
		if entry.Session.Name == name {
			return entry.Key
		}
	}

	return ""
}

func TestAssignSessionKeysPrefersLettersOfTheName(t *testing.T) {
	listed := []session.Identity{localKeyIdentity("web"), localKeyIdentity("wiki"), localKeyIdentity("notes")}

	assigned, changed := assignSessionKeys(nil, listed, defaultSessionKeyRules(), true)
	if !changed {
		t.Fatal("assignSessionKeys() changed = false for new sessions")
	}

	want := map[string]string{"web": "w", "wiki": "i", "notes": "n"}
	for name, key := range want {
		if got := keyOf(assigned, name); got != key {
			t.Errorf("key of %s = %q, want %q", name, got, key)
		}
	}
}

func TestAssignSessionKeysSkipsLettersOfPickerActionsWithAlt(t *testing.T) {
	cfg := altSessionKeyConfig()
	cfg.Keys.Bind = []config.KeyBindConfig{
		{Key: "alt+s", Action: "session.create", In: "picker", Table: "", Params: nil},
		{Key: "alt+c", Action: "session.rename", In: "picker", Table: "", Params: nil},
		{Key: "alt+r", Action: "session.kill", In: "picker", Table: "", Params: nil},
	}

	assigned, _ := assignSessionKeys(nil, []session.Identity{localKeyIdentity("scratch")}, newSessionKeyRules(cfg, testBindings(cfg)), true)

	if got := keyOf(assigned, "scratch"); got != "a" {
		t.Fatalf("key of scratch = %q, want a", got)
	}
}

func TestAssignSessionKeysUsesLettersOfPickerActionsWithTab(t *testing.T) {
	assigned, _ := assignSessionKeys(nil, []session.Identity{localKeyIdentity("scratch")}, defaultSessionKeyRules(), true)

	if got := keyOf(assigned, "scratch"); got != "s" {
		t.Fatalf("key of scratch = %q, want s", got)
	}
}

func TestAssignSessionKeysDoesNotDependOnListOrder(t *testing.T) {
	listed := []session.Identity{localKeyIdentity("web"), localKeyIdentity("wiki"), localKeyIdentity("www")}
	reversed := slices.Clone(listed)
	slices.Reverse(reversed)

	first, _ := assignSessionKeys(nil, listed, defaultSessionKeyRules(), true)
	second, _ := assignSessionKeys(nil, reversed, defaultSessionKeyRules(), true)

	for _, ident := range listed {
		if keyOf(first, ident.Name) != keyOf(second, ident.Name) {
			t.Fatalf("keys differ by list order: %#v vs %#v", first, second)
		}
	}
}

func TestAssignSessionKeysKeepsKeysOfUnlistedSessions(t *testing.T) {
	entries := []session.KeyAssignment{keyEntry("web", "w")}

	assigned, _ := assignSessionKeys(entries, []session.Identity{localKeyIdentity("wiki")}, defaultSessionKeyRules(), false)

	if got := keyOf(assigned, "web"); got != "w" {
		t.Fatalf("unlisted web key = %q, want w kept", got)
	}

	if got := keyOf(assigned, "wiki"); got != "i" {
		t.Fatalf("key of wiki = %q, want i", got)
	}
}

func fullKeyAssignments(rules sessionKeyRules) []session.KeyAssignment {
	entries := make([]session.KeyAssignment, 0, len(sessionKeyLetters))

	for _, letter := range sessionKeyLetters {
		if !rules.blocked[string(letter)] {
			entries = append(entries, keyEntry("held-"+string(letter), string(letter)))
		}
	}

	return entries
}

func TestAssignSessionKeysKeepsUnlistedKeysWhileLettersRemain(t *testing.T) {
	remote := session.Identity{Origin: string(session.OriginSSH), ConnectionID: "box", Name: "remote"}
	entries := []session.KeyAssignment{keyEntry("gone", "g"), {Session: remote, Key: "m"}}

	assigned, changed := assignSessionKeys(entries, nil, defaultSessionKeyRules(), true)
	if changed || !slices.Equal(assigned, entries) {
		t.Fatalf("assignSessionKeys() = %#v, %v, want unlisted keys kept", assigned, changed)
	}
}

func TestAssignSessionKeysReclaimsOldestUnlistedKeyWhenLettersRunOut(t *testing.T) {
	rules := defaultSessionKeyRules()
	entries := fullKeyAssignments(rules)
	listed := make([]session.Identity, 0, len(entries))
	listed = append(listed, localKeyIdentity("new"))

	for _, entry := range entries[1:] {
		listed = append(listed, entry.Session)
	}

	assigned, _ := assignSessionKeys(entries, listed, rules, true)

	if got := keyOf(assigned, "new"); got != entries[0].Key {
		t.Fatalf("key of new = %q, want %q taken back from the unlisted session", got, entries[0].Key)
	}

	if got := keyOf(assigned, entries[0].Session.Name); got != "" {
		t.Fatalf("unlisted session kept key %q after it was taken back", got)
	}
}

func TestAssignSessionKeysReclaimsNothingFromIncompleteListing(t *testing.T) {
	rules := defaultSessionKeyRules()
	entries := fullKeyAssignments(rules)

	assigned, _ := assignSessionKeys(entries, []session.Identity{localKeyIdentity("new")}, rules, false)

	if got := keyOf(assigned, "new"); got != "" {
		t.Fatalf("key of new = %q, want none while the listing is incomplete", got)
	}
}

func TestSavedSessionKeysReplaceShownKeys(t *testing.T) {
	model := newTestModel()
	model.state.SessionKeys = []session.KeyAssignment{keyEntry("web", "w")}

	model.Update(sessionKeysSavedMsg{keys: []session.KeyAssignment{keyEntry("web", "e")}, err: nil})

	if got := model.sessionKey(session.Session{Name: "web", Origin: session.OriginLocal}); got != "e" {
		t.Fatalf("sessionKey(web) = %q, want e from the saved history", got)
	}
}

func TestAssignSessionKeysReportsUnchangedKeys(t *testing.T) {
	entries := []session.KeyAssignment{keyEntry("web", "w")}

	assigned, changed := assignSessionKeys(entries, []session.Identity{localKeyIdentity("web")}, defaultSessionKeyRules(), true)
	if changed || !slices.Equal(assigned, entries) {
		t.Fatalf("assignSessionKeys() = %#v, %v, want entries unchanged", assigned, changed)
	}
}

func TestAssignSessionKeysYieldsToOverrides(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Keys.Sessions = map[string]string{"api": "w", "web": "b"}
	rules := newSessionKeyRules(cfg, testBindings(cfg))

	entries := []session.KeyAssignment{keyEntry("wiki", "w"), keyEntry("web", "e")}
	listed := []session.Identity{localKeyIdentity("wiki"), localKeyIdentity("web"), localKeyIdentity("api")}

	assigned, _ := assignSessionKeys(entries, listed, rules, true)

	if got := keyOf(assigned, "wiki"); got != "i" {
		t.Fatalf("key of wiki = %q, want i after api overrides w", got)
	}

	if keyOf(assigned, "web") != "" || keyOf(assigned, "api") != "" {
		t.Fatalf("overridden sessions recorded keys: %#v", assigned)
	}
}

func TestAssignSessionKeysReassignsKeysTakenByActions(t *testing.T) {
	cfg := altSessionKeyConfig()
	cfg.Keys.Bind = []config.KeyBindConfig{{Key: "alt+w", Action: "session.create", In: "picker"}}

	entries := []session.KeyAssignment{keyEntry("web", "w")}

	assigned, _ := assignSessionKeys(entries, []session.Identity{localKeyIdentity("web")}, newSessionKeyRules(cfg, testBindings(cfg)), true)
	if got := keyOf(assigned, "web"); got != "e" {
		t.Fatalf("key of web = %q, want e once alt+w runs an action", got)
	}
}

func TestAssignSessionKeysFallsBackToAnyLetter(t *testing.T) {
	assigned, _ := assignSessionKeys(nil, []session.Identity{localKeyIdentity("123")}, defaultSessionKeyRules(), true)
	if got := keyOf(assigned, "123"); got != "a" {
		t.Fatalf("key of 123 = %q, want a", got)
	}
}

func TestAssignSessionKeysLeavesSessionsWithoutKeysWhenLettersRunOut(t *testing.T) {
	listed := make([]session.Identity, 0, 30)
	for index := range 30 {
		listed = append(listed, localKeyIdentity(string(rune('a'+index%26))+string(rune('0'+index/26))))
	}

	assigned, _ := assignSessionKeys(nil, listed, defaultSessionKeyRules(), true)

	if want := 26 - len(defaultSessionKeyRules().blocked); len(assigned) != want {
		t.Fatalf("assigned %d keys, want %d", len(assigned), want)
	}
}

func TestSessionKeyOpensSessionHiddenByFilter(t *testing.T) {
	model := newTestModel()
	model.mode = modePopup
	model.state.SessionKeys = []session.KeyAssignment{{Session: session.Identity{Name: "web"}, Key: "w"}}
	model.sessions = []session.Session{{Name: "api", Kind: session.KindLive}, {Name: "web", Kind: session.KindLive}}
	model.filtered = model.sessions[:1]
	holdAltForSessionKeys(model)

	handler := model.handleActionKeys(keyAlt("w"))
	if handler == nil {
		t.Fatal("handleActionKeys(alt+w) returned nil")
	}

	_, cmd := handler()

	model.Update(cmd())

	if model.DeferredSwitchSession() != "web" {
		t.Fatalf("switch target = %q, want web", model.DeferredSwitchSession())
	}
}

func TestSessionKeyRestoresSavedSession(t *testing.T) {
	model := newTestModel()
	model.state.SessionKeys = []session.KeyAssignment{{Session: session.Identity{Name: "archive"}, Key: "v"}}
	model.sessions = []session.Session{{Name: "archive", Kind: session.KindSaved}}
	holdAltForSessionKeys(model)

	deps := dependenciesForTest(model)
	deps.TmuxSessionExists = func(context.Context, string) (bool, error) { return false, nil }

	restored := ""
	deps.RestoreSavedSession = func(_ context.Context, name string) error {
		restored = name
		return nil
	}

	handler := model.handleActionKeys(keyAlt("v"))
	if handler == nil {
		t.Fatal("handleActionKeys(alt+v) returned nil")
	}

	_, cmd := handler()
	runActionTrace(t, model, cmd)

	if restored != "archive" {
		t.Fatalf("restored session = %q, want archive", restored)
	}
}

func TestUnassignedSessionKeyDoesNothing(t *testing.T) {
	model := newTestModel()
	model.sessions = []session.Session{{Name: "web", Kind: session.KindLive}}

	if handler := model.handleActionKeys(keyAlt("w")); handler != nil {
		t.Fatal("handleActionKeys(alt+w) handled a key no session has")
	}
}

func TestSessionLoadAssignsAndPersistsSessionKeys(t *testing.T) {
	model := newTestModel()

	var persisted []session.KeyAssignment

	deps := dependenciesForTest(model)
	deps.SaveModelState = func(st *session.State) error {
		persisted = st.SessionKeys
		return nil
	}

	model.handleSessionLoadResult(sessionLoadResultMsg{
		sessions: []session.Session{{Name: "web", Kind: session.KindLive, Origin: session.OriginLocal, TmuxServerSocket: "/tmp/sock"}},
		status:   session.StatusComplete,
	})

	cmd := model.assignLoadedSessionKeys(true)
	if cmd != nil {
		t.Fatal("assignLoadedSessionKeys() wrote again with unchanged keys")
	}

	if got := model.sessionKey(model.sessions[0]); got != "w" {
		t.Fatalf("sessionKey(web) = %q, want w", got)
	}

	model.state.SessionKeys = nil

	write := model.assignLoadedSessionKeys(true)
	if write == nil {
		t.Fatal("assignLoadedSessionKeys() returned no write for new keys")
	}

	model.Update(write())

	if want := []session.KeyAssignment{keyEntry("web", "w")}; !slices.Equal(persisted, want) {
		t.Fatalf("persisted keys = %#v, want %#v", persisted, want)
	}
}

func TestSessionKeyField(t *testing.T) {
	model := newTestModel()
	model.state.SessionKeys = []session.KeyAssignment{{Session: session.Identity{Name: "web"}, Key: "w"}}

	keyed := sessionFormatValues{model: model, row: session.Session{Name: "web"}}
	if got := keyed.ResolveSessionFormatField(display.FieldSessionKey); got != "w" {
		t.Errorf("session_key = %q, want w", got)
	}

	unkeyed := sessionFormatValues{model: model, row: session.Session{Name: "api"}}
	if got := unkeyed.ResolveSessionFormatField(display.FieldSessionKey); got != "" {
		t.Errorf("session_key without key = %q, want empty", got)
	}
}

func indentKeyModel(t *testing.T, cfg *config.Config) *Model {
	t.Helper()

	cfg.Settings.ShortcutStyle = "indent"
	model := newTestModelWithConfig(t, cfg)
	model.width = 60
	model.height = 10
	model.state.SessionKeys = []session.KeyAssignment{{Session: session.Identity{Name: "web"}, Key: "w"}}
	model.filtered = []session.Session{{Name: "web"}, {Name: "api"}}

	return model
}

func TestIndentShortcutStyleShowsSessionKeysAfterTab(t *testing.T) {
	model := indentKeyModel(t, config.DefaultConfig())

	lines := strings.Split(stripANSI(model.renderList(60, 2)), "\n")
	if !strings.HasPrefix(lines[0], "▌   web") {
		t.Errorf("keyed row before tab = %q, want the selection bar and no key", lines[0])
	}

	pressKey(model, keyCode(tea.KeyTab))

	lines = strings.Split(stripANSI(model.renderList(60, 2)), "\n")
	if !strings.HasPrefix(lines[0], "w   web") {
		t.Errorf("keyed row after tab = %q, want the key in the first column", lines[0])
	}

	if !strings.HasPrefix(lines[1], "    api") {
		t.Errorf("row without key = %q, want a blank first column", lines[1])
	}
}

func TestIndentShortcutStyleShowsAltSessionKeysAlways(t *testing.T) {
	model := indentKeyModel(t, altSessionKeyConfig())

	if line := stripANSI(model.renderList(60, 2)); !strings.HasPrefix(line, "w   web") {
		t.Errorf("keyed row = %q, want key before the name", line)
	}
}

func TestSessionKeysStayHiddenWithoutIndentStyle(t *testing.T) {
	model := newTestModel()
	model.width = 60
	model.height = 10
	model.state.SessionKeys = []session.KeyAssignment{{Session: session.Identity{Name: "web"}, Key: "w"}}
	model.filtered = []session.Session{{Name: "web"}}

	if line := stripANSI(model.renderList(60, 1)); !strings.HasPrefix(line, "▌ web") {
		t.Errorf("row = %q, want no key gutter", line)
	}
}

func TestFooterNamesSessionKeysWhenTheyAreShown(t *testing.T) {
	hasHint := func(model *Model) bool {
		return slices.ContainsFunc(model.footerParts(200), func(part footerPart) bool {
			return part.key == "tab" && part.desc == "open"
		})
	}

	cfg := config.DefaultConfig()
	cfg.Settings.ShortcutStyle = "indent"

	if !hasHint(newTestModelWithConfig(t, cfg)) {
		t.Error("footer has no tab hint with indent shortcut style")
	}

	if hasHint(newTestModel()) {
		t.Error("footer shows tab hint while session keys are hidden")
	}
}
