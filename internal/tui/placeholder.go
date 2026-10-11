package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

const centered = 2

// SleepPlaceholder draws the screen a sleeping task's agent pane shows: the sleeping glyph and
// harness centered, and below them the last wake failure, if any.
func SleepPlaceholder(cfg *config.Config, harness string, failure string, width, height int) string {
	chrome := NewChrome(cfg)
	glyph := lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.ResolvedAgentStatusColors().Idle)).
		Render(agentStatusGlyphChar(session.AgentStatusSleeping))

	lines := []string{glyph + " " + chrome.Muted("sleeping · "+harness+" · ") + "⏎" + chrome.Muted(" wake")}
	if failure != "" {
		lines = append(lines, "", lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.Theme.Error)).Render(failure))
	}

	var out strings.Builder

	top := max((height-len(lines))/centered, 0)
	out.WriteString(strings.Repeat("\r\n", top))

	for index, line := range lines {
		if index > 0 {
			out.WriteString("\r\n")
		}

		line = truncateStyled(line, width)
		out.WriteString(strings.Repeat(" ", max((width-ansi.StringWidth(line))/centered, 0)))
		out.WriteString(line)
	}

	return out.String()
}
