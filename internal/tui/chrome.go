package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
)

// Chrome draws the picker frame, query line, rows and footer for pickers outside the session
// picker, with the same glyphs and theme.
type Chrome struct {
	cfg *config.Config
}

// Hint is one key and what it does, shown in the footer.
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

// BodyWidth is the width of the lines inside a frame that is width columns wide.
func (c Chrome) BodyWidth(width int) int {
	return max(width-popupBodyWidthMargin, 1)
}

// BodyHeight is the number of lines inside a frame that is height rows tall.
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

// Row draws a list row: the selection bar, the name, and a muted right column.
func (c Chrome) Row(name, right string, selected bool, width int) string {
	return c.TreeRow("", name, right, selected, width)
}

// TreeRow draws a list row whose name follows a muted tree prefix, like the agents under a
// session.
func (c Chrome) TreeRow(tree, name, right string, selected bool, width int) string {
	styles := rowStyles(c.cfg, inputGutterWidth(), width)
	treeWidth := lipgloss.Width(tree)

	rightWidth := lipgloss.Width(right)
	nameWidth := max(width-inputGutterWidth()-treeWidth-rightWidth-1, 1)

	if rightWidth == 0 || nameWidth < minSearchWidth {
		right = ""
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
		out.WriteString(c.selectedMuted(right))
	} else {
		out.WriteString(gap)
		out.WriteString(c.Muted(right))
	}

	return out.String()
}

// RowWidth is the width TreeRow needs to show tree, name and right in full.
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

// Blank is an empty body line.
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

// Footer draws key hints on the left and the mode badge on the right.
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

// Border colors text like the frame border, for popups that draw their own frame.
func (c Chrome) Border(text string) string {
	if defaultBorderTheme(c.cfg) {
		return ansiDefaultBorder + text + ansiReset
	}

	return lipgloss.NewStyle().Foreground(lipgloss.Color(c.cfg.Theme.Border)).Render(text)
}

// Hints draws key hints the way the footer does, without padding them to a width.
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

// Choice draws one choice of an inline row of choices, padded by a space on each side and
// highlighted like a selected row when it is the selected one.
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

// Label draws a field label: muted, or styled like a footer key when its field has focus.
func (c Chrome) Label(text string, focused bool) string {
	if focused {
		return ansiFooterKey + text + ansiReset
	}

	return c.Muted(text)
}

// Error colors text like a failed state.
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
