package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
)

const ellipsisLength = 3

func SanitizeTerminalText(s string) string {
	if s == "" {
		return ""
	}

	s = ansi.Strip(s)

	var b strings.Builder
	b.Grow(len(s))

	for _, r := range s {
		if r == '\x1b' || isBidiOrDangerousControl(r) {
			continue
		}

		if unicode.IsPrint(r) || r == ' ' {
			b.WriteRune(r)
		}
	}

	return b.String()
}

func formatAgentAgeAt(t time.Time, now time.Time) string {
	diff := now.Sub(t)
	if diff < time.Hour {
		return FormatActivityAt(t, now)
	}

	minutes := int(diff/time.Minute) % minutesPerHour
	hours := int(diff/time.Hour) % hoursPerDay
	days := int(diff / (hoursPerDay * time.Hour))

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}

	return fmt.Sprintf("%dh %dm", hours, minutes)
}

func formatAgentClockAt(t time.Time, now time.Time) string {
	return FormatClockAt(t, now)
}

func formatAgentStatusAgeAt(t time.Time, now time.Time, format string) string {
	if t.IsZero() {
		return ""
	}

	switch config.NormalizeStatusTimeFormat(format) {
	case "relative-seconds":
		return FormatElapsedAt(t, now)
	case "clock":
		return formatAgentClockAt(t, now)
	case "hh:mm":
		return FormatHoursMinutesAt(t, now)
	}

	return formatAgentAgeAt(t, now)
}

func truncateLeftWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}

	currentWidth := ansi.StringWidth(s)
	if currentWidth <= width {
		return s
	}

	if width <= ellipsisLength {
		return ansi.TruncateLeft(s, currentWidth-width, "")
	}

	return ansi.TruncateLeft(s, currentWidth-width+ellipsisLength, "...")
}

func isBidiOrDangerousControl(r rune) bool {
	if (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) {
		return true
	}

	if r < 0x20 || r == 0x7f {
		return true
	}

	if r >= 0x80 && r <= 0x9f {
		return true
	}

	return false
}
