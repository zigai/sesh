package taskpicker

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/tui"
)

const (
	treeBranch    = "├─ "
	treeLast      = "└─ "
	filterLabel   = "/"
	rowGutter     = 2
	choiceGap     = " "
	choicePadding = 2
	labelGap      = "  "
	labelColumns  = 6
	valueColumn   = labelColumns + len(labelGap)
	// textColumn is where a value's text starts, after the space that pads each choice.
	textColumn = valueColumn + 1
	// bodyColumn is where the body starts, after the left border and the space beside it.
	bodyColumn    = 2
	frameColumns  = 2 * bodyColumn
	frameRows     = 2
	cornerColumns = 3
	// listGapRows are the blank line and the filter line above the expanded list.
	listGapRows = 2
	// placeColumns is the width of a place's name, before the branch it names.
	placeColumns   = 16
	minBodyWidth   = 36
	maxBodyWidth   = 64
	maxListRows    = 10
	titleLabel     = "title"
	agentLabel     = "agent"
	machineLabel   = "host"
	popupTitle     = "new task"
	noLocations    = "nothing to start in"
	noMatch        = "nothing matches"
	noAgents       = "none available"
	noAgentFailure = "no agent is available"
	startingLabel  = "starting…"
	columnGap      = "  "
	// maxNameColumns caps the name column, so a long name leaves room for the subjects.
	maxNameColumns    = 24
	maxLabelColumns   = 22
	minSubjectColumns = 8
	ageGap            = "   "

	day   = 24 * time.Hour
	week  = 7 * day
	month = 30 * day
	year  = 365 * day
)

type columns struct {
	age, name, label, added, deleted, ahead, behind int
}

type rowParts struct {
	age, name, subject, label, added, deleted, ahead, behind string
	addedRole, deletedRole, aheadRole, behindRole            tui.SpanRole
}

type layout struct {
	bodyWidth      int
	agentTop       int
	agentChoices   [][]placed
	machineTop     int
	machineChoices [][]placed
	whereTop       int
	listTop        int
	listRows       int
}

type listLine struct {
	section section
	row     int
	last    bool
}

type placed struct {
	index int
	start int
	end   int
}

// Size is the popup size that shows options without truncating them, up to the widest the
// picker grows. In a repository it is the widest, so the branch named after a typed title fits
// and the expanded popup is as wide; a longer list scrolls.
func Size(cfg *config.Config, opts Options) (int, int) {
	probe := NewModel(cfg, opts)
	width := probe.bodyWidth()
	rows := 1 + len(placeChoices(probe.agents(), valueColumn, width)) + len(probe.machineChoices(width))

	switch {
	case opts.Problem != "":
		rows += 2
	case opts.Expanded:
		rows += listGapRows + min(max(len(probe.lines()), 1), maxListRows)
	case probe.hasWhere():
		rows += 1 + int(whereOther) + 1
	}

	return width + frameColumns, frameRows + rows
}

func (m *Model) bodyWidth() int {
	if m.hasWhere() {
		return maxBodyWidth
	}

	width := max(minBodyWidth, valueColumn+choicesWidth(m.agents()))
	if m.showFooter {
		width = max(width, hintsWidth(m.hints())+len(labelGap))
	}

	return min(width, maxBodyWidth)
}

func (m *Model) layout() layout {
	bodyWidth := max(m.width-frameColumns, 1)
	agentChoices := placeChoices(m.agents(), valueColumn, bodyWidth)
	machineChoices := m.machineChoices(bodyWidth)
	below := 1 + len(agentChoices) + len(machineChoices) + 1
	geometry := layout{
		bodyWidth: bodyWidth, agentTop: 1, agentChoices: agentChoices, machineTop: 1 + len(agentChoices),
		machineChoices: machineChoices, whereTop: -1, listTop: 0, listRows: 0,
	}

	switch {
	case m.options.Problem != "":
	case m.options.Expanded:
		geometry.listTop = below + 1
		geometry.listRows = max(m.height-frameRows-geometry.listTop, 1)
	case m.hasWhere():
		geometry.whereTop = below
	}

	return geometry
}

