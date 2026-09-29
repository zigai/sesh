package clibase

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
	prettytable "github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/samber/oops"
	"github.com/spf13/cobra"
)

const (
	defaultTableWidth    = 120
	headerRule           = "─"
	columnSpacing        = 2
	maxStackedLabelWidth = 24
	stackedLabelGutter   = 4
	stackedValueSpacing  = 2
)

type HumanWrapFunc func(string, int) []string

type TableColumn struct {
	Heading  string
	Fixed    bool
	MinWidth int
	Wrap     HumanWrapFunc
}

type sizedResponsiveColumn struct {
	heading string
	width   int
	wrap    HumanWrapFunc
}

func WriteTable(
	output io.Writer,
	header prettytable.Row,
	rows []prettytable.Row,
	columns []prettytable.ColumnConfig,
) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(output, "No results.")
		return err
	}

	writer := prettytable.NewWriter()

	style := prettytable.StyleDefault
	style.Box.PaddingLeft = ""
	style.Box.PaddingRight = strings.Repeat(" ", columnSpacing)
	style.Format.Header = text.FormatDefault
	style.Options = prettytable.OptionsNoBordersAndSeparators
	style.Options.SeparateHeader = true
	style.Box.MiddleHorizontal = headerRule

	writer.SetStyle(style)

	if len(columns) > 0 {
		writer.SetColumnConfigs(columns)
	}

	writer.AppendHeader(header)
	writer.AppendRows(rows)
	writer.SuppressTrailingSpaces()

	if _, err := fmt.Fprintln(output, trimHeaderRulePadding(writer.Render())); err != nil {
		return oops.In("cli").With("operation", "write_table").Wrap(err)
	}

	return nil
}

// trimHeaderRulePadding removes the last column's right padding from the header
// rule, which SuppressTrailingSpaces cannot trim because the rule has no spaces.
func trimHeaderRulePadding(rendered string) string {
	lines := strings.Split(rendered, "\n")

	for i, line := range lines {
		if line != "" && strings.Trim(line, headerRule) == "" {
			lines[i] = strings.TrimSuffix(line, strings.Repeat(headerRule, columnSpacing))

			break
		}
	}

	return strings.Join(lines, "\n")
}

func WriteResponsiveTable(cmd *cobra.Command, columns []TableColumn, rows []prettytable.Row, full bool) error {
	return CommandOutputError(RenderResponsiveTable(
		cmd.OutOrStdout(), CommandOutputWidth(cmd), columns, rows, full,
	))
}

func RenderResponsiveTable(
	output io.Writer,
	width int,
	columns []TableColumn,
	rows []prettytable.Row,
	full bool,
) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(output, "No results.")
		return err
	}

	if width <= 0 {
		width = defaultTableWidth
	}

	if full {
		sizedCols, fits := allocateFullResponsiveColumns(columns, rows, width)
		if !fits {
			return writeStackedResponsiveRows(output, columns, rows, width)
		}

		return writeWrappedResponsiveTable(output, sizedCols, rows)
	}

	sizedCols := allocateCompactResponsiveColumns(columns, rows, width)

	return writeCompactResponsiveTable(output, sizedCols, rows)
}

func WrapText(value string, width int) []string {
	return wrapDelimitedText(value, width, "")
}

func WrapIdentifier(value string, width int) []string {
	return wrapDelimitedText(value, width, "-_./")
}

func WrapSession(value string, width int) []string {
	if strings.Contains(value, " ") {
		return WrapText(value, width)
	}

	return WrapIdentifier(value, width)
}

func allocateCompactResponsiveColumns(columns []TableColumn, rows []prettytable.Row, maxWidth int) []sizedResponsiveColumn {
	if maxWidth <= 0 {
		maxWidth = defaultTableWidth
	}

	maxLen := measureResponsiveNaturalWidths(columns, rows)
	fixedTotal := max(0, len(columns)-1) * columnSpacing
	totalFlexNeeded := 0

	for i, col := range columns {
		if col.Fixed {
			fixedTotal += maxLen[i]
		} else {
			totalFlexNeeded += maxLen[i]
		}
	}

	available := maxWidth - fixedTotal
	out := make([]sizedResponsiveColumn, len(columns))

	for i, col := range columns {
		if col.Fixed {
			out[i] = sizedResponsiveColumn{heading: col.Heading, width: maxLen[i], wrap: col.Wrap}
			continue
		}

		headLen := ansi.StringWidth(col.Heading)
		width := headLen

		if available >= totalFlexNeeded {
			width = maxLen[i]
		} else if available > 0 && totalFlexNeeded > 0 {
			width = min(max(headLen, (available*maxLen[i])/totalFlexNeeded), maxLen[i])
		}

		out[i] = sizedResponsiveColumn{heading: col.Heading, width: width, wrap: col.Wrap}
	}

	return out
}

