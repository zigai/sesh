package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
)

const (
	SpanMuted SpanRole = iota
	SpanPrimary
	SpanSecondary
	SpanSuccess
	SpanError
)

const GitGlyph = "\ue725"

type Chrome struct {
	cfg *config.Config
}

type Span struct {
	Text string
	Role SpanRole
}

type SpanRole int

type Hint struct {
	Key  string
	Desc string
}

func NewChrome(cfg *config.Config) Chrome {
	return Chrome{cfg: cfg}
}

func (c Chrome) NewTextInput() textinput.Model {
	return newTextInput(newTextInputStyles(c.cfg))
}

func (c Chrome) BodyWidth(width int) int {
	return max(width-popupBodyWidthMargin, 1)
}

func (c Chrome) BodyHeight(height int) int {
	return max(height-popupBodyHeightMargin, 1)
}

// Frame draws the border around lines, each already bodyWidth wide.
func (c Chrome) Frame(bodyWidth int, lines []string) string {
	horizontal := strings.Repeat("─", bodyWidth+frameBorderPadding)
	top, bottom, left, right := frameBorders(c.cfg, horizontal)

	var out strings.Builder
	out.WriteString(top)

	for _, line := range lines {
		writeExactFramedLine(&out, line, left, right)
	}

	out.WriteByte('\n')
	out.WriteString(bottom)

	return out.String()
}

func (c Chrome) InputBar(input *textinput.Model, width int) string {
	return inputBar(input, width)
}

func (c Chrome) Row(name, right string, selected bool, width int) string {
	return c.TreeRow("", name, right, selected, width)
}

func (c Chrome) TreeRow(tree, name, right string, selected bool, width int) string {
	return c.TreeRowSpans(tree, name, []Span{{Text: right, Role: SpanMuted}}, selected, width)
}

func (c Chrome) TreeRowSpans(tree, name string, spans []Span, selected bool, width int) string {
	styles := rowStyles(c.cfg, inputGutterWidth(), width)
	treeWidth := lipgloss.Width(tree)

	rightWidth := SpansWidth(spans)

	nameWidth := max(width-inputGutterWidth()-treeWidth-rightWidth-1, 1)

	if rightWidth == 0 || nameWidth < minSearchWidth {
		rightWidth = 0
		nameWidth = max(width-inputGutterWidth()-treeWidth, 1)
	}

	name = display.Truncate(name, nameWidth)
	name += strings.Repeat(" ", max(nameWidth-lipgloss.Width(name), 0))

	var out strings.Builder

	switch {
	case selected && tree != "":
		out.WriteString(styles.selectedBar + c.selectedMuted(tree))
	case selected:
		out.WriteString(styles.selectedBar)
	case tree != "":
		out.WriteString(styles.blankPrefix + c.Muted(tree))
	default:
		out.WriteString(styles.blankPrefix)
	}

	nameSeq := styles.nameSeq
	selectedSeq := styles.selectedNameSeq

	if styles.fastDefaultTheme {
		nameSeq = ansiDefaultName
		selectedSeq = ansiDefaultSelected
	}

	if selected {
		writeStyledWithBackground(&out, name, selectedSeq)
	} else {
		writeStyledWithBackground(&out, name, nameSeq)
	}

	if rightWidth == 0 {
		return out.String()
	}

	gap := " "
	if selected {
		writeStyledWithBackground(&out, gap, styles.selectedBgSeq)
	} else {
		out.WriteString(gap)
	}

	c.writeSpans(&out, spans, selected)

	return out.String()
}

func SpansWidth(spans []Span) int {
	width := 0
	for _, span := range spans {
		width += lipgloss.Width(span.Text)
	}

	return width
}

func (c Chrome) RowWidth(tree, name, right string) int {
	if right == "" {
		return inputGutterWidth() + lipgloss.Width(tree) + lipgloss.Width(name)
	}

	return inputGutterWidth() + lipgloss.Width(tree) + max(lipgloss.Width(name), minSearchWidth) + 1 + lipgloss.Width(right)
}

