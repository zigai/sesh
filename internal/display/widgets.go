package display

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zigai/sesh/internal/display/layout"
)

const (
	fractionPrefixWidth = 6
	widgetCountCap      = 99
	widgetPercentMax    = 100
	barSubcells         = 8
)

type WidgetFrameResolver interface {
	WidgetFrame() int
}

type WidgetActivityResolver interface {
	WidgetWorking() bool
}

func renderWidget(item token, resolver Resolver, value string, state layout.State) (string, string, layout.State) {
	if state == layout.Unsupported {
		return "", "", state
	}

	switch item.widget {
	case "bar":
		return renderBarWidget(item, value, state)
	case "todo_progress":
		return renderTodoWidget(item, resolver, value, state)
	case "spinner":
		return renderSpinnerWidget(item, resolver, value)
	case "badge":
		return renderBadgeWidget(item, value, state)
	default:
		return value, item.role, state
	}
}

func renderBarWidget(item token, value string, state layout.State) (string, string, layout.State) {
	if state == layout.Pending {
		return strings.Repeat("░", item.width), "muted", state
	}

	if state != layout.Present {
		return "", "", state
	}

	percent, err := strconv.Atoi(value)
	if err != nil {
		return "", "", layout.Absent
	}

	return percentageBar(percent, item.width), item.role, layout.Present
}

func renderTodoWidget(item token, resolver Resolver, value string, state layout.State) (string, string, layout.State) {
	other, otherState, _ := resolveCell(resolver, item.secondary)
	if otherState == layout.Unsupported {
		return "", "", layout.Unsupported
	}

	barWidth := item.width - fractionPrefixWidth
	if state == layout.Pending || otherState == layout.Pending {
		return "--/-- " + strings.Repeat("▱", barWidth), "muted", layout.Pending
	}

	done, doneErr := strconv.Atoi(value)

	total, totalErr := strconv.Atoi(other)
	if doneErr != nil || totalErr != nil || total <= 0 {
		return "", "", layout.Absent
	}

	done = min(max(done, 0), widgetCountCap)
	total = min(max(total, 0), widgetCountCap)
	filled := min(barWidth, done*barWidth/total)

	return fmt.Sprintf("%2d/%2d ", done, total) + strings.Repeat("▰", filled) + strings.Repeat("▱", barWidth-filled), item.role, layout.Present
}

func renderSpinnerWidget(item token, resolver Resolver, value string) (string, string, layout.State) {
	working := value == "running" || value == "WORKING"
	if source, ok := resolver.(WidgetActivityResolver); ok {
		working = source.WidgetWorking()
	}

	if !working {
		return "", "", layout.Absent
	}

	frame := 0
	if source, ok := resolver.(WidgetFrameResolver); ok {
		frame = source.WidgetFrame()
	}

	frames := []string{"⠋", "⠙", "⠹", "⠸"}

	return frames[((frame%len(frames))+len(frames))%len(frames)], item.role, layout.Present
}

func renderBadgeWidget(item token, value string, state layout.State) (string, string, layout.State) {
	if state != layout.Present {
		return "", "", state
	}

	count, err := strconv.Atoi(value)
	if err != nil || count <= 0 {
		return "", "", layout.Absent
	}

	role := ""
	if count > item.threshold {
		role = item.role
	}

	if count > widgetCountCap {
		return "99+", role, layout.Present
	}

	return strconv.Itoa(count), role, layout.Present
}

func percentageBar(percent, width int) string {
	percent = min(max(percent, 0), widgetPercentMax)
	units := percent * width * barSubcells / widgetPercentMax
	full, partial := units/barSubcells, units%barSubcells

	var out strings.Builder
	out.WriteString(strings.Repeat("█", full))

	if partial > 0 {
		pieces := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
		out.WriteString(pieces[partial])
	}

	out.WriteString(strings.Repeat("░", width-full-boolInt(partial > 0)))

	return out.String()
}

func boolInt(value bool) int {
	if value {
		return 1
	}

	return 0
}
