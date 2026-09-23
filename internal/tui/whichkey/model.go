package whichkey

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
)

const (
	whichKeyFrameMargin      = 2
	whichKeyMinDimension     = 8
	whichKeyPaddingY         = 1
	whichKeyPaddingX         = 2
	whichKeyHeaderFooterRows = 5
	whichKeyPageSizeOffset   = 8
	whichKeyDescSpacing      = 3
	whichKeyDefaultWidth     = 12
)

var errNilSource = errors.New("loading tmux key bindings: binding source is nil")

type Binding struct {
	Key         string
	Description string
	Command     string
}

type Source interface {
	ListKeyBindings(ctx context.Context, table string) ([]Binding, error)
}

type Model struct {
	config       *config.Config
	table        string
	source       Source
	lifecycle    *lifecycle
	bindings     []Binding
	filtered     []Binding
	cursor       int
	query        string
	searching    bool
	loading      bool
	err          error
	width        int
	height       int
	runAfterQuit string
}

type keyLoadMsg struct {
	bindings []Binding
	err      error
}

func NewModel(ctx context.Context, cfg *config.Config, source Source, table string) *Model {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	if table == "" {
		table = "prefix"
	}

	return &Model{
		config:       cfg,
		table:        table,
		source:       source,
		lifecycle:    newLifecycle(ctx),
		bindings:     []Binding{},
		filtered:     []Binding{},
		cursor:       0,
		query:        "",
		searching:    false,
		loading:      false,
		err:          nil,
		width:        0,
		height:       0,
		runAfterQuit: "",
	}
}

func (m *Model) Init() tea.Cmd {
	m.loading = true

	return tea.Batch(tea.RequestWindowSize, m.loadKeysCommand())
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case keyLoadMsg:
		m.loading = false
		m.err = msg.err
		m.bindings = msg.bindings
		m.filter()
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m *Model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true

	return view
}

func (m *Model) Close() {
	if m == nil || m.lifecycle == nil {
		return
	}

	m.lifecycle.Close()
}

func (m *Model) DeferredCommand() string {
	return m.runAfterQuit
}

func (m *Model) LoadError() error {
	if m == nil {
		return nil
	}

	return m.err
}

func (m *Model) loadKeysCommand() tea.Cmd {
	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		if m.source == nil {
			return keyLoadMsg{bindings: nil, err: oops.In("tui").Code("config_invalid").Wrap(errNilSource)}
		}

		bindings, err := m.source.ListKeyBindings(m.lifecycle, m.table)

		return keyLoadMsg{bindings: bindings, err: err}
	}
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := keySpecFromMsg(msg)
	if isCloseKey(key) {
		return m, tea.Quit
	}

	if key == "enter" {
		return m.runSelected()
	}

	if m.handleMoveKey(key) {
		return m, nil
	}

	if key == "ctrl+f" {
		m.searching = true

		return m, nil
	}

	if m.handleSearchKey(msg, key) {
		return m, nil
	}

	if binding, ok := m.bindingForKey(key); ok {
		m.runAfterQuit = binding.Command

		return m, tea.Quit
	}

	return m, nil
}

func isCloseKey(key string) bool {
	return key == "esc" || key == "ctrl+c" || key == "ctrl+o"
}

func (m *Model) handleMoveKey(key string) bool {
	switch key {
	case "up", "ctrl+p":
		m.move(-1)
	case "down", "ctrl+n":
		m.move(1)
	case "pgup":
		m.movePage(-m.pageSize())
	case "pgdown":
		m.movePage(m.pageSize())
	case "home":
		m.cursor = 0
	case "end":
		if len(m.filtered) > 0 {
			m.cursor = len(m.filtered) - 1
		}
	default:
		return false
	}

	return true
}

func (m *Model) handleSearchKey(msg tea.KeyPressMsg, key string) bool {
	if key == "backspace" {
		m.deleteQueryRune()

		return true
	}

	if !m.searching {
		return false
	}

	if msg.Key().Text != "" {
		m.query += msg.Key().Text
		m.filter()
	}

	return true
}

func (m *Model) deleteQueryRune() {
	if m.query == "" {
		return
	}

	_, size := utf8.DecodeLastRuneInString(m.query)
	m.query = m.query[:len(m.query)-size]
	m.filter()
}

func (m *Model) runSelected() (tea.Model, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return m, nil
	}

	m.runAfterQuit = m.filtered[m.cursor].Command

	return m, tea.Quit
}

func (m *Model) bindingForKey(key string) (Binding, bool) {
	for _, binding := range m.bindings {
		if normalizeKey(binding.Key) == normalizeKey(key) {
			return binding, true
		}
	}

	return Binding{Key: "", Description: "", Command: ""}, false
}

func (m *Model) move(delta int) {
	if len(m.filtered) == 0 {
		m.cursor = 0

		return
	}

	m.cursor = (m.cursor + delta) % len(m.filtered)
	if m.cursor < 0 {
		m.cursor += len(m.filtered)
	}
}

func (m *Model) movePage(delta int) {
	if len(m.filtered) == 0 {
		m.cursor = 0

		return
	}

	m.cursor = min(max(m.cursor+delta, 0), len(m.filtered)-1)
}

