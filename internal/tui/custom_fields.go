package tui

import (
	"context"
	"errors"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
	"github.com/zigai/sesh/internal/fields"
	"github.com/zigai/sesh/internal/session"
)

var errCustomFieldCommandNotFound = errors.New("custom field command is not an executable file on PATH")

type customFieldKey struct {
	field string
	row   string
}

type customFieldCell struct {
	request   uint64
	cancel    context.CancelFunc
	changeKey string
	text      string
	state     layout.State
}

type customFieldState struct {
	commands    map[string]config.FieldCommand
	cells       map[customFieldKey]*customFieldCell
	cache       map[string]map[string]fields.CacheEntry
	unsaved     map[string]map[string]fields.CacheEntry
	writeErrors map[string]error
	ticking     map[string]bool
	requests    uint64
	dirty       bool
}

type customFieldTarget struct {
	key       customFieldKey
	input     fields.Context
	changeKey string
}

type customFieldResultMsg struct {
	key       customFieldKey
	request   uint64
	changeKey string
	result    fields.Result
	at        time.Time
}

type customFieldTickMsg struct {
	field string
}

func newCustomFieldState(formats sessionRowFormats, definitions config.FieldCommands) (customFieldState, error) {
	names := slices.Concat(
		formats.seshSession.CustomFields(),
		formats.agentsSession.CustomFields(),
		formats.agentsAgent.CustomFields(),
	)

	commands := make(map[string]config.FieldCommand, len(names))

	for _, name := range names {
		command := definitions[name]
		if command.Command != "" {
			if _, err := exec.LookPath(command.Command); err != nil {
				return customFieldState{}, oops.In("tui").
					Code("custom_field_invalid").
					With("operation", "resolve_custom_field", "field", name).
					Wrapf(errCustomFieldCommandNotFound, "display.fields.%s command %q", name, command.Command)
			}
		}

		commands[name] = command
	}

	return customFieldState{
		commands:    commands,
		cells:       make(map[customFieldKey]*customFieldCell),
		cache:       make(map[string]map[string]fields.CacheEntry),
		unsaved:     make(map[string]map[string]fields.CacheEntry),
		writeErrors: make(map[string]error),
		ticking:     make(map[string]bool),
		requests:    0,
		dirty:       false,
	}, nil
}

func (m *Model) customFieldNames() []string {
	return slices.Sorted(maps.Keys(m.customFields.commands))
}

func readCustomFieldCaches(names []string) map[string]map[string]fields.CacheEntry {
	caches := make(map[string]map[string]fields.CacheEntry, len(names))

	for _, name := range names {
		entries, err := fields.ReadCache(fields.CachePath(name))
		if err == nil && entries != nil {
			caches[name] = entries
		}
	}

	return caches
}

func (m *Model) seedCustomFieldCaches(caches map[string]map[string]fields.CacheEntry) {
	for name, entries := range caches {
		if _, used := m.customFields.commands[name]; used {
			m.customFields.cache[name] = entries
		}
	}
}

func (m *Model) markCustomFieldsDirty() {
	m.customFields.dirty = len(m.customFields.commands) > 0
}

func (m *Model) syncCustomFields() tea.Cmd {
	if !m.customFields.dirty {
		return nil
	}

	m.customFields.dirty = false

	targets := m.customFieldTargets()
	current := make(map[customFieldKey]struct{}, len(targets))
	commands := make([]tea.Cmd, 0, len(targets))

	for _, target := range targets {
		current[target.key] = struct{}{}

		cell, ok := m.customFields.cells[target.key]
		if !ok {
			cell = m.seededCustomFieldCell(target)
			m.customFields.cells[target.key] = cell
		} else if cell.changeKey == target.changeKey {
			continue
		}

		commands = append(commands, m.startCustomFieldRun(cell, target))
	}

	for key, cell := range m.customFields.cells {
		if _, ok := current[key]; !ok {
			cancelCustomFieldRun(cell)
			delete(m.customFields.cells, key)
		}
	}

	for _, name := range m.customFieldNames() {
		if !m.customFields.ticking[name] && m.customFields.commands[name].Refresh != "" {
			m.customFields.ticking[name] = true
			commands = append(commands, m.customFieldTickCommand(name))
		}
	}

	return tea.Batch(commands...)
}

