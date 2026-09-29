package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/session"
)

const (
	maxHelpOverlayWidth     = 72
	minHelpOverlayWidth     = 30
	titleHorizontalPadding  = 2
	boxHorizontalPadding    = 2
	extraHelpLines          = 2
	maxCreateDirDialogWidth = 72
	minCreateDirDialogWidth = 42
	createDirBorderPadding  = 4
	confirmButtonWidth      = 8
	defaultDialogWidth      = 48
	helpWidthMargin         = 8
	createDirWidthMargin    = 10
	dialogWidthMargin       = 6
	centerDivisor           = 2
	innerFrameMargin        = 2
)

type helpBinding struct {
	key  string
	desc string
}

func (m *Model) selectedRowIsSaved() bool {
	return m.cursor < len(m.filtered) && m.filtered[m.cursor].IsSaved()
}

func (m *Model) helpBindings(keys actionKeyMap) []helpBinding {
	moveDescription := "Move between sessions"
	if m.displayMode == displayModeAgents {
		moveDescription = "Move between agents"
	}

	bindings := []helpBinding{
		{"↑/↓, ctrl+n", moveDescription},
		{keys.pageUp.Help().Key + "/" + keys.pageDown.Help().Key, "Page up/down"},
		{keys.top.Help().Key + "/" + keys.bottom.Help().Key, "Jump to top/bottom"},
		{keys.enter.Help().Key, keys.enter.Help().Desc},
		{m.visibleSessionShortcutLabel(), "Open visible session or agent by slot"},
	}

	if m.showsDisplayModeIndicator() {
		bindings = append([]helpBinding{{keys.cycleMode.Help().Key, keys.cycleMode.Help().Desc}}, bindings...)
	}

	if m.displayMode == displayModeAgents {
		bindings = append(bindings,
			helpBinding{keys.agentSort.Help().Key, keys.agentSort.Help().Desc},
			helpBinding{"ctrl+↑/ctrl+↓", "Move between tmux sessions"},
		)
	}

	if !m.selectedRowIsSaved() {
		bindings = append(bindings,
			helpBinding{keys.rename.Help().Key, keys.rename.Help().Desc},
			helpBinding{keys.kill.Help().Key, keys.kill.Help().Desc},
		)
	}

	if m.cursor < len(m.filtered) &&
		m.filtered[m.cursor].Origin == session.OriginSSH &&
		!m.filtered[m.cursor].IsSaved() &&
		!m.selectedRowIsAgent() {
		bindings = append(bindings, helpBinding{keys.snapshot.Help().Key, keys.snapshot.Help().Desc})
	}

	bindings = append(bindings, helpBinding{keys.create.Help().Key, keys.create.Help().Desc})
	if !m.selectedRowIsAgent() {
		bindings = append(bindings, helpBinding{keys.delete.Help().Key, keys.delete.Help().Desc})
	}

	bindings = append(bindings,
		helpBinding{keys.close.Help().Key, keys.close.Help().Desc},
		helpBinding{keys.help.Help().Key, keys.help.Help().Desc},
	)

	return bindings
}

func (m *Model) renderHelpOverlay() string {
	contentWidth := m.contentWidth()
	width := min(max(contentWidth-helpWidthMargin, minHelpOverlayWidth), maxHelpOverlayWidth)
	keys := newActionKeyMap(m.config.Keys)

	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.config.Theme.Primary)).
		Bold(true).
		Background(lipgloss.Color("236")).
		Padding(0, titleHorizontalPadding).
		Render(" Popup Keys ")

	keyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(shortcutKeyColor)).
		Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.config.Theme.Muted))

	bindings := m.helpBindings(keys)
	lines := make([]string, 0, 1+len(bindings)+extraHelpLines)
	lines = append(lines, title)

	for _, binding := range bindings {
		if binding.key == "" {
			continue
		}

		key := keyStyle.Render(fmt.Sprintf("%-22s", binding.key))
		desc := descStyle.Render(binding.desc)
		lines = append(lines, "  "+key+"  "+desc)
	}

	if statusLine := m.renderHelpStatusLine(width); statusLine != "" {
		lines = append(lines, "", statusLine)
	}

	lines = append(lines, "", dimStyle.Render("  Press any key to dismiss"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.config.Theme.Border)).
		Padding(1, boxHorizontalPadding).
		Width(width).
		Render(strings.Join(lines, "\n"))

	return box
}

func (m *Model) renderHelpStatusLine(width int) string {
	if !m.hasStatusBadge() {
		return ""
	}

	glyph := "⚠ "
	color := ""

	if m.config != nil {
		color = m.config.Theme.Warning
	}

	if m.statusLevel == statusError {
		glyph = "✖ "

		if m.config != nil {
			color = m.config.Theme.Error
		}
	}

	statusStyle := lipgloss.NewStyle().Bold(true)
	if color != "" {
		statusStyle = statusStyle.Foreground(lipgloss.Color(color))
	}

	statusText := display.Truncate(glyph+SanitizeTerminalText(m.statusMessage), max(width-statusWidthInset, 1))

	return "  " + statusStyle.Render(statusText)
}

