package taskpicker

import (
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/git"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/tui"
)

const maxTitleRunes = 120

const (
	fieldTitle field = iota
	fieldList
)

const (
	whereNew where = iota
	whereHere
	whereOther
)

const (
	stagePicking stage = iota
	stageStarting
	stageFailed
)

const (
	sectionNew section = iota
	sectionCheckout
	sectionBranch
	sectionInUse
)

var (
	whereChoices = []where{whereNew, whereHere, whereOther}
	sectionOrder = []section{sectionNew, sectionCheckout, sectionBranch, sectionInUse}
)

// Options is what the picker offers. Project names the pane's project and is empty elsewhere;
// Locations, the Current checkout's index in them, and NextNumber and Branches that name a new
// worktree's branch come from the project's offer. Expanded lists every location of the project,
// and Title and Agent fill in the title and agent, the first agent when empty. Refs describe the
// branches as of Now, Starts names the ref each new worktree base starts from, and Changes counts the uncommitted changes of checkouts after the popup
// opens. Start runs the choice while the popup is still open, so a failure can be shown in it.
// Machines lists this machine and then the remote hosts a task can start on; the popup shows them
// when there is more than one. Machine is the index of the chosen one, and Problem, when set, is
// why no task can start on it, shown in place of the checkouts.
type Options struct {
	Project    string
	Locations  []task.Location
	Current    int
	NextNumber int
	Branches   []string
	Harnesses  []string
	Agent      string
	Title      string
	Expanded   bool
	Refs       map[string]task.Ref
	Starts     map[string]string
	Now        time.Time
	Changes    func() map[string]git.Diff
	Start      func(Choice) error
	Machines   []Machine
	Machine    int
	Problem    string
}

type Machine struct {
	Name   string
	Usable bool
}

// Choice is what the person picked: a plain tab, or a harness, a title and, in a project, the
// location the task works in. Show is a task to go to once the popup closes, one that already
// works in the chosen checkout. Reopen asks for the popup again on Machine, Expanded or not,
// keeping the title and harness.
type Choice struct {
	Reopen   bool
	Expanded bool
	Machine  int
	Tab      bool
	Harness  string
	Title    string
	Location task.Location
	Show     string
}

type field int

type where int

type stage int

type section int

type row struct {
	section    section
	location   task.Location
	filterOnly bool
}

type startedMsg struct {
	err error
}

type changesMsg map[string]git.Diff

type Model struct {
	chrome     tui.Chrome
	options    Options
	all        []row
	rows       []int
	title      textinput.Model
	filter     textinput.Model
	focus      field
	where      where
	cursor     int
	listTop    int
	agent      string
	width      int
	height     int
	stage      stage
	failure    string
	chosen     Choice
	done       bool
	showFooter bool
	changes    map[string]git.Diff
}

func NewModel(cfg *config.Config, opts Options) *Model {
	chrome := tui.NewChrome(cfg)
	m := &Model{
		chrome: chrome, options: opts, all: rowsOf(opts), rows: nil, title: chrome.NewTextInput(),
		filter: chrome.NewTextInput(), focus: fieldTitle, where: whereNew, cursor: 0, listTop: 0,
		agent: opts.Agent, width: 0, height: 0, stage: stagePicking, failure: "", chosen: emptyChoice(),
		done: false, showFooter: cfg.Tasks.ShowFooter, changes: nil,
	}

	m.title.CharLimit = maxTitleRunes
	m.title.SetValue(opts.Title)
	m.filterRows()

	switch {
	case !m.hasWhere():
		m.where = whereHere
	case opts.Expanded:
		m.where = whereOther
	}

	m.focusField(fieldTitle)

	if opts.Expanded {
		m.focusField(fieldList)
	}

	m.keepAgent()

	return m
}

func (m *Model) Chosen() (Choice, bool) {
	return m.chosen, m.done
}

func (m *Model) Init() tea.Cmd {
	if m.options.Changes == nil {
		return tea.RequestWindowSize
	}

	return tea.Batch(tea.RequestWindowSize, func() tea.Msg { return changesMsg(m.options.Changes()) })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.keepCursorVisible()
	case startedMsg:
		return m.finishStart(msg)
	case changesMsg:
		m.changes = msg
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
		return m.click(msg.Mouse())
	default:
		if m.stage == stagePicking {
			return m.updateInput(msg)
		}
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
	switch key := msg.String(); key {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		return m.escape()
	case "enter":
		return m.enter()
	case "left", "right":
		m.cycleAgent(key)

		return m, nil
	case "shift+left", "shift+right":
		return m.cycleMachine(key)
	case "tab", "shift+tab":
		return m, m.focusField(m.otherField())
	case "up", "down":
		m.moveVertically(key)

		return m, nil
	}

	return m.updateInput(msg)
}

