package snapshotpicker

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/tui"
)

const snapshotHistorySelectionPrefix = "▌ "

const (
	SnapshotValid   Validity = "valid"
	SnapshotPartial Validity = "partial"
	SnapshotInvalid Validity = "invalid"
)

const (
	wheelStepDelta       = 3
	borderMarginWidth    = 4
	borderMarginHeight   = 2
	minSectionHeight     = 2
	frameBorderPadding   = 2
	timestampColumnWidth = 72
	countsColumnWidth    = 54
	sizeColumnWidth      = 64
	fixedPrefixWidth     = 4
	fixedGapWidth        = 1
	fixedStatusWidth     = 7
	timestampFieldWidth  = 19
	ageFieldWidth        = 4
	countsFieldWidth     = 11
	sizeFieldWidth       = 8
	fileFieldGap         = 1
	gibDivisor           = 1 << 30
	mibDivisor           = 1 << 20
	kibDivisor           = 1 << 10
)

var errNilConfig = errors.New("config must not be nil")

type Validity string

type Entry struct {
	ID           string
	Path         string
	SavedAt      time.Time
	SizeBytes    int64
	SessionCount int
	WindowCount  int
	PaneCount    int
	Validity     Validity
	Latest       bool
	Warnings     []string
}

type Model struct {
	entries  []Entry
	config   *config.Config
	now      time.Time
	width    int
	height   int
	cursor   int
	listTop  int
	selected *Entry
	status   string
}

type snapshotHistoryLayout struct {
	bodyWidth  int
	listHeight int
	showHeader bool
	showStatus bool
	showFooter bool
}

type snapshotColumnLayout struct {
	showTimestamp bool
	showCounts    bool
	showSize      bool
	fileWidth     int
}

func (entry Entry) Restorable() bool {
	return entry.Validity == SnapshotValid || entry.Validity == SnapshotPartial
}

func NewModel(cfg *config.Config, entries []Entry) (*Model, error) {
	if cfg == nil {
		return nil, oops.In("tui").Code("config_invalid").Wrap(errNilConfig)
	}

	cloned := make([]Entry, len(entries))
	copy(cloned, entries)

	for index := range cloned {
		cloned[index].Warnings = append([]string(nil), entries[index].Warnings...)
	}

	return &Model{
		entries:  cloned,
		config:   cfg,
		now:      time.Now(),
		width:    0,
		height:   0,
		cursor:   0,
		listTop:  0,
		selected: nil,
		status:   "",
	}, nil
}

func (m *Model) Selected() (Entry, bool) {
	if m == nil || m.selected == nil {
		return emptySnapshotEntry(), false
	}

	return *m.selected, true
}

func (m *Model) SelectedID() string {
	entry, ok := m.Selected()
	if !ok {
		return ""
	}

	return entry.ID
}

func emptySnapshotEntry() Entry {
	var entry Entry

	return entry
}

func (m *Model) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ensureSnapshotCursorVisible()
	case tea.KeyPressMsg:
		return m.updateSnapshotHistoryKey(msg)
	case tea.MouseWheelMsg:
		m.moveSnapshotCursor(snapshotWheelDelta(msg.Mouse().Button))
	case tea.MouseClickMsg:
		return m.updateSnapshotHistoryClick(msg.Mouse())
	}

	return m, nil
}

func (m *Model) View() tea.View {
	view := tea.NewView(m.renderSnapshotHistory())
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion

	return view
}

