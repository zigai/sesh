package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display/layout"
	"github.com/zigai/sesh/internal/fields"
	"github.com/zigai/sesh/internal/session"
)

func customFieldTestModel(t *testing.T, seshFormat string, definitions config.FieldCommands) *Model {
	t.Helper()

	cfg := defaultCompactTestConfig()
	cfg.Display.SeshFormat = seshFormat
	cfg.Display.Fields = definitions

	model := newTestModelWithConfig(t, cfg)
	model.displayMode = displayModeSesh

	return model
}

func shellFieldCommand(script string) config.FieldCommand {
	return config.FieldCommand{ShellCommand: script, Command: "", Args: nil, Refresh: ""}
}

func liveTestSession(id, name string) session.Session {
	return session.Session{ID: id, Name: name, Kind: session.KindLive, Origin: session.OriginLocal, WindowCount: 1}
}

func runCustomFieldCommands(t *testing.T, model *Model, command tea.Cmd) int {
	t.Helper()

	results := collectCustomFieldResults(command)
	for _, result := range results {
		updated, next := model.Update(result)
		if updated != model {
			t.Fatalf("Update() returned another model")
		}

		runCustomFieldCommands(t, model, next)
	}

	return len(results)
}

func collectCustomFieldResults(command tea.Cmd) []customFieldResultMsg {
	if command == nil {
		return nil
	}

	switch msg := command().(type) {
	case customFieldResultMsg:
		return []customFieldResultMsg{msg}
	case tea.BatchMsg:
		var results []customFieldResultMsg
		for _, child := range msg {
			results = append(results, collectCustomFieldResults(child)...)
		}

		return results
	default:
		return nil
	}
}

func renderedRows(model *Model) string {
	return model.renderList(80, len(model.filtered)+2)
}

func TestCustomFieldsRunForDisplayedRowsAndRenderColors(t *testing.T) {
	model := customFieldTestModel(t, "{session_name} {custom.probe}", config.FieldCommands{
		"probe": shellFieldCommand(`printf '%s:\033[31m%s\033[0m\n' "$SESH_SESSION" "$SESH_ROW"`),
	})
	model.SetInitialSessions([]session.Session{liveTestSession("$1", "alpha"), liveTestSession("$2", "beta")})

	if before := ansi.Strip(renderedRows(model)); strings.Contains(before, "alpha:session") {
		t.Fatalf("value rendered before the command ran: %q", before)
	}

	if runs := runCustomFieldCommands(t, model, model.syncCustomFields()); runs != 2 {
		t.Fatalf("runs = %d, want one per row", runs)
	}

	rendered := renderedRows(model)
	if plain := ansi.Strip(rendered); !strings.Contains(plain, "alpha alpha:session") || !strings.Contains(plain, "beta beta:session") {
		t.Fatalf("rendered rows = %q", plain)
	}

	if !strings.Contains(rendered, "\x1b[31msession\x1b[0m") {
		t.Fatalf("rendered rows lost the field color: %q", rendered)
	}
}

func TestCustomFieldsRerunOnlyChangedRows(t *testing.T) {
	model := customFieldTestModel(t, "{session_name} {custom.windows}", config.FieldCommands{
		"windows": shellFieldCommand(`printf '%s' "$(cat | sed 's/.*"windowCount":\([0-9]*\).*/\1/')"`),
	})
	alpha, beta := liveTestSession("$1", "alpha"), liveTestSession("$2", "beta")
	model.SetInitialSessions([]session.Session{alpha, beta})

	if runs := runCustomFieldCommands(t, model, model.syncCustomFields()); runs != 2 {
		t.Fatalf("initial runs = %d", runs)
	}

	model.refreshDisplaySessions()

	if command := model.syncCustomFields(); command != nil {
		t.Fatal("unchanged rows started new runs")
	}

	beta.WindowCount = 4
	model.SetInitialSessions([]session.Session{alpha, beta})

	if runs := runCustomFieldCommands(t, model, model.syncCustomFields()); runs != 1 {
		t.Fatalf("runs after one row changed = %d, want 1", runs)
	}

	if plain := ansi.Strip(renderedRows(model)); !strings.Contains(plain, "beta 4") || !strings.Contains(plain, "alpha 1") {
		t.Fatalf("rendered rows = %q", plain)
	}
}