func allocateFullResponsiveColumns(columns []TableColumn, rows []prettytable.Row, maxWidth int) ([]sizedResponsiveColumn, bool) {
	if maxWidth <= 0 {
		maxWidth = defaultTableWidth
	}

	maxLen := measureResponsiveNaturalWidths(columns, rows)
	fixedTotal := max(0, len(columns)-1) * columnSpacing
	minFlexTotal := 0
	minWidths := make([]int, len(columns))

	for i, col := range columns {
		headLen := ansi.StringWidth(col.Heading)
		if col.Fixed {
			fixedTotal += maxLen[i]
			minWidths[i] = maxLen[i]
		} else {
			minW := max(headLen, min(maxLen[i], col.MinWidth))
			minWidths[i] = minW
			minFlexTotal += minW
		}
	}

	totalMinNeeded := fixedTotal + minFlexTotal
	if totalMinNeeded > maxWidth {
		return nil, false
	}

	extra := maxWidth - totalMinNeeded
	totalUnmet := 0

	for i, col := range columns {
		if !col.Fixed && maxLen[i] > minWidths[i] {
			totalUnmet += maxLen[i] - minWidths[i]
		}
	}

	out := make([]sizedResponsiveColumn, len(columns))

	for i, col := range columns {
		if col.Fixed {
			out[i] = sizedResponsiveColumn{heading: col.Heading, width: minWidths[i], wrap: col.Wrap}
			continue
		}

		add := 0
		unmet := maxLen[i] - minWidths[i]

		if extra > 0 && totalUnmet > 0 {
			add = min(unmet, (extra*unmet)/totalUnmet)
		}

		out[i] = sizedResponsiveColumn{heading: col.Heading, width: minWidths[i] + add, wrap: col.Wrap}
	}

	return out, true
}

func formatStackedResponsiveRecord(out *strings.Builder, columns []TableColumn, row prettytable.Row, labelWidth, valueWidth int) {
	for colIndex, col := range columns {
		cell := ""
		if colIndex < len(row) {
			cell = fmt.Sprint(row[colIndex])
		}

		label := ansi.Truncate(col.Heading+":", labelWidth, "")

		wrapCell := col.Wrap
		if wrapCell == nil {
			wrapCell = WrapText
		}

		lines := wrapCell(cell, valueWidth)
		for lineIndex, line := range lines {
			if lineIndex == 0 {
				fmt.Fprintf(out, "%s  %s\n", text.Pad(label, labelWidth, ' '), line)
			} else {
				fmt.Fprintf(out, "%s  %s\n", strings.Repeat(" ", labelWidth), line)
			}
		}
	}
}

func maxResponsiveLabelWidth(columns []TableColumn) int {
	labelWidth := 0
	for _, col := range columns {
		labelWidth = max(labelWidth, ansi.StringWidth(col.Heading)+1)
	}

	return labelWidth
}

func measureResponsiveNaturalWidths(columns []TableColumn, rows []prettytable.Row) []int {
	maxLen := make([]int, len(columns))
	for i, col := range columns {
		maxLen[i] = ansi.StringWidth(col.Heading)
	}

	for _, row := range rows {
		for i, cell := range row {
			if i < len(maxLen) {
				cellStr := sanitizeTableCell(fmt.Sprint(cell))
				maxLen[i] = max(maxLen[i], ansi.StringWidth(cellStr))
			}
		}
	}

	return maxLen
}

func newColumnConfig(number int, widthMax int, enforcer func(string, int) string) prettytable.ColumnConfig {
	var cfg prettytable.ColumnConfig

	cfg.Number = number
	cfg.WidthMax = widthMax
	cfg.WidthMaxEnforcer = enforcer

	return cfg
}

func sanitizeTableCell(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func wrapDelimitedText(value string, width int, delimiters string) []string {
	value = sanitizeTableCell(value)
	if value == "" {
		return []string{"-"}
	}

	if width <= 0 {
		return []string{value}
	}

	return strings.Split(ansi.Wrap(value, width, delimiters), "\n")
}

func writeCompactResponsiveTable(output io.Writer, columns []sizedResponsiveColumn, rows []prettytable.Row) error {
	header := make(prettytable.Row, len(columns))
	configs := make([]prettytable.ColumnConfig, len(columns))

	for i, col := range columns {
		header[i] = col.heading
		configs[i] = newColumnConfig(i+1, col.width, func(value string, maxLen int) string {
			return ansi.Truncate(sanitizeTableCell(value), maxLen, "…")
		})
	}

	return WriteTable(output, header, rows, configs)
}

func writeStackedResponsiveRows(output io.Writer, columns []TableColumn, rows []prettytable.Row, maxWidth int) error {
	if maxWidth <= 0 {
		maxWidth = defaultTableWidth
	}

	labelWidth := maxResponsiveLabelWidth(columns)
	labelWidth = min(labelWidth, maxStackedLabelWidth, max(1, maxWidth-stackedLabelGutter))
	valueWidth := max(1, maxWidth-labelWidth-stackedValueSpacing)

	var out strings.Builder

	for rowIndex, row := range rows {
		if rowIndex > 0 {
			out.WriteByte('\n')
		}

		formatStackedResponsiveRecord(&out, columns, row, labelWidth, valueWidth)
	}

	if _, err := fmt.Fprintf(output, "%s", out.String()); err != nil {
		return oops.In("cli").With("operation", "write_stacked_rows").Wrap(err)
	}

	return nil
}

func writeWrappedResponsiveTable(output io.Writer, columns []sizedResponsiveColumn, rows []prettytable.Row) error {
	header := make(prettytable.Row, len(columns))
	configs := make([]prettytable.ColumnConfig, len(columns))

	for i, col := range columns {
		header[i] = col.heading

		wrapCell := col.wrap
		if wrapCell == nil {
			wrapCell = WrapText
		}

		configs[i] = newColumnConfig(i+1, col.width, func(value string, maxLen int) string {
			return strings.Join(wrapCell(value, maxLen), "\n")
		})
	}

	return WriteTable(output, header, rows, configs)
}