func (m *Model) renderDialog() string {
	switch m.dialog {
	case dialogConfirmKill:
		return renderConfirmDialog(m.width, m.height, "Confirm Kill", "Kill this session?", m.confirmYes, m.config)
	case dialogConfirmDelete:
		return renderConfirmDialog(m.width, m.height, "Confirm Delete", "Delete this session?", m.confirmYes, m.config)
	case dialogInputRename:
		return renderDialog(m.width, m.height, "Rename Session", "New name: "+m.renameInput.View(), m.config)
	case dialogInputCreateDir:
		return m.renderCreateDirDialog()
	case dialogNone:
		return ""
	}

	return ""
}

func (m *Model) renderCreateDirDialog() string {
	width := min(max(m.contentWidth()-createDirWidthMargin, minCreateDirDialogWidth), maxCreateDirDialogWidth)
	inputWidth := max(width-createDirBorderPadding, minSearchWidth)
	originalWidth := m.createDirInput.Width()
	m.createDirInput.SetWidth(inputWidth)
	input := m.createDirInput.View()
	m.createDirInput.SetWidth(originalWidth)

	lines := []string{
		"Session: " + SanitizeTerminalText(m.createSession),
		"",
		"Directory:",
		input,
		"",
	}

	start := min(m.createDirSuggestionTop, len(m.createDirSuggestions))

	end := min(start+m.visibleDirSuggestions(), len(m.createDirSuggestions))
	for i := start; i < start+m.visibleDirSuggestions(); i++ {
		if i >= end {
			lines = append(lines, strings.Repeat(" ", inputWidth))
			continue
		}

		suggestion := m.createDirSuggestions[i]
		prefix := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

		if i == m.createDirSuggestionIndex {
			prefix = "> "
			style = style.
				Foreground(lipgloss.Color("0")).
				Background(lipgloss.Color(m.config.Theme.Primary)).
				Bold(true)
		}

		suggestion = display.Truncate(SanitizeTerminalText(suggestion), inputWidth-lipgloss.Width(prefix))
		lines = append(lines, style.Render(prefix+suggestion))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(m.config.Theme.Border)).
		Width(width).
		Padding(1, boxHorizontalPadding)
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.config.Theme.Primary)).
		Bold(true)

	return box.Render(titleStyle.Render("Create Session") + "\n\n" + strings.Join(lines, "\n"))
}

func renderDialog(width, height int, title, content string, cfg *config.Config) string {
	_ = height

	border := lipgloss.Color(cfg.Theme.Border)
	primary := lipgloss.Color(cfg.Theme.Primary)

	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(border).
		Width(max(min(defaultDialogWidth, width-dialogWidthMargin), 1)).
		Padding(1, boxHorizontalPadding)

	titleStyle := lipgloss.NewStyle().Foreground(primary).Bold(true)

	return box.Render(titleStyle.Render(title) + "\n\n" + content)
}

func renderConfirmDialog(width, height int, title, content string, confirmYes bool, cfg *config.Config) string {
	buttons := renderConfirmButtons(confirmYes, cfg)

	return renderDialog(width, height, title, content+"\n\n"+buttons, cfg)
}

func renderConfirmButtons(confirmYes bool, cfg *config.Config) string {
	button := func(label string, selected bool) string {
		style := lipgloss.NewStyle().
			Width(confirmButtonWidth).
			Align(lipgloss.Center)

		if selected {
			style = style.
				Foreground(lipgloss.Color("0")).
				Background(lipgloss.Color(cfg.Theme.Primary)).
				Bold(true)
		} else {
			style = style.
				Foreground(lipgloss.Color("252")).
				Background(lipgloss.Color(cfg.Theme.Selected))
		}

		return style.Render(label)
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		button("Yes", confirmYes),
		" ",
		button("No", !confirmYes),
	)
}

func overlayCentered(base string, overlay string, width int, height int) string {
	if overlay == "" {
		return base
	}

	baseLines := strings.Split(base, "\n")
	for len(baseLines) < height {
		baseLines = append(baseLines, strings.Repeat(" ", width))
	}

	overlayLines := strings.Split(overlay, "\n")

	overlayWidth := 0
	for _, line := range overlayLines {
		overlayWidth = max(overlayWidth, lipgloss.Width(line))
	}

	innerWidth := max(width-innerFrameMargin, 1)
	x := max((innerWidth-overlayWidth)/centerDivisor, 0)
	y := max((height-len(overlayLines))/centerDivisor, 0)

	for i, overlayLine := range overlayLines {
		target := y + i
		if target >= len(baseLines) {
			break
		}

		baseLines[target] = overlayLineAt(baseLines[target], overlayLine, x+1, width)
	}

	return strings.Join(baseLines[:min(len(baseLines), height)], "\n")
}

func overlayLineAt(baseLine string, overlayLine string, x int, width int) string {
	if width <= 0 {
		return overlayLine
	}

	baseWidth := lipgloss.Width(baseLine)
	if baseWidth < width {
		baseLine += strings.Repeat(" ", width-baseWidth)
	}

	overlayWidth := lipgloss.Width(overlayLine)
	left := ansi.Cut(baseLine, 0, min(x, width))
	rightStart := min(max(x+overlayWidth, 0), width)
	right := ansi.Cut(baseLine, rightStart, width)

	return left + overlayLine + right
}