func (m *Model) updateSnapshotHistoryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k", "ctrl+p":
		m.moveSnapshotCursor(-1)
	case "down", "j", "ctrl+n":
		m.moveSnapshotCursor(1)
	case "pgup":
		m.moveSnapshotCursor(-m.snapshotHistoryPageSize())
	case "pgdown":
		m.moveSnapshotCursor(m.snapshotHistoryPageSize())
	case "home", "g":
		m.setSnapshotCursor(0)
	case "end", "G":
		m.setSnapshotCursor(len(m.entries) - 1)
	case "enter":
		if m.selectCurrentSnapshot() {
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m *Model) updateSnapshotHistoryClick(mouse tea.Mouse) (tea.Model, tea.Cmd) {
	index, ok := m.snapshotIndexAtY(mouse.Y)
	if !ok {
		return m, nil
	}

	m.setSnapshotCursor(index)

	if mouse.Button == tea.MouseLeft && m.selectCurrentSnapshot() {
		return m, tea.Quit
	}

	return m, nil
}

func snapshotWheelDelta(button tea.MouseButton) int {
	switch button {
	case tea.MouseWheelUp:
		return -wheelStepDelta
	case tea.MouseWheelDown:
		return wheelStepDelta
	default:
		return 0
	}
}

func (m *Model) moveSnapshotCursor(delta int) {
	if len(m.entries) == 0 || delta == 0 {
		return
	}

	m.setSnapshotCursor(m.cursor + delta)
}

func (m *Model) setSnapshotCursor(index int) {
	if len(m.entries) == 0 {
		m.cursor = 0
		m.listTop = 0

		return
	}

	m.cursor = min(max(index, 0), len(m.entries)-1)
	m.ensureSnapshotCursorVisible()
}

func (m *Model) selectCurrentSnapshot() bool {
	if len(m.entries) == 0 || m.cursor >= len(m.entries) {
		return false
	}

	entry := m.entries[m.cursor]
	if !entry.Restorable() {
		detail := "snapshot is invalid"
		if len(entry.Warnings) > 0 {
			detail = entry.Warnings[0]
		}

		m.status = "Cannot restore: " + detail

		return false
	}

	m.selected = &entry

	return true
}

func (m *Model) snapshotLayout() snapshotHistoryLayout {
	bodyWidth := max(m.width-borderMarginWidth, 1)
	bodyHeight := max(m.height-borderMarginHeight, 1)
	reserved := 1

	showHeader := bodyHeight-reserved >= minSectionHeight
	if showHeader {
		reserved++
	}

	showFooter := bodyHeight-reserved >= minSectionHeight
	if showFooter {
		reserved++
	}

	showStatus := bodyHeight-reserved >= minSectionHeight
	if showStatus {
		reserved++
	}

	return snapshotHistoryLayout{
		bodyWidth:  bodyWidth,
		listHeight: max(bodyHeight-reserved, 1),
		showHeader: showHeader,
		showStatus: showStatus,
		showFooter: showFooter,
	}
}

func (m *Model) snapshotHistoryPageSize() int {
	return max(m.snapshotLayout().listHeight, 1)
}

func (m *Model) ensureSnapshotCursorVisible() {
	if len(m.entries) == 0 {
		m.listTop = 0

		return
	}

	height := m.snapshotHistoryPageSize()
	if m.cursor < m.listTop {
		m.listTop = m.cursor
	}

	if m.cursor >= m.listTop+height {
		m.listTop = m.cursor - height + 1
	}

	maxTop := max(len(m.entries)-height, 0)
	m.listTop = min(max(m.listTop, 0), maxTop)
}

func (m *Model) snapshotIndexAtY(y int) (int, bool) {
	layout := m.snapshotLayout()

	listY := 2
	if layout.showHeader {
		listY++
	}

	row := y - listY
	if row < 0 || row >= layout.listHeight {
		return 0, false
	}

	index := m.listTop + row

	return index, index < len(m.entries)
}

