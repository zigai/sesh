package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
)

const (
	maxHelpOverlayWidth     = 72
	minHelpOverlayWidth     = 30
	titleHorizontalPadding  = 2
	boxHorizontalPadding    = 2
	extraHelpLines          = 2
	maxPathDialogWidth      = 72
	minPathDialogWidth      = 42
	pathDialogBorderPadding = 4
	confirmButtonWidth      = 8
	defaultDialogWidth      = 48
	helpWidthMargin         = 8
	pathDialogWidthMargin   = 10
	dialogWidthMargin       = 6
	centerDivisor           = 2
	innerFrameMargin        = 2
)

type helpBinding struct {
	key  string
	desc string
}

func (m *Model) helpBindings(keys actionKeyMap) []helpBinding {
	moveDescription := "Move between sessions"

	switch m.displayMode {
	case displayModeSesh:
	case displayModeAgents:
		moveDescription = "Move between agents"
	case displayModeInbox:
		moveDescription = "Move between agents"
	}

	bindings := []helpBinding{
		{"↑/↓, ctrl+n", moveDescription},
		{keys.pageUp.Help().Key + "/" + keys.pageDown.Help().Key, "Page up/down"},
		{keys.top.Help().Key + "/" + keys.bottom.Help().Key, "Jump to top/bottom"},
		{keys.enter.Help().Key, keys.enter.Help().Desc},
		m.sessionKeyHelpBinding(),
	}

	if m.showsDisplayModeIndicator() {
		bindings = append([]helpBinding{{keys.cycleMode.Help().Key, keys.cycleMode.Help().Desc}}, bindings...)
	}

	switch m.displayMode {
	case displayModeSesh:
	case displayModeAgents:
		bindings = append(
			bindings,
			helpBinding{keys.agentSort.Help().Key, keys.agentSort.Help().Desc},
			helpBinding{"ctrl+↑/ctrl+↓", "Move between tmux sessions"},
		)
	case displayModeInbox:
		bindings = append(
			bindings,
			helpBinding{keys.inboxGroup.Help().Key, keys.inboxGroup.Help().Desc},
			helpBinding{"ctrl+↑/ctrl+↓", "Move between groups"},
		)
	}

	bindings = append(bindings, helpBinding{keys.menu.Help().Key + ", right-click", keys.menu.Help().Desc})

	for _, bound := range m.viewActions(keys) {
		bindings = append(bindings, helpBinding{bound.key.Help().Key, bound.key.Help().Desc})
	}

	bindings = append(
		bindings,
		helpBinding{keys.close.Help().Key, keys.close.Help().Desc},
		helpBinding{keys.help.Help().Key, keys.help.Help().Desc},
	)

	return bindings
}

func (m *Model) renderHelpOverlay() string {
	contentWidth := m.contentWidth()
	width := min(max(contentWidth-helpWidthMargin, minHelpOverlayWidth), maxHelpOverlayWidth)
	keys := m.actionKeys()

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
	if m.pending == nil {
		return ""
	}

	descriptor := m.pending.entry.Describe()

	switch m.dialog {
	case dialogConfirm:
		question := descriptor.Title + "?"
		if names := m.subjectNames(m.pending.subject); names != "" {
			question = descriptor.Title + " " + names + "?"
		}

		return renderConfirmDialog(m.width, m.height, "Confirm", question, m.confirmYes, m.config)
	case dialogInputText:
		return renderDialog(m.width, m.height, descriptor.Title, m.pending.param.Usage+": "+m.textParamInput.View(), m.config)
	case dialogInputPath:
		return m.renderPathDialog(descriptor, *m.pending)
	case dialogNone:
		return ""
	}

	return ""
}

func (m *Model) subjectNames(subject action.Subject) string {
	if subject.Task != "" {
		for _, live := range m.tasks {
			if live.Record.ID == subject.Task {
				return SanitizeTerminalText(live.Record.Title)
			}
		}

		return SanitizeTerminalText(subject.Task)
	}

	names := make([]string, 0, len(subject.Sessions))
	for _, target := range subject.Sessions {
		names = append(names, SanitizeTerminalText(target.Name))
	}

	return strings.Join(names, ", ")
}

func enteredValues(descriptor action.Descriptor, request pendingAction) []string {
	lines := make([]string, 0, len(descriptor.Params))
	for _, param := range descriptor.Params {
		if param.Name != request.param.Name && request.values.Has(param.Name) {
			lines = append(lines, param.Usage+": "+SanitizeTerminalText(request.values.String(param.Name)))
		}
	}

	return lines
}

func (m *Model) renderPathDialog(descriptor action.Descriptor, request pendingAction) string {
	width := min(max(m.contentWidth()-pathDialogWidthMargin, minPathDialogWidth), maxPathDialogWidth)
	inputWidth := max(width-pathDialogBorderPadding, minSearchWidth)
	originalWidth := m.pathInput.Width()
	m.pathInput.SetWidth(inputWidth)
	input := m.pathInput.View()
	m.pathInput.SetWidth(originalWidth)

	lines := enteredValues(descriptor, request)
	if len(lines) > 0 {
		lines = append(lines, "")
	}

	lines = append(lines, request.param.Usage+":", input, "")

	start := min(m.pathSuggestionTop, len(m.pathSuggestions))

	end := min(start+m.visiblePathSuggestions(), len(m.pathSuggestions))
	for i := start; i < start+m.visiblePathSuggestions(); i++ {
		if i >= end {
			lines = append(lines, strings.Repeat(" ", inputWidth))
			continue
		}

		suggestion := m.pathSuggestions[i]
		prefix := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

		if i == m.pathSuggestionIndex {
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

	return box.Render(titleStyle.Render(descriptor.Title) + "\n\n" + strings.Join(lines, "\n"))
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
