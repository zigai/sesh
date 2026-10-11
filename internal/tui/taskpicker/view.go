package taskpicker

import (
	"path/filepath"
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
	labelColumns  = 5
	// bodyColumn is where the body starts, after the left border and the space beside it.
	bodyColumn    = 2
	frameColumns  = 2 * bodyColumn
	frameRows     = 2
	cornerColumns = 3
	// fieldRows are the title line and the filter line above the where list; fixedRows adds the
	// blank line between the list and the agents.
	fieldRows     = 2
	fixedRows     = fieldRows + 1
	minBodyWidth  = 36
	maxBodyWidth  = 64
	maxListRows   = 14
	titleLabel    = "task"
	agentLabel    = "agent"
	popupTitle    = "new task"
	noLocations   = "nothing to start in"
	noMatch       = "nothing matches"
	startingLabel = "starting…"
)

// layout is where the parts of the body are, in body lines.
type layout struct {
	bodyWidth int
	listTop   int
	listRows  int
	agentTop  int
	choices   [][]placed
}

// listLine is one line of the where list: a section heading, with row -1, or the row at that
// position of the filtered rows. last marks the final row of its section.
type listLine struct {
	section section
	row     int
	last    bool
}

// placed is a choice of the agent row and the body columns it covers.
type placed struct {
	index int
	start int
	end   int
}

// Size is the popup size that shows options without truncating them, up to the widest the
// picker grows. A longer where list scrolls.
func Size(cfg *config.Config, opts Options) (int, int) {
	probe := NewModel(cfg, opts)

	width := minBodyWidth
	if cfg.Tasks.ShowFooter {
		width = max(width, hintsWidth(hints())+len(labelGap))
	}

	for _, entry := range probe.all {
		name, detail := probe.label(entry)
		width = max(width, probe.chrome.RowWidth(treeBranch, name, detail), rowGutter+lipgloss.Width(entry.section.label()))
	}

	agentSets := [][]string{
		append([]string{config.HarnessTab}, opts.Harnesses...),
		append(append([]string{}, opts.Harnesses...), config.HarnessNone),
	}
	for _, agents := range agentSets {
		width = max(width, labelColumns+len(labelGap)+choicesWidth(agents))
	}

	width = min(width, maxBodyWidth)

	agentRows := 0
	for _, agents := range agentSets {
		agentRows = max(agentRows, len(placeChoices(agents, labelColumns+len(labelGap), width)))
	}

	listRows := min(max(len(probe.lines()), 1), maxListRows)

	return width + frameColumns, frameRows + fixedRows + listRows + agentRows
}

func (m *Model) layout() layout {
	bodyWidth := max(m.width-frameColumns, 1)
	choices := placeChoices(m.agents(), labelColumns+len(labelGap), bodyWidth)
	listRows := max(m.height-frameRows-fixedRows-len(choices), 1)

	return layout{bodyWidth: bodyWidth, listTop: fieldRows, listRows: listRows, agentTop: fieldRows + listRows + 1, choices: choices}
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

	return m.frame(m.renderForm(geometry), geometry.bodyWidth, m.footer(hints()))
}

// footer draws key hints in the bottom edge when the footer is enabled.
func (m *Model) footer(hints []tui.Hint) string {
	if !m.showFooter {
		return ""
	}

	return m.chrome.Hints(hints)
}

func (m *Model) renderForm(geometry layout) []string {
	inputWidth := max(geometry.bodyWidth-labelColumns-len(labelGap)-1, 1)
	m.title.SetWidth(inputWidth)
	m.filter.SetWidth(inputWidth)

	body := make([]string, 0, m.height-frameRows)
	body = append(body, m.field(titleLabel, m.focus == fieldTitle, m.title.View()))

	filter := ""
	if m.focus == fieldWhere || m.filter.Value() != "" {
		filter = m.field(filterLabel, m.focus == fieldWhere, m.filter.View())
	}

	body = append(body, filter)

	lines := m.lines()
	for line := range geometry.listRows {
		body = append(body, m.renderLine(lines, m.listTop+line, geometry.bodyWidth))
	}

	body = append(body, "")

	return append(body, m.renderAgents(geometry)...)
}

func (m *Model) field(label string, focused bool, value string) string {
	return m.chrome.Label(label, focused) + strings.Repeat(" ", labelColumns-lipgloss.Width(label)) + labelGap + value
}

// lines lays the filtered rows out under their section headings.
func (m *Model) lines() []listLine {
	lines := make([]listLine, 0, len(m.rows)+int(sectionProjects)+1)

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
		name, detail := m.label(m.all[m.rows[line.row]])

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
	indent := strings.Repeat(" ", labelColumns+len(labelGap))
	lines := make([]string, 0, len(geometry.choices))

	for row, choices := range geometry.choices {
		var line strings.Builder

		if row == 0 {
			line.WriteString(m.field(agentLabel, m.focus == fieldAgent, ""))
		} else {
			line.WriteString(indent)
		}

		for position, choice := range choices {
			if position > 0 {
				line.WriteString(choiceGap)
			}

			line.WriteString(m.chrome.Choice(agents[choice.index], agents[choice.index] == m.agent))
		}

		lines = append(lines, line.String())
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

// label names a where row and adds a short detail. Its section says what kind of place it is:
// a new worktree is named by its base, a checkout by its directory with its branch, a branch only
// a remote has by its remote, and a checkout in use by the task working there.
func (m *Model) label(entry row) (string, string) {
	switch entry.kind {
	case rowHere:
		return m.short(m.options.Dir), ""
	case rowProject:
		return entry.project.Name, ""
	case rowLocation:
	}

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
	case sectionHere:
		return "here"
	case sectionProjects:
		return "projects"
	default:
		return ""
	}
}

// short writes a path under the home directory with ~.
func (m *Model) short(path string) string {
	if m.options.Home == "" {
		return path
	}

	if relative, found := strings.CutPrefix(path, m.options.Home); found && (relative == "" || strings.HasPrefix(relative, "/")) {
		return "~" + relative
	}

	return path
}

// taskName shows an untitled task, whose title is its number, as task and the number.
func taskName(title string) string {
	if title != "" && strings.IndexFunc(title, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
		return "task " + title
	}

	return title
}

func hints() []tui.Hint {
	return []tui.Hint{{Key: "enter", Desc: "start"}, {Key: "tab", Desc: "next"}, {Key: "esc", Desc: "close"}}
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

// placeChoices lays the agent choices out in rows after the label, wrapping before a choice
// that would not fit.
func placeChoices(agents []string, indent, width int) [][]placed {
	rows := [][]placed{{}}
	column := indent

	for index, agent := range agents {
		choiceWidth := lipgloss.Width(agent) + choicePadding
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

func choicesWidth(agents []string) int {
	width := 0

	for index, agent := range agents {
		if index > 0 {
			width += len(choiceGap)
		}

		width += lipgloss.Width(agent) + choicePadding
	}

	return width
}
