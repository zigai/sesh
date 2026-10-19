// Package projectpicker is the picker that opens a project: an existing one under the project
// roots, a repository to clone, or a new one to create.
package projectpicker

import (
	"cmp"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/tui"
)

const (
	ChoiceOpen ChoiceKind = iota + 1
	ChoiceAdopt
	ChoiceClone
	ChoiceCreate
)

const (
	modeLabel       = "PROJECTS"
	openGlyph       = "● "
	closedGlyph     = "○ "
	reservedRows    = 2
	minFrameWidth   = 8
	minFrameHeight  = 4
	wheelStepDelta  = 3
	listTopMargin   = 2
	maxQueryRunes   = 512
	urlSchemeMarker = "://"
)

type ChoiceKind uint8

// Entry is a project the picker lists. Session names the project's open session.
type Entry struct {
	Root        string
	Name        string
	Path        string
	Session     string
	NameTakenBy string
}

// Choice is what the person picked: a project root to open or adopt, a URL to clone, or a name
// to create.
type Choice struct {
	Kind  ChoiceKind
	Value string
}

type row struct {
	entry  int
	choice Choice
}

type Model struct {
	chrome  tui.Chrome
	input   textinput.Model
	entries []Entry
	rows    []row
	cursor  int
	listTop int
	width   int
	height  int
	status  string
	chosen  Choice
}

func NewModel(cfg *config.Config, entries []Entry) *Model {
	chrome := tui.NewChrome(cfg)
	input := chrome.NewTextInput()
	input.CharLimit = maxQueryRunes
	input.Focus()

	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(left, right Entry) int {
		return cmp.Compare(boolRank(left.Session == ""), boolRank(right.Session == ""))
	})

	m := &Model{
		chrome: chrome, input: input, entries: sorted, rows: nil, cursor: 0, listTop: 0,
		width: 0, height: 0, status: "", chosen: Choice{Kind: 0, Value: ""},
	}
	m.filter()

	return m
}

func (m *Model) Chosen() (Choice, bool) {
	return m.chosen, m.chosen.Kind != 0
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestWindowSize, textinput.Blink)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.keepCursorVisible()

		return m, nil
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	case tea.MouseWheelMsg:
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			m.move(-wheelStepDelta)
		case tea.MouseWheelDown:
			m.move(wheelStepDelta)
		case tea.MouseNone, tea.MouseLeft, tea.MouseMiddle, tea.MouseRight, tea.MouseWheelLeft, tea.MouseWheelRight,
			tea.MouseBackward, tea.MouseForward, tea.MouseButton10, tea.MouseButton11:
		}

		return m, nil
	case tea.MouseClickMsg:
		return m.updateClick(msg.Mouse())
	}

	return m.updateInput(msg)
}

func (m *Model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion

	return view
}

func (m *Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return m, tea.Quit
	case "up", "ctrl+p", "ctrl+k":
		m.move(-1)

		return m, nil
	case "down", "ctrl+n", "ctrl+j":
		m.move(1)

		return m, nil
	case "pgup":
		m.move(-m.listHeight())

		return m, nil
	case "pgdown":
		m.move(m.listHeight())

		return m, nil
	case "enter":
		return m.choose(false)
	case "alt+a":
		return m.choose(true)
	}

	return m.updateInput(msg)
}

func (m *Model) updateInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	query := m.input.Value()

	var cmd tea.Cmd

	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != query {
		m.status = ""
		m.filter()
	}

	return m, cmd
}

func (m *Model) updateClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	index := m.listTop + mouse.Y - listTopMargin
	if mouse.Y < listTopMargin || index < 0 || index >= len(m.rows) || mouse.Y-listTopMargin >= m.listHeight() {
		return m, nil
	}

	m.cursor = index
	if mouse.Button == tea.MouseLeft {
		return m.choose(false)
	}

	return m, nil
}

func (m *Model) choose(adopt bool) (tea.Model, tea.Cmd) {
	if m.cursor >= len(m.rows) {
		return m, nil
	}

	selected := m.rows[m.cursor]
	if selected.entry < 0 {
		if adopt {
			return m, nil
		}

		m.chosen = selected.choice

		return m, tea.Quit
	}

	entry := m.entries[selected.entry]

	switch {
	case adopt && entry.NameTakenBy != "":
		m.chosen = Choice{Kind: ChoiceAdopt, Value: entry.Root}
	case adopt:
		m.status = "Only a project whose name is used by another session can adopt it"

		return m, nil
	case entry.Session == "" && entry.NameTakenBy != "":
		m.status = "Session " + entry.NameTakenBy + " already has this name: alt+a adopts it"

		return m, nil
	default:
		m.chosen = Choice{Kind: ChoiceOpen, Value: entry.Root}
	}

	return m, tea.Quit
}

