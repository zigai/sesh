// Package taskpicker is the small popup that starts a task: what it is about, where it works and
// which agent runs there. In a project session "where" lists the project's checkouts and branches;
// elsewhere it is the pane's directory or another project.
package taskpicker

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/project"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/tui"
)

const maxTitleRunes = 120

const (
	fieldTitle field = iota
	fieldWhere
	fieldAgent
	fieldCount
)

const (
	stagePicking stage = iota
	stageStarting
	stageFailed
)

const (
	rowLocation rowKind = iota
	rowHere
	rowProject
)

const (
	sectionNew section = iota
	sectionCheckout
	sectionBranch
	sectionInUse
	sectionHere
	sectionProjects
)

// Options is what the picker offers. Project names the project of a project session and is
// empty elsewhere; Locations and Location to start on come from the project's offer, and
// Projects lists the projects outside a project session. Start runs the choice while the popup
// is still open, so a failure can be shown in it; it returns the started task's ID.
type Options struct {
	Project     string
	Dir         string
	Home        string
	Locations   []task.Location
	Location    int
	Projects    []project.Project
	Harnesses   []string
	LastHarness string
	Start       func(Choice) (string, error)
}

// Choice is what the person picked: a plain tab, or a harness, a title and, in a project, the
// location the task works in. Project is the root of another project to start the task in. Show
// is a task to go to once the popup closes: one that already works in the chosen checkout, or a
// task just started in another project's session.
type Choice struct {
	Tab      bool
	Harness  string
	Title    string
	Location task.Location
	Project  string
	Show     string
}

type field int

type stage int

type rowKind int

// section is a group of where rows, listed in this order under a muted heading.
type section int

// row is one entry of the where list. A filterOnly row is listed only while filtering, so the
// list stays short.
type row struct {
	kind       rowKind
	section    section
	location   task.Location
	project    project.Project
	filterOnly bool
}

type startedMsg struct {
	id  string
	err error
}

type Model struct {
	chrome  tui.Chrome
	options Options
	all     []row
	rows    []int
	title   textinput.Model
	filter  textinput.Model
	focus   field
	cursor  int
	listTop int
	agent   string
	width   int
	height  int
	stage   stage
	failure string
	chosen  Choice
	done    bool
	// showFooter shows key hints in the bottom edge.
	showFooter bool
}

func NewModel(cfg *config.Config, opts Options) *Model {
	chrome := tui.NewChrome(cfg)
	m := &Model{
		chrome: chrome, options: opts, all: rowsOf(opts), rows: nil, title: chrome.NewTextInput(),
		filter: chrome.NewTextInput(), focus: fieldTitle, cursor: 0, listTop: 0, agent: opts.LastHarness,
		width: 0, height: 0, stage: stagePicking, failure: "", chosen: emptyChoice(), done: false,
		showFooter: cfg.Tasks.ShowFooter,
	}

	m.title.CharLimit = maxTitleRunes
	m.title.Focus()
	m.filterRows()

	if opts.Project != "" {
		m.cursor = max(slices.Index(m.rows, opts.Location), 0)
	}

	m.keepAgent()

	return m
}

// Chosen returns the pick, or false when the picker was closed without one.
func (m *Model) Chosen() (Choice, bool) {
	return m.chosen, m.done
}

func (m *Model) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.keepCursorVisible()
	case startedMsg:
		return m.finishStart(msg)
	case tea.KeyPressMsg:
		switch m.stage {
		case stageStarting:
			return m, nil
		case stageFailed:
			return m.updateFailedKey(msg)
		case stagePicking:
			return m.updateKey(msg)
		}
	case tea.MouseWheelMsg:
		m.wheel(msg.Mouse())
	case tea.MouseClickMsg:
		m.click(msg.Mouse())
	}

	return m, nil
}

func (m *Model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion

	return view
}

func (m *Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.focus == fieldWhere && m.filter.Value() != "" {
			m.filter.SetValue("")
			m.filterRows()

			return m, nil
		}

		return m, tea.Quit
	case "enter":
		return m.choose()
	case "tab":
		return m, m.focusField((m.focus + 1) % fieldCount)
	case "shift+tab":
		return m, m.focusField((m.focus + fieldCount - 1) % fieldCount)
	case "up", "ctrl+p", "ctrl+k":
		m.step(-1)
	case "down", "ctrl+n", "ctrl+j":
		m.step(1)
	default:
		return m, m.updateField(msg)
	}

	return m, nil
}

func (m *Model) updateFailedKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "enter":
		m.stage = stagePicking
		m.failure = ""
	}

	return m, nil
}