func TestCustomFieldsDropResultsFromReplacedRuns(t *testing.T) {
	model := customFieldTestModel(t, "{session_name} {custom.windows}", config.FieldCommands{
		"windows": shellFieldCommand(`printf 'w%s' "$(cat | sed 's/.*"windowCount":\([0-9]*\).*/\1/')"`),
	})
	row := liveTestSession("$1", "alpha")
	model.SetInitialSessions([]session.Session{row})

	stale := collectCustomFieldResults(model.syncCustomFields())

	row.WindowCount = 7
	model.SetInitialSessions([]session.Session{row})
	fresh := collectCustomFieldResults(model.syncCustomFields())

	for _, result := range append(fresh, stale...) {
		model.Update(result)
	}

	if plain := ansi.Strip(renderedRows(model)); !strings.Contains(plain, "alpha w7") {
		t.Fatalf("rendered rows = %q, want the replacing run's value", plain)
	}
}

func TestCustomFieldsDescribeRemoteSavedAndAgentRows(t *testing.T) {
	model := customFieldTestModel(t, "{session_name} {custom.origin}", config.FieldCommands{
		"origin": shellFieldCommand(`printf '%s/%s/%s/%s' "$SESH_ROW" "$SESH_SESSION_KIND" "$SESH_ORIGIN" "$SESH_CONNECTION_ID$SESH_AGENT_CWD"`),
	})
	model.config.Display.AgentsSessionFormat = "{session_name} {custom.origin}"

	remote := liveTestSession("$5", "edge")
	remote.Origin = session.OriginSSH
	remote.RuntimeIdentity.ConnectionID = "work"
	saved := session.Session{ID: "s1", Name: "archive", Kind: session.KindSaved, Origin: session.OriginLocal, PathRaw: []byte("/srv/archive")}

	model.SetInitialSessions([]session.Session{remote, saved})
	runCustomFieldCommands(t, model, model.syncCustomFields())

	plain := ansi.Strip(renderedRows(model))
	if !strings.Contains(plain, "edge session/live/ssh/work") || !strings.Contains(plain, "archive session/saved/local/") {
		t.Fatalf("rendered rows = %q", plain)
	}

	cfg := defaultCompactTestConfig()
	cfg.Display.AgentsAgentFormat = "{tree}{harness} {custom.origin}"
	cfg.Display.Fields = config.FieldCommands{"origin": model.config.Display.Fields["origin"]}
	agents := newTestModelWithConfig(t, cfg)
	agents.displayMode = displayModeAgents

	host := liveTestSession("$9", "host")
	host.Agents = []session.AgentSession{{Harness: "claude", PaneID: "%4", CWD: "/srv/app", Status: session.AgentStatusIdle}}
	agents.SetInitialSessions([]session.Session{host})
	runCustomFieldCommands(t, agents, agents.syncCustomFields())

	if got := ansi.Strip(agents.FormatAgentRow(host, host.Agents[0])); !strings.Contains(got, "agent/live/local//srv/app") {
		t.Fatalf("agent row = %q", got)
	}
}

