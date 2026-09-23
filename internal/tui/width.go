package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

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