func (m *Model) render() string {
	if m.width <= frameColumns || m.height <= frameRows {
		return ""
	}

	geometry := m.layout()

	switch m.stage {
	case stageFailed:
		return m.frame(m.renderFailure(geometry.bodyWidth, m.height-frameRows), geometry.bodyWidth, m.footer(failedHints()))
	case stageStarting:
		return m.frame(m.renderForm(geometry), geometry.bodyWidth, m.chrome.Muted(startingLabel))
	case stagePicking:
	}

	return m.frame(m.renderForm(geometry), geometry.bodyWidth, m.footer(m.hints()))
}

func (m *Model) footer(hints []tui.Hint) string {
	if !m.showFooter {
		return ""
	}

	return m.chrome.Hints(hints)
}

func (m *Model) renderForm(geometry layout) []string {
	inputWidth := max(geometry.bodyWidth-textColumn-1, 1)
	m.title.SetWidth(inputWidth)
	m.filter.SetWidth(max(inputWidth-len(filterLabel)-1, 1))

	body := make([]string, 0, m.height-frameRows)
	body = append(body, m.field(titleLabel, m.focus == fieldTitle, " "+m.title.View()))
	body = append(body, m.renderAgents(geometry)...)
	body = append(body, m.renderChoiceRows(machineLabel, m.machineNames(), m.options.Machine, geometry.machineChoices)...)

	switch {
	case m.options.Problem != "":
		body = append(body, "", m.chrome.Muted(strings.Repeat(" ", rowGutter)+tui.SanitizeTerminalText(m.options.Problem)))
	case m.options.Expanded:
		filter := m.chrome.Label(filterLabel, m.focus == fieldList) + " " + m.filter.View()
		body = append(body, "", strings.Repeat(" ", rowGutter)+filter)

		lines := m.lines()
		for line := range geometry.listRows {
			body = append(body, m.renderLine(lines, m.listTop+line, geometry.bodyWidth))
		}
	case m.hasWhere():
		body = append(body, "")

		for place := whereNew; place <= whereOther; place++ {
			name, detail := m.place(place)
			detail = tui.SanitizeTerminalText(detail)
			padded := name + strings.Repeat(" ", max(placeColumns-lipgloss.Width(name), 1))
			body = append(body, m.chrome.Row(padded+detail, "", place == m.where, geometry.bodyWidth))
		}
	}

	return body
}

func (m *Model) place(place where) (string, string) {
	switch place {
	case whereNew:
		location := m.options.Locations[0]
		title := strings.TrimSpace(m.title.Value())

		return "new worktree", task.NewBranch(title, location.Branch, m.options.NextNumber, m.options.Branches)
	case whereHere:
		current, ok := m.current()
		if !ok {
			return "this checkout", ""
		}

		if current.Task != "" {
			return "this checkout", "task · " + taskName(current.TaskTitle)
		}

		return "this checkout", filepath.Base(current.Directory) + " · " + current.Branch
	case whereOther:
	}

	return "existing branch…", ""
}

func (m *Model) field(label string, focused bool, value string) string {
	return m.chrome.Label(label, focused) + strings.Repeat(" ", labelColumns-lipgloss.Width(label)) + labelGap + value
}

func (m *Model) renderChoices(choices []string, selected int) string {
	var line strings.Builder

	for index, choice := range choices {
		if index > 0 {
			line.WriteString(choiceGap)
		}

		line.WriteString(m.chrome.Choice(choice, index == selected))
	}

	return line.String()
}

func (m *Model) lines() []listLine {
	lines := make([]listLine, 0, len(m.rows)+int(sectionInUse)+1)

	for position, index := range m.rows {
		current := m.all[index].section
		if position == 0 || m.all[m.rows[position-1]].section != current {
			lines = append(lines, listLine{section: current, row: -1, last: false})
		}

		last := position == len(m.rows)-1 || m.all[m.rows[position+1]].section != current
		lines = append(lines, listLine{section: current, row: position, last: last})
	}

	return lines
}