func TestCustomFieldFailureKeepsPreviousValue(t *testing.T) {
	failFlag := filepath.Join(t.TempDir(), "fail")
	model := customFieldTestModel(t, "{session_name} {custom.flaky}", config.FieldCommands{
		"flaky": shellFieldCommand(`if [ -e ` + failFlag + ` ]; then echo broken >&2; exit 2; fi; printf 'v%s' "$(cat | sed 's/.*"windowCount":\([0-9]*\).*/\1/')"`),
	})
	row := liveTestSession("$1", "alpha")
	model.SetInitialSessions([]session.Session{row})
	runCustomFieldCommands(t, model, model.syncCustomFields())

	if err := os.WriteFile(failFlag, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	row.WindowCount = 3
	model.SetInitialSessions([]session.Session{row})
	runCustomFieldCommands(t, model, model.syncCustomFields())

	if plain := ansi.Strip(renderedRows(model)); !strings.Contains(plain, "alpha v1") {
		t.Fatalf("rendered rows = %q, want the previous value", plain)
	}

	values := sessionFormatValues{model: model, row: model.displaySessions[0]}
	if _, state := values.resolveCustomCell("custom.flaky"); state != layout.Pending {
		t.Fatalf("state after failure = %v, want pending", state)
	}
}

func TestCustomFieldCacheShowsPreviousValueBeforeRunAndIsRewritten(t *testing.T) {
	model := customFieldTestModel(t, "{session_name} {custom.note}", config.FieldCommands{
		"note": shellFieldCommand(`printf fresh`),
	})
	row := liveTestSession("$1", "alpha")

	input := customFieldSessionContext(row)
	input.Field = "note"

	changeKey, err := input.ChangeKey()
	if err != nil {
		t.Fatal(err)
	}

	rowKey := customFieldSessionRowKey(row)
	if err := fields.MergeCache(t.Context(), fields.CachePath("note"), map[string]fields.CacheEntry{rowKey: {ChangeKey: changeKey, Text: "cached"}}); err != nil {
		t.Fatal(err)
	}

	model.seedCustomFieldCaches(readCustomFieldCaches(model.customFieldNames()))
	model.SetInitialSessions([]session.Session{row})
	command := model.syncCustomFields()

	if plain := ansi.Strip(renderedRows(model)); !strings.Contains(plain, "alpha cached") {
		t.Fatalf("first frame = %q, want cached value", plain)
	}

	runCustomFieldCommands(t, model, command)

	if plain := ansi.Strip(renderedRows(model)); !strings.Contains(plain, "alpha fresh") {
		t.Fatalf("after run = %q", plain)
	}

	entries, err := fields.ReadCache(fields.CachePath("note"))
	if err != nil || entries[rowKey].Text != "fresh" || entries[rowKey].ChangeKey != changeKey {
		t.Fatalf("cache after run = %#v, %v", entries, err)
	}
}

func TestCustomFieldRefreshTickRerunsRows(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "count")
	command := shellFieldCommand(`echo run >> ` + counter + `; wc -l < ` + counter + ` | tr -d ' '`)
	command.Refresh = "1ms"

	model := customFieldTestModel(t, "{session_name} {custom.count}", config.FieldCommands{"count": command})
	model.SetInitialSessions([]session.Session{liveTestSession("$1", "alpha")})
	runCustomFieldCommands(t, model, model.syncCustomFields())

	_, next := model.Update(customFieldTickMsg{field: "count"})
	if runs := runCustomFieldCommandsSkippingTicks(t, model, next); runs != 1 {
		t.Fatalf("runs after tick = %d", runs)
	}

	if plain := ansi.Strip(renderedRows(model)); !strings.Contains(plain, "alpha 2") {
		t.Fatalf("rendered rows = %q", plain)
	}
}

func runCustomFieldCommandsSkippingTicks(t *testing.T, model *Model, command tea.Cmd) int {
	t.Helper()

	batch, ok := command().(tea.BatchMsg)
	if !ok {
		t.Fatalf("tick did not return a batch")
	}

	runs := 0
	for _, child := range batch[1:] {
		runs += runCustomFieldCommands(t, model, child)
	}

	return runs
}

func TestCustomFieldCompletedValueSurvivesCloseWithPendingRow(t *testing.T) {
	dir := t.TempDir()

	readyPath, blockedPath := filepath.Join(dir, "ready"), filepath.Join(dir, "blocked")
	ready := customFieldFIFO(t, readyPath)
	customFieldFIFO(t, blockedPath)

	model := customFieldTestModel(t, "{session_name} {custom.partial}", config.FieldCommands{
		"partial": shellFieldCommand("if [ \"$SESH_SESSION\" = second ]; then printf 'ready\\n' > " + readyPath + "; read blocked < " + blockedPath + "; else printf completed; fi"),
	})
	first, second := liveTestSession("$1", "first"), liveTestSession("$2", "second")
	model.SetInitialSessions([]session.Session{first, second})

	batch, ok := model.syncCustomFields()().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatal("expected commands for both displayed rows")
	}

	finished := make(chan struct{})

	t.Cleanup(func() {
		model.Close()
		<-finished
	})

	go func() {
		batch[1]()
		close(finished)
	}()

	if err := ready.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	if line, err := bufio.NewReader(ready).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("unfinished command readiness = %q, %v", line, err)
	}

	result, ok := batch[0]().(customFieldResultMsg)
	if !ok {
		t.Fatal("completed command did not return its result")
	}

	model.Update(result)
	model.Close()
	<-finished

	entries, err := fields.ReadCache(fields.CachePath("partial"))
	if err != nil || entries[customFieldSessionRowKey(first)].Text != "completed" {
		t.Fatalf("completed value after close = %#v, %v", entries, err)
	}
}

func customFieldFIFO(t *testing.T, path string) *os.File {
	t.Helper()

	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	// macOS does not poll a FIFO opened by path, so read deadlines need a nonblocking descriptor.
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}

	file := os.NewFile(uintptr(fd), path)

	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})

	return file
}

