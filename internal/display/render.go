package display

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	ellipsisWidth = 3
	centerDivisor = 2
)

func AlignPart(part Part) string {
	value := TruncatePart(part)

	valueWidth := ansi.StringWidth(value)
	paddingWidth := max(part.Width-valueWidth, 0)

	if paddingWidth == 0 {
		return value
	}

	switch part.Alignment {
	case AlignmentRight:
		return strings.Repeat(" ", paddingWidth) + value
	case AlignmentCenter:
		leftWidth := paddingWidth / centerDivisor
		return strings.Repeat(" ", leftWidth) + value + strings.Repeat(" ", paddingWidth-leftWidth)
	case AlignmentNone, AlignmentLeft:
		return value + strings.Repeat(" ", paddingWidth)
	default:
		return value
	}
}

func TruncatePart(part Part) string {
	if !part.HasMaxWidth || ansi.StringWidth(part.Value) <= part.MaxWidth {
		return part.Value
	}

	if part.Alignment == AlignmentRight {
		return TruncateLeft(part.Value, part.MaxWidth)
	}

	return Truncate(part.Value, part.MaxWidth)
}

func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}

	if width <= ellipsisWidth {
		return ansi.Truncate(s, width, "")
	}

	return ansi.Truncate(s, width, "...")
}

func TruncateLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}

	currentWidth := ansi.StringWidth(s)
	if currentWidth <= width {
		return s
	}

	if width <= ellipsisWidth {
		return ansi.TruncateLeft(s, currentWidth-width, "")
	}

	return ansi.TruncateLeft(s, currentWidth-width+ellipsisWidth, "...")
}

func StringWidth(s string) int {
	return ansi.StringWidth(s)
}

func (p Part) Render() string {
	return AlignPart(p)
}

func (p Part) String() string {
	return AlignPart(p)
}

func (l Line) RenderLeft() string {
	var b strings.Builder
	for _, p := range l.Left {
		b.WriteString(AlignPart(p))
	}

	return b.String()
}

func (l Line) RenderRight() string {
	var b strings.Builder
	for _, p := range l.Right {
		b.WriteString(AlignPart(p))
	}

	return b.String()
}