func (m *Model) updateInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.focus == fieldList {
		return m, m.updateFilter(msg)
	}

	title, cmd := m.title.Update(msg)
	m.title = title

	return m, cmd
}

func (m *Model) moveVertically(key string) {
	delta := 1
	if key == "up" {
		delta = -1
	}

	switch {
	case m.options.Expanded:
		m.step(delta)
	case m.hasWhere():
		index := slices.Index(whereChoices, m.where)
		m.where = whereChoices[(index+delta+len(whereChoices))%len(whereChoices)]
	}
}

func (m *Model) updateFilter(msg tea.Msg) tea.Cmd {
	before := m.filter.Value()
	filter, cmd := m.filter.Update(msg)
	m.filter = filter

	if m.filter.Value() != before {
		m.filterRows()
	}

	return cmd
}

func (m *Model) escape() (tea.Model, tea.Cmd) {
	switch {
	case m.focus == fieldList && m.filter.Value() != "":
		m.filter.SetValue("")
		m.filterRows()

		return m, nil
	case m.options.Expanded:
		return m.reopen(false)
	default:
		return m, tea.Quit
	}
}

func (m *Model) enter() (tea.Model, tea.Cmd) {
	if m.where == whereOther && !m.options.Expanded {
		return m.reopen(true)
	}

	return m.choose()
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

func (m *Model) otherField() field {
	if m.focus == fieldTitle && m.options.Expanded {
		return fieldList
	}

	return fieldTitle
}

func (m *Model) focusField(next field) tea.Cmd {
	m.focus = next
	m.title.Blur()
	m.filter.Blur()

	switch next {
	case fieldTitle:
		return m.title.Focus()
	case fieldList:
		return m.filter.Focus()
	}

	return nil
}

func (m *Model) cycleAgent(key string) {
	delta := 1
	if key == "left" {
		delta = -1
	}

	agents := m.agents()
	if len(agents) == 0 {
		return
	}

	index := max(slices.Index(agents, m.agent), 0)
	m.agent = agents[(index+delta+len(agents))%len(agents)]
}

func (m *Model) cycleMachine(key string) (tea.Model, tea.Cmd) {
	delta := 1
	if key == "shift+left" {
		delta = -1
	}

	count := len(m.options.Machines)
	if !m.showsMachines() {
		return m, nil
	}

	for step := 1; step < count; step++ {
		next := ((m.options.Machine+delta*step)%count + count) % count
		if m.options.Machines[next].Usable {
			return m.reopenOn(next)
		}
	}

	return m, nil
}

func (m *Model) reopenOn(machine int) (tea.Model, tea.Cmd) {
	if machine == m.options.Machine || !m.options.Machines[machine].Usable {
		return m, nil
	}

	model, cmd := m.reopen(false)
	m.chosen.Machine = machine

	return model, cmd
}

func (m *Model) showsMachines() bool {
	return len(m.options.Machines) > 1
}

func (m *Model) reopen(expanded bool) (tea.Model, tea.Cmd) {
	m.chosen = emptyChoice()
	m.chosen.Reopen = true
	m.chosen.Expanded = expanded
	m.chosen.Machine = m.options.Machine
	m.chosen.Title = strings.TrimSpace(m.title.Value())
	m.chosen.Harness = m.agent
	m.done = true

	return m, tea.Quit
}

// hasWhere reports whether the task can work somewhere other than the pane's checkout: in a
// repository, whose first location is a new worktree.
func (m *Model) hasWhere() bool {
	return m.options.Project != "" && len(m.options.Locations) > 0 && m.options.Locations[0].Where == task.WhereNew
}

func (m *Model) current() (task.Location, bool) {
	if index := m.options.Current; index >= 0 && index < len(m.options.Locations) {
		return m.options.Locations[index], true
	}

	return emptyLocation(), false
}

func (m *Model) location() (task.Location, bool) {
	switch {
	case m.options.Project == "":
		return emptyLocation(), true
	case m.options.Expanded:
		if m.cursor >= len(m.rows) {
			return emptyLocation(), false
		}

		return m.all[m.rows[m.cursor]].location, true
	case m.where == whereNew:
		return m.options.Locations[0], true
	case m.where == whereHere:
		return m.current()
	default:
		return emptyLocation(), false
	}
}

func (m *Model) choose() (tea.Model, tea.Cmd) {
	location, ok := m.location()
	if !ok || m.options.Problem != "" {
		return m, nil
	}

	if location.Task != "" {
		m.chosen = emptyChoice()
		m.chosen.Show = location.Task
		m.done = true

		return m, tea.Quit
	}

	if m.agent == "" {
		m.stage = stageFailed
		m.failure = noAgentFailure

		return m, nil
	}

	choice := emptyChoice()
	choice.Harness = m.agent
	choice.Title = strings.TrimSpace(m.title.Value())
	choice.Location = location

	if m.agent == config.HarnessTab {
		choice.Tab = true
		choice.Harness = ""
	}

	if m.options.Start == nil {
		m.chosen, m.done = choice, true

		return m, tea.Quit
	}

	m.stage = stageStarting
	m.chosen = choice
	start := m.options.Start

	return m, func() tea.Msg {
		return startedMsg{err: start(choice)}
	}
}

func (m *Model) finishStart(msg startedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.stage = stageFailed
		m.failure = msg.err.Error()

		return m, nil
	}

	m.done = true

	return m, tea.Quit
}