func (c Chrome) Muted(text string) string {
	if defaultMutedTheme(c.cfg) {
		return ansiDefaultMuted + text + ansiReset
	}

	return lipgloss.NewStyle().Foreground(lipgloss.Color(c.cfg.Theme.Muted)).Render(text)
}

func (c Chrome) Blank(width int) string {
	return strings.Repeat(" ", max(width, 0))
}

// Pad fills a styled line to width, truncating it when it is wider.
func (c Chrome) Pad(line string, width int) string {
	line = truncateStyled(line, width)
	if lineWidth := lipgloss.Width(line); lineWidth < width {
		line += strings.Repeat(" ", width-lineWidth)
	}

	return line
}

func (c Chrome) Footer(hints []Hint, badge string, width int) string {
	label := modeBadge(c.cfg, badge)
	labelWidth := lipgloss.Width(label)

	if labelWidth >= width {
		return truncateStyled(label, width)
	}

	parts := make([]footerPart, 0, len(hints))
	for _, hint := range hints {
		parts = append(parts, footerPart{
			key: hint.Key, desc: hint.Desc, width: lipgloss.Width(hint.Key) + 1 + lipgloss.Width(hint.Desc), binding: action.Binding{Key: "", Action: "", In: "", Table: "", Values: nil},
		})
	}

	hintWidth := max(width-labelWidth-frameBorderPadding, 0)

	return renderFooterLine(parts, hintWidth, "") + strings.Repeat(" ", width-labelWidth-hintWidth) + label
}

func (c Chrome) Border(text string) string {
	if defaultBorderTheme(c.cfg) {
		return ansiDefaultBorder + text + ansiReset
	}

	return lipgloss.NewStyle().Foreground(lipgloss.Color(c.cfg.Theme.Border)).Render(text)
}

func (c Chrome) Hints(hints []Hint) string {
	var out strings.Builder

	for index, hint := range hints {
		if index > 0 {
			out.WriteString("  ")
		}

		writeFooterPart(&out, footerPart{
			key: hint.Key, desc: hint.Desc, width: 0, binding: action.Binding{Key: "", Action: "", In: "", Table: "", Values: nil},
		}, false)
	}

	return out.String()
}

func (c Chrome) Choice(text string, selected bool) string {
	styles := rowStyles(c.cfg, 0, 0)

	seq := styles.nameSeq
	if selected {
		seq = styles.selectedNameSeq
	}

	if styles.fastDefaultTheme {
		seq = ansiDefaultName
		if selected {
			seq = ansiDefaultSelected
		}
	}

	var out strings.Builder
	writeStyledWithBackground(&out, " "+text+" ", seq)

	return out.String()
}

func (c Chrome) Label(text string, focused bool) string {
	if focused {
		return ansiFooterKey + text + ansiReset
	}

	return c.Muted(text)
}

func (c Chrome) Error(text string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c.cfg.Theme.Error)).Render(text)
}

func (c Chrome) selectedMuted(text string) string {
	if defaultRowTheme(c.cfg) && defaultMutedTheme(c.cfg) {
		return ansiDefaultSelectedBg + ansiDefaultMuted + text + ansiReset
	}

	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(c.cfg.Theme.Muted)).
		Background(lipgloss.Color(c.cfg.Theme.Selected)).
		Render(text)
}

func (c Chrome) writeSpans(out *strings.Builder, spans []Span, selected bool) {
	for _, span := range spans {
		out.WriteString(c.span(span, selected))
	}
}

func (c Chrome) span(span Span, selected bool) string {
	var color string

	switch span.Role {
	case SpanMuted:
		if selected {
			return c.selectedMuted(span.Text)
		}

		return c.Muted(span.Text)
	case SpanPrimary:
		color = c.cfg.Theme.Primary
	case SpanSecondary:
		color = c.cfg.Theme.Secondary
	case SpanSuccess:
		color = c.cfg.Theme.Success
	case SpanError:
		color = c.cfg.Theme.Error
	}

	style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	if selected {
		style = style.Background(lipgloss.Color(c.cfg.Theme.Selected))
	}

	return style.Render(span.Text)
}