// updateField sends a key to the focused field: text to the title or the filter. Left and right
// always pick the agent; the title's cursor moves with the text input's other keys.
func (m *Model) updateField(msg tea.KeyPressMsg) tea.Cmd {
	if key := msg.String(); key == "left" || key == "right" {
		delta := 1
		if key == "left" {
			delta = -1
		}

		m.cycleAgent(delta)

		return nil
	}

	var cmd tea.Cmd

	switch m.focus {
	case fieldTitle:
		m.title, cmd = m.title.Update(msg)
	case fieldWhere:
		before := m.filter.Value()
		m.filter, cmd = m.filter.Update(msg)

		if m.filter.Value() != before {
			m.filterRows()
		}
	case fieldAgent, fieldCount:
	}

	return cmd
}

func (m *Model) focusField(next field) tea.Cmd {
	m.focus = next
	m.title.Blur()
	m.filter.Blur()

	switch next {
	case fieldTitle:
		return m.title.Focus()
	case fieldWhere:
		return m.filter.Focus()
	case fieldAgent, fieldCount:
	}

	return nil
}

func (m *Model) cycleAgent(delta int) {
	agents := m.agents()
	index := max(slices.Index(agents, m.agent), 0)
	m.agent = agents[(index+delta+len(agents))%len(agents)]
}

// choose goes to the task already working in the selected checkout, or starts the choice.
func (m *Model) choose() (tea.Model, tea.Cmd) {
	selected, ok := m.selected()
	if !ok {
		return m, nil
	}

	if selected.location.Task != "" {
		m.chosen = emptyChoice()
		m.chosen.Show = selected.location.Task
		m.done = true

		return m, tea.Quit
	}

	choice := m.choice(selected)
	if m.options.Start == nil {
		m.chosen, m.done = choice, true

		return m, tea.Quit
	}

	m.stage = stageStarting
	m.chosen = choice
	start := m.options.Start

	return m, func() tea.Msg {
		id, err := start(choice)

		return startedMsg{id: id, err: err}
	}
}

func (m *Model) finishStart(msg startedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.stage = stageFailed
		m.failure = msg.err.Error()

		return m, nil
	}

	if m.chosen.Project != "" {
		m.chosen.Show = msg.id
	}

	m.done = true

	return m, tea.Quit
}

func (m *Model) choice(selected row) Choice {
	choice := emptyChoice()
	choice.Harness = m.agent
	choice.Title = strings.TrimSpace(m.title.Value())

	switch selected.kind {
	case rowHere:
		if m.agent == config.HarnessTab {
			choice.Tab = true
			choice.Harness = ""
		}
	case rowProject:
		choice.Project = selected.project.Root
		if selected.project.Kind == project.KindFolder {
			choice.Location.Where = task.WhereMain
			choice.Location.Directory = selected.project.Root
		} else {
			choice.Location.Where = task.WhereNew
		}
	case rowLocation:
		choice.Location = selected.location
	}

	return choice
}

func (m *Model) selected() (row, bool) {
	if m.cursor >= len(m.rows) {
		return row{kind: rowLocation, section: sectionNew, location: emptyLocation(), project: emptyProject(), filterOnly: false}, false
	}

	return m.all[m.rows[m.cursor]], true
}

// agents are the agent choices for the selected row: a plain tab first in the pane's directory,
// and a shell after the agents in a project.
func (m *Model) agents() []string {
	if selected, _ := m.selected(); selected.kind == rowHere {
		return append([]string{config.HarnessTab}, m.options.Harnesses...)
	}

	return append(slices.Clone(m.options.Harnesses), config.HarnessNone)
}

// filterRows lists the rows whose label contains the filter first, then the other fuzzy
// matches, so a typed name is not outranked by a scattered match.
func (m *Model) filterRows() {
	query := strings.TrimSpace(m.filter.Value())
	m.rows = m.rows[:0]

	if query == "" {
		for index, entry := range m.all {
			if !entry.filterOnly {
				m.rows = append(m.rows, index)
			}
		}
	} else {
		labels := make([]string, len(m.all))
		for index, entry := range m.all {
			name, detail := m.label(entry)
			labels[index] = entry.section.label() + " " + name + " " + detail
		}

		for index, label := range labels {
			if strings.Contains(strings.ToLower(label), strings.ToLower(query)) {
				m.rows = append(m.rows, index)
			}
		}

		for _, match := range fuzzy.Find(query, labels) {
			if !slices.Contains(m.rows, match.Index) {
				m.rows = append(m.rows, match.Index)
			}
		}
	}

	slices.SortStableFunc(m.rows, func(a, b int) int { return int(m.all[a].section) - int(m.all[b].section) })

	m.cursor = 0
	m.listTop = 0
	m.keepAgent()
}