func (m *Model) renderSnapshotHistory() string {
	if m.width == 0 || m.height == 0 {
		return "Loading snapshot history..."
	}

	if m.width < 5 || m.height < 4 {
		return display.Truncate("Snapshot History", max(m.width, 1))
	}

	layout := m.snapshotLayout()
	horizontal := strings.Repeat("─", layout.bodyWidth+frameBorderPadding)
	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Border))
	top := borderStyle.Render("┌" + horizontal + "┐")
	bottom := borderStyle.Render("└" + horizontal + "┘")
	left := borderStyle.Render("│")
	right := borderStyle.Render("│")

	var out strings.Builder
	out.WriteString(top)
	writeExactFramedLine(&out, m.snapshotHistoryTitle(layout.bodyWidth), left, right)

	if layout.showHeader {
		writeExactFramedLine(&out, m.snapshotHistoryHeader(layout.bodyWidth), left, right)
	}

	m.writeSnapshotHistoryRows(&out, layout, left, right)

	if layout.showStatus {
		writeExactFramedLine(&out, m.snapshotHistoryStatus(layout.bodyWidth), left, right)
	}

	if layout.showFooter {
		writeExactFramedLine(&out, m.snapshotHistoryFooter(layout.bodyWidth), left, right)
	}

	out.WriteByte('\n')
	out.WriteString(bottom)

	return out.String()
}

func (m *Model) snapshotHistoryTitle(width int) string {
	title := "Snapshot History"
	if len(m.entries) > 0 {
		title = fmt.Sprintf("Snapshot History · %d", len(m.entries))
	}

	return padSnapshotLine(
		lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Primary)).Bold(true).Render(title),
		width,
	)
}

func (m *Model) snapshotHistoryHeader(width int) string {
	columns := snapshotColumns(width)

	return padSnapshotLine(renderSnapshotColumns(columns, emptySnapshotEntry(), m.now, false, true), width)
}

func (m *Model) writeSnapshotHistoryRows(
	out *strings.Builder,
	layout snapshotHistoryLayout,
	left string,
	right string,
) {
	if len(m.entries) == 0 {
		line := lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Muted)).Render("No saved snapshots found")
		writeExactFramedLine(out, padSnapshotLine(line, layout.bodyWidth), left, right)

		for row := 1; row < layout.listHeight; row++ {
			writeExactFramedLine(out, strings.Repeat(" ", layout.bodyWidth), left, right)
		}

		return
	}

	columns := snapshotColumns(layout.bodyWidth)

	for row := range layout.listHeight {
		index := m.listTop + row
		if index >= len(m.entries) {
			writeExactFramedLine(out, strings.Repeat(" ", layout.bodyWidth), left, right)

			continue
		}

		selected := index == m.cursor

		line := renderSnapshotColumns(columns, m.entries[index], m.now, selected, false)
		if selected {
			line = lipgloss.NewStyle().
				Foreground(lipgloss.Color("255")).
				Background(lipgloss.Color(m.config.Theme.Selected)).
				Bold(true).
				Render(line)
		}

		writeExactFramedLine(out, padSnapshotLine(line, layout.bodyWidth), left, right)
	}
}

func snapshotColumns(width int) snapshotColumnLayout {
	columns := snapshotColumnLayout{
		showTimestamp: width >= timestampColumnWidth,
		showCounts:    width >= countsColumnWidth,
		showSize:      width >= sizeColumnWidth,
		fileWidth:     0,
	}

	fixed := lipgloss.Width(snapshotHistorySelectionPrefix) + fixedPrefixWidth + fixedGapWidth + fixedStatusWidth
	if columns.showTimestamp {
		fixed += fixedGapWidth + timestampFieldWidth
	}

	if columns.showCounts {
		fixed += fixedGapWidth + countsFieldWidth
	}

	if columns.showSize {
		fixed += fixedGapWidth + sizeFieldWidth
	}

	columns.fileWidth = max(width-fixed-fileFieldGap, 1)

	return columns
}