func (m *Model) agents() []string {
	if m.options.Project == "" {
		return append(slices.Clone(m.options.Harnesses), config.HarnessTab)
	}

	return slices.Clone(m.options.Harnesses)
}

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
			name, detail := label(entry)
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

	slices.SortStableFunc(m.rows, func(a, b int) int {
		return slices.Index(sectionOrder, m.all[a].section) - slices.Index(sectionOrder, m.all[b].section)
	})

	m.cursor = 0
	m.listTop = 0
}

func (m *Model) move(delta int) {
	if !m.options.Expanded || len(m.rows) == 0 {
		return
	}

	m.cursor = min(max(m.cursor+delta, 0), len(m.rows)-1)
	m.keepCursorVisible()
}

func (m *Model) step(delta int) {
	if len(m.rows) == 0 {
		return
	}

	m.cursor = (m.cursor + delta + len(m.rows)) % len(m.rows)
	m.keepCursorVisible()
}

func (m *Model) keepAgent() {
	agents := m.agents()

	switch {
	case len(agents) == 0:
		m.agent = ""
	case !slices.Contains(agents, m.agent):
		m.agent = agents[0]
	}
}

func (m *Model) keepCursorVisible() {
	rows := m.layout().listRows
	lines := m.lines()

	target := slices.IndexFunc(lines, func(l listLine) bool { return !l.heading && l.row == m.cursor })
	if target < 0 || rows <= 0 {
		m.listTop = 0

		return
	}

	top := target
	if target > 0 && lines[target-1].heading {
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

func (m *Model) click(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	if mouse.Button != tea.MouseLeft || m.stage != stagePicking {
		return m, nil
	}

	geometry := m.layout()
	line, column := mouse.Y-1, mouse.X-bodyColumn

	if index, found := clickedChoice(geometry.machineChoices, geometry.machineTop, line, column); found {
		return m.reopenOn(index)
	}

	switch {
	case geometry.showsWhere && line >= geometry.whereTop && line < geometry.whereTop+len(whereChoices):
		m.where = whereChoices[line-geometry.whereTop]
	case line >= geometry.listTop && line < geometry.listTop+geometry.listRows:
		m.clickList(line - geometry.listTop)
	default:
		if index, found := clickedChoice(geometry.agentChoices, geometry.agentTop, line, column); found {
			m.agent = m.agents()[index]
		}
	}

	return m, nil
}

func clickedChoice(rows [][]placed, top, line, column int) (int, bool) {
	if line < top || line >= top+len(rows) {
		return 0, false
	}

	return choiceAt(rows[line-top], column)
}

func (m *Model) clickList(line int) {
	lines := m.lines()
	if index := m.listTop + line; index < len(lines) && !lines[index].heading {
		m.cursor = lines[index].row
	}
}

func choiceAt(choices []placed, column int) (int, bool) {
	for _, choice := range choices {
		if column >= choice.start && column < choice.end {
			return choice.index, true
		}
	}

	return 0, false
}

// rowsOf lists the rows of the expanded list: the project's locations. New worktrees after the
// leading ones start from less likely bases and wait for a filter.
func rowsOf(opts Options) []row {
	rows := make([]row, 0, len(opts.Locations))
	leading := true

	for _, location := range opts.Locations {
		leading = leading && location.Where == task.WhereNew
		filterOnly := !leading && location.Where == task.WhereNew
		rows = append(rows, row{section: sectionOf(location), location: location, filterOnly: filterOnly})
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
	return Choice{Reopen: false, Expanded: false, Machine: 0, Tab: false, Harness: "", Title: "", Location: emptyLocation(), Show: ""}
}

func emptyLocation() task.Location {
	return task.Location{Where: "", Branch: "", Remote: "", Directory: "", Task: "", TaskTitle: ""}
}