func (m *Model) filter() {
	query := strings.TrimSpace(m.input.Value())
	m.rows = m.rows[:0]

	if query == "" {
		for index := range m.entries {
			m.rows = append(m.rows, row{entry: index, choice: Choice{Kind: 0, Value: ""}})
		}
	} else {
		names := make([]string, len(m.entries))
		for index, entry := range m.entries {
			names[index] = entry.Name
		}

		for _, match := range fuzzy.Find(query, names) {
			m.rows = append(m.rows, row{entry: match.Index, choice: Choice{Kind: 0, Value: ""}})
		}

		if extra, ok := m.extraChoice(query); ok {
			m.rows = append(m.rows, row{entry: -1, choice: extra})
		}
	}

	m.cursor = 0
	m.listTop = 0
}

func (m *Model) extraChoice(query string) (Choice, bool) {
	if strings.Contains(query, urlSchemeMarker) || (strings.Contains(query, "@") && strings.Contains(query, ":")) {
		return Choice{Kind: ChoiceClone, Value: query}, true
	}

	if strings.ContainsAny(query, "/ ") || slices.ContainsFunc(m.entries, func(entry Entry) bool { return entry.Name == query }) {
		return Choice{Kind: 0, Value: ""}, false
	}

	return Choice{Kind: ChoiceCreate, Value: query}, true
}

func (m *Model) move(delta int) {
	if len(m.rows) == 0 {
		return
	}

	m.cursor = min(max(m.cursor+delta, 0), len(m.rows)-1)
	m.keepCursorVisible()
}

func (m *Model) keepCursorVisible() {
	height := m.listHeight()
	if m.cursor < m.listTop {
		m.listTop = m.cursor
	}

	if m.cursor >= m.listTop+height {
		m.listTop = m.cursor - height + 1
	}

	m.listTop = min(max(m.listTop, 0), max(len(m.rows)-height, 0))
}

func (m *Model) listHeight() int {
	return max(m.chrome.BodyHeight(m.height)-reservedRows, 1)
}

func (m *Model) render() string {
	if m.width < minFrameWidth || m.height < minFrameHeight {
		return modeLabel
	}

	width := m.chrome.BodyWidth(m.width)
	lines := make([]string, 0, m.chrome.BodyHeight(m.height))
	lines = append(lines, m.chrome.InputBar(&m.input, width))

	for line := range m.listHeight() {
		index := m.listTop + line

		switch {
		case index < len(m.rows):
			lines = append(lines, m.renderRow(m.rows[index], index == m.cursor, width))
		case index == 0:
			lines = append(lines, m.chrome.Pad("  "+m.chrome.Muted("No projects match"), width))
		default:
			lines = append(lines, m.chrome.Blank(width))
		}
	}

	lines = append(lines, m.renderFooter(width))

	return m.chrome.Frame(width, lines)
}

func (m *Model) renderRow(current row, selected bool, width int) string {
	if current.entry < 0 {
		label := "create " + current.choice.Value
		if current.choice.Kind == ChoiceClone {
			label = "clone " + current.choice.Value
		}

		return m.chrome.Row("+ "+label, "", selected, width)
	}

	entry := m.entries[current.entry]

	glyph := closedGlyph
	if entry.Session != "" {
		glyph = openGlyph
	}

	right := entry.Path
	if entry.Session == "" && entry.NameTakenBy != "" {
		right = "name used by " + entry.NameTakenBy + "  " + entry.Path
	}

	return m.chrome.Row(glyph+entry.Name, right, selected, width)
}

func (m *Model) renderFooter(width int) string {
	if m.status != "" {
		return m.chrome.Pad(m.chrome.Muted(m.status), width)
	}

	hints := []tui.Hint{{Key: "enter", Desc: "open"}, {Key: "esc", Desc: "close"}}
	if m.cursor < len(m.rows) && m.rows[m.cursor].entry >= 0 && m.entries[m.rows[m.cursor].entry].NameTakenBy != "" {
		hints = append(hints, tui.Hint{Key: "alt+a", Desc: "adopt"})
	}

	return m.chrome.Footer(hints, modeLabel, width)
}

func boolRank(value bool) int {
	if value {
		return 1
	}

	return 0
}