func renderSnapshotColumns(
	columns snapshotColumnLayout,
	entry Entry,
	now time.Time,
	selected bool,
	header bool,
) string {
	prefix := "  "
	age := "Age"
	status := "Status"
	file := "File"
	timestamp := "Timestamp"
	counts := " S  W  P "
	size := "Size"

	if !header {
		if selected {
			prefix = snapshotHistorySelectionPrefix
		} else if entry.Latest {
			prefix = "* "
		}

		age = tui.FormatActivityAt(entry.SavedAt, now)
		status = string(entry.Validity)
		file = filepath.Base(entry.Path)
		timestamp = entry.SavedAt.Format("2006-01-02 15:04:05")
		counts = fmt.Sprintf("%2d %2d %2d", entry.SessionCount, entry.WindowCount, entry.PaneCount)
		size = compactByteSize(entry.SizeBytes)
	}

	parts := []string{prefix}
	if columns.showTimestamp {
		parts = append(parts, fitSnapshotCell(timestamp, timestampFieldWidth, false), " ")
	}

	parts = append(parts, fitSnapshotCell(age, ageFieldWidth, true), " ")

	if columns.showCounts {
		parts = append(parts, fitSnapshotCell(counts, countsFieldWidth, true), " ")
	}

	if columns.showSize {
		parts = append(parts, fitSnapshotCell(size, sizeFieldWidth, true), " ")
	}

	parts = append(parts, fitSnapshotCell(status, fixedStatusWidth, false), " ", fitSnapshotCell(file, columns.fileWidth, false))

	return strings.Join(parts, "")
}

func fitSnapshotCell(value string, width int, right bool) string {
	value = display.Truncate(value, width)

	padding := max(width-lipgloss.Width(value), 0)
	if right {
		return strings.Repeat(" ", padding) + value
	}

	return value + strings.Repeat(" ", padding)
}

func compactByteSize(size int64) string {
	switch {
	case size >= gibDivisor:
		return fmt.Sprintf("%.1fG", float64(size)/gibDivisor)
	case size >= mibDivisor:
		return fmt.Sprintf("%.1fM", float64(size)/mibDivisor)
	case size >= kibDivisor:
		return fmt.Sprintf("%.1fK", float64(size)/kibDivisor)
	default:
		return fmt.Sprintf("%dB", size)
	}
}

func (m *Model) snapshotHistoryStatus(width int) string {
	status := m.status
	if status == "" && len(m.entries) > 0 && m.cursor < len(m.entries) {
		entry := m.entries[m.cursor]
		switch {
		case len(entry.Warnings) > 0:
			status = entry.Warnings[0]
		case entry.Latest:
			status = "Latest snapshot"
		default:
			status = entry.Path
		}
	}

	if status == "" {
		status = "No snapshot selected"
	}

	style := lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Muted))
	if strings.HasPrefix(status, "Cannot restore:") {
		style = lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Error))
	}

	return padSnapshotLine(style.Render(display.Truncate(status, width)), width)
}

func (m *Model) snapshotHistoryFooter(width int) string {
	footer := "↑/↓ navigate  pgup/pgdn page  enter select  esc cancel  * latest"
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Muted))

	return padSnapshotLine(style.Render(display.Truncate(footer, width)), width)
}

func padSnapshotLine(line string, width int) string {
	line = truncateStyled(line, width)
	if lineWidth := lipgloss.Width(line); lineWidth < width {
		line += strings.Repeat(" ", width-lineWidth)
	}

	return line
}

func writeExactFramedLine(out *strings.Builder, line string, leftBorder string, rightBorder string) {
	out.WriteByte('\n')
	out.WriteString(leftBorder)
	out.WriteByte(' ')
	out.WriteString(line)
	out.WriteByte(' ')
	out.WriteString(rightBorder)
}

func truncateStyled(s string, width int) string {
	if width <= 0 {
		return ""
	}

	truncated := ansi.Truncate(s, width, "")
	if strings.Contains(truncated, "\x1b") && !strings.HasSuffix(truncated, "\x1b[0m") {
		truncated += "\x1b[0m"
	}

	return truncated
}