func (m *Model) renderLine(lines []listLine, index, width int) string {
	switch {
	case index < len(lines) && lines[index].row < 0:
		return strings.Repeat(" ", rowGutter) + lines[index].section.label()
	case index < len(lines):
		line := lines[index]

		tree := treeBranch
		if line.last {
			tree = treeLast
		}

		return m.renderRow(m.all[m.rows[line.row]], tree, line.row == m.cursor, width)
	case index > 0:
		return ""
	case len(m.all) == 0:
		return m.chrome.Muted(strings.Repeat(" ", rowGutter) + noLocations)
	default:
		return m.chrome.Muted(strings.Repeat(" ", rowGutter) + noMatch)
	}
}

func (m *Model) renderAgents(geometry layout) []string {
	agents := m.agents()
	if len(agents) == 0 {
		return []string{m.field(agentLabel, false, m.chrome.Muted(noAgents))}
	}

	return m.renderChoiceRows(agentLabel, agents, slices.Index(agents, m.agent), geometry.agentChoices)
}

func (m *Model) machineChoices(width int) [][]placed {
	if !m.showsMachines() {
		return nil
	}

	return placeChoices(m.machineNames(), valueColumn, width)
}

func (m *Model) machineNames() []string {
	if !m.showsMachines() {
		return nil
	}

	names := make([]string, 0, len(m.options.Machines))
	for _, machine := range m.options.Machines {
		name := tui.SanitizeTerminalText(machine.Name)
		if !machine.Usable {
			name += " (update sesh)"
		}

		names = append(names, name)
	}

	return names
}

func (m *Model) renderChoiceRows(label string, names []string, selected int, rows [][]placed) []string {
	lines := make([]string, 0, len(rows))

	for row, choices := range rows {
		shown := make([]string, 0, len(choices))
		for _, choice := range choices {
			shown = append(shown, names[choice.index])
		}

		rowSelected := -1
		if len(choices) > 0 {
			rowSelected = selected - choices[0].index
		}

		line := strings.Repeat(" ", valueColumn) + m.renderChoices(shown, rowSelected)
		if row == 0 {
			line = m.field(label, false, m.renderChoices(shown, rowSelected))
		}

		lines = append(lines, line)
	}

	return lines
}

func (m *Model) renderFailure(width, height int) []string {
	lines := make([]string, 0, height)

	for _, line := range wrapWords(m.failure, width) {
		if len(lines) < height {
			lines = append(lines, m.chrome.Error(line))
		}
	}

	for len(lines) < height {
		lines = append(lines, "")
	}

	return lines
}

func (m *Model) frame(body []string, bodyWidth int, footer string) string {
	total := bodyWidth + frameColumns

	title := " " + popupTitle + " "
	if m.options.Project != "" {
		title = " " + popupTitle + m.chrome.Muted(" · "+tui.SanitizeTerminalText(m.options.Project)) + " "
	}

	if footer != "" {
		footer = " " + footer + " "
	}

	var out strings.Builder

	out.WriteString(m.chrome.Border("┌─") + title)
	out.WriteString(m.chrome.Border(strings.Repeat("─", max(total-cornerColumns-lipgloss.Width(title), 0)) + "┐"))

	for _, line := range body {
		out.WriteString("\n" + m.chrome.Border("│") + " " + m.chrome.Pad(line, bodyWidth) + " " + m.chrome.Border("│"))
	}

	out.WriteString("\n" + m.chrome.Border("└─") + footer)
	out.WriteString(m.chrome.Border(strings.Repeat("─", max(total-cornerColumns-lipgloss.Width(footer), 0)) + "┘"))

	return out.String()
}