func TestCustomFieldLatePublicationCannotReplaceNewerValue(t *testing.T) {
	model := customFieldTestModel(t, "{session_name} {custom.ordered}", config.FieldCommands{
		"ordered": shellFieldCommand("printf '%s' \"$SESH_SESSION\""),
	})
	row := liveTestSession("$1", "older")
	model.SetInitialSessions([]session.Session{row})
	oldResult := collectCustomFieldResults(model.syncCustomFields())[0]
	oldResult.at = time.Unix(100, 0)
	_, olderWrite := model.Update(oldResult)

	row.Name = "newer"
	model.SetInitialSessions([]session.Session{row})
	newResult := collectCustomFieldResults(model.syncCustomFields())[0]
	newResult.at = time.Unix(200, 0)
	_, newerWrite := model.Update(newResult)
	runCustomFieldCommands(t, model, newerWrite)
	runCustomFieldCommands(t, model, olderWrite)

	entries, err := fields.ReadCache(fields.CachePath("ordered"))
	if err != nil || entries[newResult.key.row].Text != "newer" {
		t.Fatalf("value after late older publication = %#v, %v", entries, err)
	}
}

func TestCustomFieldPopupsPreserveEachOthersCompletedEntries(t *testing.T) {
	definitions := config.FieldCommands{"shared": shellFieldCommand("printf '%s' \"$SESH_SESSION\"")}
	first := customFieldTestModel(t, "{session_name} {custom.shared}", definitions)
	second := customFieldTestModel(t, "{session_name} {custom.shared}", definitions)
	firstRow, secondRow := liveTestSession("$1", "first"), liveTestSession("$2", "second")
	first.SetInitialSessions([]session.Session{firstRow})
	second.SetInitialSessions([]session.Session{secondRow})
	runCustomFieldCommands(t, first, first.syncCustomFields())
	runCustomFieldCommands(t, second, second.syncCustomFields())
	first.Close()
	second.Close()

	entries, err := fields.ReadCache(fields.CachePath("shared"))
	if err != nil || entries[customFieldSessionRowKey(firstRow)].Text != "first" || entries[customFieldSessionRowKey(secondRow)].Text != "second" {
		t.Fatalf("shared cache = %#v, %v", entries, err)
	}
}

func TestCustomFieldOlderPopupResultCannotReplaceNewerEntry(t *testing.T) {
	older := customFieldTestModel(t, "{session_name} {custom.current}", config.FieldCommands{"current": shellFieldCommand("printf older")})
	newer := customFieldTestModel(t, "{session_name} {custom.current}", config.FieldCommands{"current": shellFieldCommand("printf newer")})
	row := liveTestSession("$1", "alpha")
	older.SetInitialSessions([]session.Session{row})
	newer.SetInitialSessions([]session.Session{row})

	oldResult := collectCustomFieldResults(older.syncCustomFields())[0]
	newResult := collectCustomFieldResults(newer.syncCustomFields())[0]
	oldResult.at, newResult.at = time.Unix(100, 0), time.Unix(200, 0)
	_, next := newer.Update(newResult)
	runCustomFieldCommands(t, newer, next)
	_, next = older.Update(oldResult)
	runCustomFieldCommands(t, older, next)

	entries, err := fields.ReadCache(fields.CachePath("current"))
	if err != nil || entries[newResult.key.row].Text != "newer" {
		t.Fatalf("cache after older popup finished = %#v, %v", entries, err)
	}
}