func (m *Model) seededCustomFieldCell(target customFieldTarget) *customFieldCell {
	cell := &customFieldCell{request: 0, cancel: nil, changeKey: "", text: "", state: layout.Pending}

	if entry, ok := m.customFields.cache[target.key.field][target.key.row]; ok && entry.ChangeKey == target.changeKey {
		cell.text = entry.Text
	}

	return cell
}

func (m *Model) startCustomFieldRun(cell *customFieldCell, target customFieldTarget) tea.Cmd {
	cancelCustomFieldRun(cell)

	m.customFields.requests++
	request := m.customFields.requests
	ctx, cancel := context.WithCancel(m.operationContext())

	cell.request, cell.cancel, cell.changeKey = request, cancel, target.changeKey
	if cell.state != layout.Present {
		cell.state = layout.Pending
	}

	command := m.customFields.commands[target.key.field]
	key, input, changeKey := target.key, target.input, target.changeKey

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		result := fields.Run(ctx, command, input)

		return customFieldResultMsg{key: key, request: request, changeKey: changeKey, result: result, at: time.Now()}
	}
}

func cancelCustomFieldRun(cell *customFieldCell) {
	if cell.cancel != nil {
		cell.cancel()
		cell.cancel = nil
	}
}

func (m *Model) handleCustomFieldResult(msg customFieldResultMsg) {
	cell, ok := m.customFields.cells[msg.key]
	if !ok || cell.request != msg.request {
		return
	}

	cancelCustomFieldRun(cell)

	if msg.result.Failed() {
		if !errors.Is(msg.result.Err, context.Canceled) {
			logCustomFieldFailure(m.operationContext(), msg.result)
		}

		cell.state = layout.Pending

		return
	}

	cell.text = SanitizeTerminalTextKeepSGR(msg.result.Text)

	cell.state = layout.Absent
	if cell.text != "" {
		cell.state = layout.Present
	}

	entries := m.customFields.cache[msg.key.field]
	if entries == nil {
		entries = make(map[string]fields.CacheEntry)
		m.customFields.cache[msg.key.field] = entries
	}

	entry := fields.CacheEntry{ChangeKey: msg.changeKey, Text: cell.text, At: msg.at}
	entries[msg.key.row] = entry

	updates := m.customFields.unsaved[msg.key.field]
	if updates == nil {
		updates = make(map[string]fields.CacheEntry)
		m.customFields.unsaved[msg.key.field] = updates
	}

	updates[msg.key.row] = entry
	m.saveCustomFieldCache(msg.key.field)
}

func (m *Model) saveCustomFieldCache(name string) {
	updates := m.customFields.unsaved[name]
	if len(updates) == 0 {
		return
	}

	ctx := context.WithoutCancel(m.operationContext())
	if err := fields.MergeCache(ctx, fields.CachePath(name), updates); err != nil {
		if m.customFields.writeErrors[name] == nil {
			zerolog.Ctx(ctx).Error().
				Str("component", "tui").
				Str("operation", "custom_field_cache_write").
				Str("status", "failed").
				Str("reason", "cache_write_failed").
				Str("error_code", classifyTUIErrorCode(err)).
				Msg("custom field cache write failed")
		}

		m.customFields.writeErrors[name] = oops.In("tui").
			Code("custom_field_cache_write_failed").
			With("operation", "custom_field_cache_write").
			Wrap(err)

		return
	}

	delete(m.customFields.unsaved, name)
	delete(m.customFields.writeErrors, name)
}

func (m *Model) flushCustomFieldCaches() {
	for _, name := range m.customFieldNames() {
		m.saveCustomFieldCache(name)
	}
}

func (m *Model) customFieldTickCommand(name string) tea.Cmd {
	refresh, err := time.ParseDuration(m.customFields.commands[name].Refresh)
	if err != nil || refresh <= 0 {
		return nil
	}

	return tea.Tick(refresh, func(time.Time) tea.Msg { return customFieldTickMsg{field: name} })
}

func (m *Model) handleCustomFieldTick(msg customFieldTickMsg) tea.Cmd {
	commands := []tea.Cmd{m.customFieldTickCommand(msg.field)}

	for _, target := range m.customFieldTargets() {
		if target.key.field != msg.field {
			continue
		}

		cell, ok := m.customFields.cells[target.key]
		if !ok || cell.cancel != nil {
			continue
		}

		commands = append(commands, m.startCustomFieldRun(cell, target))
	}

	return tea.Batch(commands...)
}