func (m *Model) parts(entry row) rowParts {
	location := entry.location
	name, _ := label(entry)
	parts := rowParts{
		age: "", name: tui.SanitizeTerminalText(name), subject: "", label: "", added: "", deleted: "", ahead: "", behind: "",
		addedRole: tui.SpanMuted, deletedRole: tui.SpanMuted, aheadRole: tui.SpanMuted, behindRole: tui.SpanMuted,
	}

	switch {
	case location.Where == task.WhereBranch:
		parts.label = tui.SanitizeTerminalText(location.Remote)
	case location.Where != task.WhereNew && location.Branch != "":
		parts.label = tui.GitGlyph + " " + tui.SanitizeTerminalText(location.Branch)
	}

	if diff, ok := m.changes[location.Directory]; ok && location.Directory != "" {
		parts.added, parts.deleted = "+"+strconv.Itoa(diff.Added), "-"+strconv.Itoa(diff.Deleted)
		parts.addedRole, parts.deletedRole = countRole(diff.Added, tui.SpanSuccess), countRole(diff.Deleted, tui.SpanError)
	}

	ref, ok := m.ref(location)

	switch {
	case location.Task != "":
		parts.subject = tui.SanitizeTerminalText(taskName(location.TaskTitle))
	case ok:
		parts.subject = tui.SanitizeTerminalText(ref.Subject)
	}

	if !ok {
		return parts
	}

	parts.age = age(time.Unix(ref.Time, 0), m.options.Now)
	parts.ahead, parts.behind = "↑"+strconv.Itoa(ref.Ahead), "↓"+strconv.Itoa(ref.Behind)

	parts.aheadRole, parts.behindRole = countRole(ref.Ahead, tui.SpanPrimary), countRole(ref.Behind, tui.SpanSecondary)

	return parts
}

func countRole(count int, role tui.SpanRole) tui.SpanRole {
	if count == 0 {
		return tui.SpanMuted
	}

	return role
}

func (m *Model) columns() columns {
	var widths columns

	for _, entry := range m.all {
		parts := m.parts(entry)
		widths.age = max(widths.age, lipgloss.Width(parts.age))
		widths.name = max(widths.name, lipgloss.Width(parts.name))
		widths.label = max(widths.label, lipgloss.Width(parts.label))
		widths.added = max(widths.added, lipgloss.Width(parts.added))
		widths.deleted = max(widths.deleted, lipgloss.Width(parts.deleted))
		widths.ahead = max(widths.ahead, lipgloss.Width(parts.ahead))
		widths.behind = max(widths.behind, lipgloss.Width(parts.behind))
	}

	widths.name = min(widths.name, maxNameColumns)
	widths.label = min(widths.label, maxLabelColumns)

	return widths
}

func (m *Model) renderRow(entry row, tree string, selected bool, width int) string {
	widths := m.columns()
	parts := m.parts(entry)
	spans := rightSpans(parts, widths)

	left := pad(display.Truncate(parts.name, widths.name), widths.name)
	if widths.age > 0 {
		left = pad(parts.age, widths.age) + ageGap + left
	}

	room := width - m.chrome.RowWidth(tree, left, "") - len(columnGap) - 1 - tui.SpansWidth(spans)
	if room >= minSubjectColumns && parts.subject != "" {
		left += columnGap + display.Truncate(parts.subject, room)
	}

	return m.chrome.TreeRowSpans(tree, left, spans, selected, width)
}

func rightSpans(parts rowParts, widths columns) []tui.Span {
	var spans []tui.Span

	for _, column := range []struct {
		text  string
		width int
		role  tui.SpanRole
	}{
		{display.Truncate(parts.label, widths.label), widths.label, tui.SpanMuted},
		{parts.added, widths.added, parts.addedRole},
		{parts.deleted, widths.deleted, parts.deletedRole},
		{parts.ahead, widths.ahead, parts.aheadRole},
		{parts.behind, widths.behind, parts.behindRole},
	} {
		if column.width == 0 {
			continue
		}

		if len(spans) > 0 {
			spans = append(spans, tui.Span{Text: " ", Role: tui.SpanMuted})
		}

		spans = append(spans, tui.Span{Text: pad(column.text, column.width), Role: column.role})
	}

	return spans
}

func (m *Model) ref(location task.Location) (task.Ref, bool) {
	name := "refs/heads/" + location.Branch

	switch {
	case location.Remote != "":
		name = "refs/remotes/" + location.Remote + "/" + location.Branch
	case location.Where == task.WhereNew && m.options.Starts[location.Branch] != "":
		name = m.options.Starts[location.Branch]
	}

	ref, ok := m.options.Refs[name]

	return ref, ok
}

func pad(text string, width int) string {
	return text + strings.Repeat(" ", max(width-lipgloss.Width(text), 0))
}