func (m *Model) pageSize() int {
	return max(m.height-whichKeyPageSizeOffset, 1)
}

func (m *Model) filter() {
	if strings.TrimSpace(m.query) == "" {
		m.filtered = append([]Binding(nil), m.bindings...)
		m.clampCursor()

		return
	}

	values := make([]string, len(m.bindings))
	for i, binding := range m.bindings {
		values[i] = binding.Key + " " + binding.Description + " " + binding.Command
	}

	matches := fuzzy.Find(m.query, values)

	m.filtered = make([]Binding, 0, len(matches))
	for _, match := range matches {
		m.filtered = append(m.filtered, m.bindings[match.Index])
	}

	m.clampCursor()
}

func (m *Model) clampCursor() {
	if len(m.filtered) == 0 {
		m.cursor = 0

		return
	}

	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
}

func (m *Model) render() string {
	width := max(m.width-whichKeyFrameMargin, 0)

	height := max(m.height-whichKeyFrameMargin, 0)
	if width < whichKeyMinDimension || height < whichKeyMinDimension {
		return truncateStyledWidth("Tmux Keys", max(m.width, 0))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.config.Theme.Border)).
		Padding(whichKeyPaddingY, whichKeyPaddingX)
	frameWidth, frameHeight := box.GetFrameSize()
	contentWidth := max(width-frameWidth, 1)
	contentHeight := max(height-frameHeight, 1)

	title := m.titleStyle().Render("Tmux Keys")
	subtitle := m.dimStyle().Render("table: " + m.table)
	header := layoutLine(title, subtitle, contentWidth)

	body := make([]string, 0, contentHeight)
	body = append(body, header, m.searchLine(contentWidth), "")
	body = append(body, m.renderRows(contentWidth, max(contentHeight-whichKeyHeaderFooterRows, 1))...)
	body = append(body, "", m.footer(contentWidth))

	box = box.Width(width).Height(height)

	return box.Render(strings.Join(body, "\n"))
}

func (m *Model) searchLine(width int) string {
	prefix := m.dimStyle().Render("/")

	query := m.query
	if query == "" && !m.searching {
		query = m.dimStyle().Render("press ctrl+f to filter")
	}

	return truncateStyledWidth(prefix+" "+query, width)
}

func (m *Model) renderRows(width int, height int) []string {
	if m.loading {
		return []string{m.dimStyle().Render("Loading key bindings...")}
	}

	if m.err != nil {
		return []string{m.errorStyle().Render("Unable to load key bindings: " + m.err.Error())}
	}

	if len(m.filtered) == 0 {
		return []string{m.dimStyle().Render("No matching key bindings")}
	}

	top := 0
	if m.cursor >= height {
		top = m.cursor - height + 1
	}

	lines := make([]string, 0, height)

	for row := range height {
		index := top + row
		if index >= len(m.filtered) {
			lines = append(lines, "")

			continue
		}

		lines = append(lines, m.renderRow(width, index, m.filtered[index]))
	}

	return lines
}

func (m *Model) renderRow(width int, index int, binding Binding) string {
	keyWidth := whichKeyDefaultWidth
	key := truncateStyledWidth(binding.Key, keyWidth)
	key = lipgloss.NewStyle().Width(keyWidth).Render(key)

	descWidth := max(width-keyWidth-whichKeyDescSpacing, 1)
	desc := truncateStyledWidth(binding.Description, descWidth)

	style := lipgloss.NewStyle()
	if index == m.cursor {
		style = style.
			Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color(m.config.Theme.Selected))
		key = m.keyStyle().Render(key)
	} else {
		key = m.keyStyle().Render(key)
		desc = m.descStyle().Render(desc)
	}

	return style.Width(width).Render(key + "  " + desc)
}

func (m *Model) footer(width int) string {
	left := m.dimStyle().Render("enter run")
	right := m.dimStyle().Render("esc close")

	if len(m.filtered) > 0 {
		left = m.dimStyle().Render(fmt.Sprintf("%d/%d", m.cursor+1, len(m.filtered)))
	}

	return layoutLine(left, right, width)
}

func (m *Model) titleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Primary)).Bold(true)
}

func (m *Model) keyStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Secondary)).Bold(true)
}

func (m *Model) descStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
}

func (m *Model) dimStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Muted))
}

func (m *Model) errorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Error))
}

func layoutLine(left string, right string, width int) string {
	leftWidth := lipgloss.Width(left)

	rightWidth := lipgloss.Width(right)
	if leftWidth+rightWidth >= width {
		return truncateStyledWidth(left, width)
	}

	return left + strings.Repeat(" ", width-leftWidth-rightWidth) + right
}

func keySpecFromMsg(msg tea.KeyPressMsg) string {
	if msg.Key().Mod != 0 {
		return msg.Keystroke()
	}

	return msg.String()
}

func normalizeKey(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "c-", "ctrl+")

	value = strings.ReplaceAll(value, "m-", "alt+")
	if value == "space" {
		return " "
	}

	return value
}

func truncateStyledWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}

	return ansi.Truncate(s, width, "")
}