func (m *Model) move(delta int) {
	if len(m.rows) == 0 {
		return
	}

	m.cursor = min(max(m.cursor+delta, 0), len(m.rows)-1)
	m.keepCursorVisible()
	m.keepAgent()
}

// step moves the cursor by one row from the keyboard, wrapping from the last row to the first and
// back.
func (m *Model) step(delta int) {
	if len(m.rows) == 0 {
		return
	}

	m.cursor = (m.cursor + delta + len(m.rows)) % len(m.rows)
	m.keepCursorVisible()
	m.keepAgent()
}

// keepAgent keeps the chosen agent when the selected row offers it, and otherwise picks the
// first agent the row offers.
func (m *Model) keepAgent() {
	if agents := m.agents(); !slices.Contains(agents, m.agent) {
		m.agent = agents[0]
	}
}

// keepCursorVisible scrolls the list so the selected row shows, with its section heading when it
// is the section's first row.
func (m *Model) keepCursorVisible() {
	rows := m.layout().listRows
	lines := m.lines()

	target := slices.IndexFunc(lines, func(l listLine) bool { return l.row == m.cursor })
	if target < 0 {
		m.listTop = 0

		return
	}

	top := target
	if target > 0 && lines[target-1].row < 0 {
		top = target - 1
	}

	if top < m.listTop {
		m.listTop = top
	}

	if target >= m.listTop+rows {
		m.listTop = target - rows + 1
	}

	m.listTop = min(max(m.listTop, 0), max(len(lines)-rows, 0))
}

func (m *Model) wheel(mouse tea.Mouse) {
	switch mouse.Button {
	case tea.MouseWheelUp:
		m.move(-1)
	case tea.MouseWheelDown:
		m.move(1)
	case tea.MouseNone, tea.MouseLeft, tea.MouseMiddle, tea.MouseRight, tea.MouseWheelLeft, tea.MouseWheelRight,
		tea.MouseBackward, tea.MouseForward, tea.MouseButton10, tea.MouseButton11:
	}
}

// click selects the row or agent under the pointer.
func (m *Model) click(mouse tea.Mouse) {
	if mouse.Button != tea.MouseLeft || m.stage != stagePicking {
		return
	}

	geometry := m.layout()
	line, column := mouse.Y-1, mouse.X-bodyColumn

	lines := m.lines()
	if index := m.listTop + line - geometry.listTop; line >= geometry.listTop && line < geometry.listTop+geometry.listRows && index < len(lines) {
		if lines[index].row >= 0 {
			m.cursor = lines[index].row
			m.keepAgent()
		}

		return
	}

	if row := line - geometry.agentTop; row >= 0 && row < len(geometry.choices) {
		agents := m.agents()

		for _, choice := range geometry.choices[row] {
			if column >= choice.start && column < choice.end {
				m.agent = agents[choice.index]
			}
		}
	}
}

// rowsOf lists the where rows: a project's locations, or the pane's directory and the projects.
// New worktrees after the leading ones start from less likely bases and wait for a filter.
func rowsOf(opts Options) []row {
	if opts.Project != "" {
		rows := make([]row, 0, len(opts.Locations))
		leading := true

		for _, location := range opts.Locations {
			leading = leading && location.Where == task.WhereNew
			filterOnly := !leading && location.Where == task.WhereNew
			rows = append(rows, row{kind: rowLocation, section: sectionOf(location), location: location, project: emptyProject(), filterOnly: filterOnly})
		}

		return rows
	}

	rows := []row{{kind: rowHere, section: sectionHere, location: emptyLocation(), project: emptyProject(), filterOnly: false}}
	for _, p := range opts.Projects {
		rows = append(rows, row{kind: rowProject, section: sectionProjects, location: emptyLocation(), project: p, filterOnly: false})
	}

	return rows
}

func sectionOf(location task.Location) section {
	switch {
	case location.Task != "":
		return sectionInUse
	case location.Where == task.WhereNew:
		return sectionNew
	case location.Where == task.WhereBranch:
		return sectionBranch
	default:
		return sectionCheckout
	}
}

func emptyChoice() Choice {
	return Choice{Tab: false, Harness: "", Title: "", Location: emptyLocation(), Project: "", Show: ""}
}

func emptyLocation() task.Location {
	return task.Location{Where: "", Branch: "", Remote: "", Directory: "", Task: "", TaskTitle: ""}
}

func emptyProject() project.Project {
	return project.Project{Root: "", Name: "", Kind: ""}
}