func age(at, now time.Time) string {
	elapsed := now.Sub(at)

	switch {
	case elapsed < time.Minute:
		return "now"
	case elapsed < time.Hour:
		return strconv.Itoa(int(elapsed/time.Minute)) + "m"
	case elapsed < day:
		return strconv.Itoa(int(elapsed/time.Hour)) + "h"
	case elapsed < week:
		return strconv.Itoa(int(elapsed/day)) + "d"
	case elapsed < month:
		return strconv.Itoa(int(elapsed/week)) + "w"
	case elapsed < year:
		return strconv.Itoa(int(elapsed/month)) + "mo"
	default:
		return strconv.Itoa(int(elapsed/year)) + "y"
	}
}

func label(entry row) (string, string) {
	location := entry.location

	switch {
	case location.Where == task.WhereNew:
		return location.Branch, ""
	case location.Where == task.WhereBranch:
		return location.Branch, location.Remote
	case location.Task != "":
		return filepath.Base(location.Directory), taskName(location.TaskTitle)
	default:
		return filepath.Base(location.Directory), location.Branch
	}
}

func (s section) label() string {
	switch s {
	case sectionNew:
		return "new worktree"
	case sectionCheckout:
		return "checkouts"
	case sectionBranch:
		return "branches"
	case sectionInUse:
		return "in use"
	default:
		return ""
	}
}

// taskName shows an untitled task, whose title is its number, as task and the number.
func taskName(title string) string {
	if title != "" && strings.IndexFunc(title, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
		return "task " + title
	}

	return title
}

func (m *Model) hints() []tui.Hint {
	agent := tui.Hint{Key: "←→", Desc: "agent"}
	enter := tui.Hint{Key: "enter", Desc: "start"}

	if m.where == whereOther && !m.options.Expanded {
		enter.Desc = "branches"
	}

	var hints []tui.Hint

	switch {
	case m.options.Problem != "":
		hints = []tui.Hint{agent}
	case m.focus == fieldList:
		hints = []tui.Hint{agent, {Key: "↑↓", Desc: "select"}, enter, {Key: "esc", Desc: "back"}}
	case m.hasWhere():
		hints = []tui.Hint{agent, {Key: "↑↓", Desc: "checkout"}, enter}
	default:
		hints = []tui.Hint{agent, enter}
	}

	if m.showsMachines() {
		hints = append(hints, tui.Hint{Key: "⇧←→", Desc: machineLabel})
	}

	return hints
}

func failedHints() []tui.Hint {
	return []tui.Hint{{Key: "enter", Desc: "back"}, {Key: "ctrl+c", Desc: "close"}}
}

func hintsWidth(hints []tui.Hint) int {
	width := 0

	for index, hint := range hints {
		if index > 0 {
			width += len(labelGap)
		}

		width += lipgloss.Width(hint.Key) + 1 + lipgloss.Width(hint.Desc)
	}

	return width
}

func wrapWords(text string, width int) []string {
	var lines []string

	line := ""

	for word := range strings.FieldsSeq(text) {
		for lipgloss.Width(word) > width {
			if line != "" {
				lines = append(lines, line)
				line = ""
			}

			lines = append(lines, ansi.Truncate(word, width, ""))
			word = ansi.TruncateLeft(word, width, "")
		}

		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}

	if line != "" {
		lines = append(lines, line)
	}

	return lines
}

func placeChoices(choices []string, indent, width int) [][]placed {
	rows := [][]placed{{}}
	column := indent

	for index, choice := range choices {
		choiceWidth := lipgloss.Width(choice) + choicePadding
		row := &rows[len(rows)-1]

		if len(*row) > 0 {
			if column+len(choiceGap)+choiceWidth > width {
				rows = append(rows, []placed{})
				row = &rows[len(rows)-1]
				column = indent
			} else {
				column += len(choiceGap)
			}
		}

		*row = append(*row, placed{index: index, start: column, end: column + choiceWidth})
		column += choiceWidth
	}

	return rows
}

func choicesWidth(choices []string) int {
	width := 0

	for index, choice := range choices {
		if index > 0 {
			width += len(choiceGap)
		}

		width += lipgloss.Width(choice) + choicePadding
	}

	return width
}