func (m *Model) customFieldTargets() []customFieldTarget {
	sessionFields, agentFields := m.sessionFormats.seshSession.CustomFields(), []string(nil)
	switch m.displayMode {
	case displayModeAgents:
		sessionFields, agentFields = m.sessionFormats.agentsSession.CustomFields(), m.sessionFormats.agentsAgent.CustomFields()
	case displayModeWork:
		sessionFields, agentFields = m.sessionFormats.workSection.CustomFields(), m.sessionFormats.workTask.CustomFields()
	case displayModeSesh:
	}

	var targets []customFieldTarget

	for _, row := range m.displaySessions {
		sessionInput := customFieldSessionContext(row)

		targets = appendCustomFieldTargets(targets, sessionFields, customFieldSessionRowKey(row), sessionInput)

		if len(agentFields) == 0 {
			continue
		}

		for _, agent := range row.Agents {
			input := sessionInput
			input.Row = fields.RowAgent
			input.Agent = customFieldAgentContext(agent)

			targets = appendCustomFieldTargets(targets, agentFields, customFieldAgentRowKey(row, agent), input)
		}
	}

	return targets
}

func appendCustomFieldTargets(targets []customFieldTarget, names []string, row string, input fields.Context) []customFieldTarget {
	for _, name := range names {
		input.Field = name

		changeKey, err := input.ChangeKey()
		if err != nil {
			continue
		}

		targets = append(targets, customFieldTarget{key: customFieldKey{field: name, row: row}, input: input, changeKey: changeKey})
	}

	return targets
}

func customFieldSessionRowKey(row session.Session) string {
	if row.Saved() {
		return row.EntityKey()
	}

	return row.RuntimeIdentity.ConnectionID + "|session:" + row.ID
}

func customFieldAgentRowKey(row session.Session, agent session.AgentSession) string {
	return row.RuntimeIdentity.ConnectionID + "|pane:" + agent.PaneID
}

func customFieldSessionContext(row session.Session) fields.Context {
	path := string(row.PathRaw)
	if path == "" {
		path = row.ActivePanePath
	}

	origin := row.Origin
	if origin == "" {
		origin = session.OriginLocal
	}

	return fields.Context{
		SchemaVersion: fields.SchemaVersion,
		Field:         "",
		Row:           fields.RowSession,
		Session: fields.Session{
			ID: row.ID, Name: row.Name, Kind: string(row.Kind), Path: path, Origin: string(origin),
			ConnectionID: row.RuntimeIdentity.ConnectionID, WindowCount: row.WindowCount, Attached: row.Attached,
			Activity: row.Activity, ActivePaneID: row.ActivePaneID, ActivePanePath: row.ActivePanePath,
			ActivePaneCommand: row.ActivePaneCommand, ActivePaneTitle: row.ActivePaneTitle,
		},
		Agent: nil,
	}
}

func customFieldAgentContext(agent session.AgentSession) *fields.Agent {
	return &fields.Agent{
		PaneID: agent.PaneID, Harness: agent.Harness, Status: string(agent.Status), StatusSince: agent.StatusSince,
		CWD: agent.CWD, ProjectRoot: agent.ProjectRoot, GitBranch: agent.GitBranch, Worktree: agent.Worktree,
		SessionPath: agent.SessionPath, PID: agent.PID, Model: agent.Model, Task: agent.Task,
	}
}

func (v sessionFormatValues) resolveCustomCell(field string) (string, layout.State) {
	name, ok := display.CustomFieldName(field)
	if !ok {
		return "", layout.Absent
	}

	row := customFieldSessionRowKey(v.row)
	if v.hasAgent {
		row = customFieldAgentRowKey(v.row, v.agent)
	}

	cell, ok := v.model.customFields.cells[customFieldKey{field: name, row: row}]
	if !ok {
		return "", layout.Pending
	}

	text := cell.text
	if strings.Contains(text, "\x1b[") {
		text += ansiReset
	}

	return text, cell.state
}

func logCustomFieldFailure(ctx context.Context, result fields.Result) {
	zerolog.Ctx(ctx).Error().
		Str("component", "tui").
		Str("operation", string(tuiOperationCustomFieldRun)).
		Str("status", "failed").
		Str("reason", "command_failed").
		Int("exit_code", result.ExitCode).
		Str("error_code", classifyTUIErrorCode(result.Err)).
		Msg("custom field command failed")
}