func TestCustomFieldSavedRowsRemainDistinctAcrossOriginsAndRepositories(t *testing.T) {
	model := customFieldTestModel(t, "{session_name} {custom.identity}", config.FieldCommands{
		"identity": shellFieldCommand("printf '%s:%s' \"$SESH_ORIGIN\" \"$SESH_CONNECTION_ID\""),
	})
	rows := []session.Session{
		{Name: "archive", Kind: session.KindSaved, Origin: session.OriginLocal, RepositorySource: "one"},
		{Name: "archive", Kind: session.KindSaved, Origin: session.OriginLocal, RepositorySource: "two"},
		{Name: "archive", Kind: session.KindSaved, Origin: session.OriginSSH, RuntimeIdentity: session.RuntimeIdentity{ConnectionID: "east"}},
		{Name: "archive", Kind: session.KindSaved, Origin: session.OriginSSH, RuntimeIdentity: session.RuntimeIdentity{ConnectionID: "west"}},
	}
	model.SetInitialSessions(rows)

	if runs := runCustomFieldCommands(t, model, model.syncCustomFields()); runs != 4 {
		t.Fatalf("saved row runs = %d, want 4", runs)
	}

	want := []string{"local:", "local:", "ssh:east", "ssh:west"}
	for index, row := range rows {
		if got := ansi.Strip(model.FormatSessionRow(row)); !strings.Contains(got, "archive "+want[index]) {
			t.Errorf("saved row %d = %q", index, got)
		}
	}

	entries, err := fields.ReadCache(fields.CachePath("identity"))
	if err != nil || len(entries) != 4 {
		t.Fatalf("saved cache = %#v, %v, want 4 distinct entries", entries, err)
	}

	reopened := customFieldTestModel(t, "{session_name} {custom.identity}", model.config.Display.Fields)
	reopened.seedCustomFieldCaches(readCustomFieldCaches(reopened.customFieldNames()))
	reopened.SetInitialSessions(rows)
	reopened.syncCustomFields()

	for index, row := range rows {
		if got := ansi.Strip(reopened.FormatSessionRow(row)); !strings.Contains(got, "archive "+want[index]) {
			t.Errorf("cached saved row %d = %q", index, got)
		}
	}
}

func TestCustomFieldStylesEndAtSessionAndAgentBoundaries(t *testing.T) {
	for _, agentRow := range []bool{false, true} {
		for _, selected := range []bool{false, true} {
			t.Run(fmt.Sprintf("agent=%t/selected=%t", agentRow, selected), func(t *testing.T) {
				cfg := defaultCompactTestConfig()
				cfg.Display.SeshFormat = "{session_name} {custom.color} {window_count}"
				cfg.Display.AgentsAgentFormat = "{harness} {custom.color} {agent_pid}"
				cfg.Display.Fields = config.FieldCommands{"color": shellFieldCommand("printf '\\033[31;1mX'")}
				model := newTestModelWithConfig(t, cfg)
				model.displayMode = displayModeSesh
				row := liveTestSession("$1", "alpha")
				row.WindowCount = 7
				agent := session.AgentSession{Harness: "codex", PaneID: "%1", PID: 7, Status: session.AgentStatusIdle}

				if agentRow {
					model.displayMode = displayModeAgents
					row.Agents = []session.AgentSession{agent}
				}

				model.SetInitialSessions([]session.Session{row})
				runCustomFieldCommands(t, model, model.syncCustomFields())
				table := model.calcSessionLayout(80)

				var (
					values sessionFormatValues
					output strings.Builder
				)
				model.writeFormattedSessionRow(&output, table, model.sessionRowStyles(table), row, agent, agentRow, false, selected, &values)

				beforeContent := "a"
				if agentRow {
					beforeContent = "C"
				}

				before := styledFieldPen(t, output.String(), beforeContent).Style()
				field := styledFieldPen(t, output.String(), "X").Style()

				after := styledFieldPen(t, output.String(), "7").Style()
				if field.Equal(&before) {
					t.Fatal("custom field lost its own style")
				}

				if !after.Equal(&before) {
					t.Fatalf("following field style = %+v, surrounding row style = %+v; row %q", after, before, output.String())
				}
			})
		}
	}
}

func styledFieldPen(t *testing.T, text, content string) *lipgloss.WrapWriter {
	t.Helper()

	pen := lipgloss.NewWrapWriter(io.Discard)

	t.Cleanup(func() {
		if err := pen.Close(); err != nil {
			t.Error(err)
		}
	})

	var state byte

	for text != "" {
		sequence, _, consumed, nextState := ansi.DecodeSequence(text, state, nil)
		state, text = nextState, text[consumed:]

		if _, err := io.WriteString(pen, sequence); err != nil {
			t.Fatal(err)
		}

		if sequence == content {
			return pen
		}
	}

	t.Fatalf("rendered row has no %q cell", content)

	return nil
}

func TestCustomFieldMissingCommandFailsModel(t *testing.T) {
	cfg := defaultCompactTestConfig()
	cfg.Display.SeshFormat = "{session_name} {custom.weather}"
	cfg.Display.Fields = config.FieldCommands{
		"weather": {ShellCommand: "", Command: filepath.Join(t.TempDir(), "weather"), Args: nil, Refresh: ""},
	}

	_, err := newModel(cfg, testState())
	if err == nil || !strings.Contains(err.Error(), "display.fields.weather") {
		t.Fatalf("newModel() error = %v, want missing command naming the field", err)
	}
}
