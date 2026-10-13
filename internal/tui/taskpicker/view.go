package taskpicker

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
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
	placeColumns  = 16
	minBodyWidth  = 36
	maxBodyWidth  = 64
	maxListRows   = 10
	titleLabel    = "title"
	agentLabel    = "agent"
	popupTitle    = "new task"
	noLocations   = "nothing to start in"
	noMatch       = "nothing matches"
	startingLabel = "starting…"
)

// layout is where the parts of the body are, in body lines: the title, the agents, and then the
// places to work or the filter and the expanded list. whereTop is -1 when the popup has no places,
// and listRows is 0 when it has no list.
type layout struct {
	bodyWidth    int
	agentTop     int
	agentChoices [][]placed
	whereTop     int
	listTop      int
	listRows     int
}

// listLine is one line of the expanded list: a section heading, with row -1, or the row at that
// position of the filtered rows. last marks the final row of its section.
type listLine struct {
	section section
	row     int
	last    bool
}

// placed is a choice of a row of choices and the body columns it covers.
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
	rows := 1 + len(placeChoices(probe.agents(), valueColumn, width))

	switch {
	case opts.Expanded:
		rows += listGapRows + min(max(len(probe.lines()), 1), maxListRows)
	case probe.hasWhere():
		rows += 1 + int(whereOther) + 1
	}

	return width + frameColumns, frameRows + rows
}

// bodyWidth fits the agents and the key hints, and is the widest in a repository.
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
	below := 1 + len(agentChoices) + 1
	geometry := layout{
		bodyWidth: bodyWidth, agentTop: 1, agentChoices: agentChoices, whereTop: -1, listTop: 0, listRows: 0,
	}

	switch {
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

// footer draws key hints in the bottom edge when the footer is enabled.
func (m *Model) footer(hints []tui.Hint) string {
	if !m.showFooter {
		return ""
	}

	return m.chrome.Hints(hints)
}

// renderForm draws the title, the agents, and then the places to work or the expanded list.
func (m *Model) renderForm(geometry layout) []string {
	inputWidth := max(geometry.bodyWidth-textColumn-1, 1)
	m.title.SetWidth(inputWidth)
	m.filter.SetWidth(max(inputWidth-len(filterLabel)-1, 1))

	body := make([]string, 0, m.height-frameRows)
	body = append(body, m.field(titleLabel, m.focus == fieldTitle, " "+m.title.View()))
	body = append(body, m.renderAgents(geometry)...)

	switch {
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
			padded := name + strings.Repeat(" ", max(placeColumns-lipgloss.Width(name), 1))
			body = append(body, m.chrome.Row(padded+detail, "", place == m.where, geometry.bodyWidth))
		}
	}

	return body
}

// place names a place to work and the branch it works on: the new worktree's, the pane's
// checkout's or the task already working there, and nothing for an existing branch, which the
// expanded list picks.
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

// renderChoices draws a row of choices with the selected one highlighted.
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

// lines lays the filtered rows out under their section headings.
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
		return m.chrome.Muted(strings.Repeat(" ", rowGutter) + lines[index].section.label())
	case index < len(lines):
		line := lines[index]
		name, detail := label(m.all[m.rows[line.row]])

		tree := treeBranch
		if line.last {
			tree = treeLast
		}

		return m.chrome.TreeRow(tree, name, detail, line.row == m.cursor, width)
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
	selected := slices.Index(agents, m.agent)
	lines := make([]string, 0, len(geometry.agentChoices))

	for row, choices := range geometry.agentChoices {
		names := make([]string, 0, len(choices))
		for _, choice := range choices {
			names = append(names, agents[choice.index])
		}

		rowSelected := -1
		if len(choices) > 0 {
			rowSelected = selected - choices[0].index
		}

		line := strings.Repeat(" ", valueColumn) + m.renderChoices(names, rowSelected)
		if row == 0 {
			line = m.field(agentLabel, false, m.renderChoices(names, rowSelected))
		}

		lines = append(lines, line)
	}

	return lines
}

// renderFailure shows why the start failed, wrapped to the body, in place of the form.
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

// frame draws the border with the title in its top edge and footer in its bottom edge.
func (m *Model) frame(body []string, bodyWidth int, footer string) string {
	total := bodyWidth + frameColumns

	title := " " + popupTitle + " "
	if m.options.Project != "" {
		title = " " + popupTitle + m.chrome.Muted(" · "+m.options.Project) + " "
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

// label names a list row and adds a short detail. Its section says what kind of place it is: a
// new worktree is named by its base, a checkout by its directory with its branch, a branch only a
// remote has by its remote, and a checkout in use by the task working there.
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

// hints are the keys that act from the focused row.
func (m *Model) hints() []tui.Hint {
	agent := tui.Hint{Key: "←→", Desc: "agent"}
	enter := tui.Hint{Key: "enter", Desc: "start"}

	if m.where == whereOther && !m.options.Expanded {
		enter.Desc = "branches"
	}

	switch {
	case m.focus == fieldList:
		return []tui.Hint{agent, {Key: "↑↓", Desc: "select"}, enter, {Key: "esc", Desc: "back"}}
	case m.hasWhere():
		return []tui.Hint{agent, {Key: "↑↓", Desc: "where"}, enter}
	default:
		return []tui.Hint{agent, enter}
	}
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

// wrapWords breaks text into lines of at most width columns at spaces, so paths and branch names
// stay whole unless one alone is wider than a line.
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

// placeChoices lays a row of choices out after the label, wrapping before a choice that would not
// fit.
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
