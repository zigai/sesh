package layout

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

const (
	Absent State = iota
	Present
	Unsupported
	Pending
)

const (
	Left Alignment = iota
	Right
	Center
)

const (
	TruncateRight TruncateSide = iota
	TruncateLeft
)

const (
	Decay              = 30 * time.Second
	centerPaddingSides = 2
	sgrReset           = "\x1b[m"
	wideEllipsisWidth  = 3
)

type State uint8

type Alignment uint8

type TruncateSide uint8

type Cell struct {
	Text     string
	Role     string
	Variants []string
	State    State
}

type ColumnSpec struct {
	ID             string
	Align          Alignment
	Min            int
	Max            int
	Reserve        int
	Flex           bool
	Grow           bool
	ShrinkPriority int
	HidePriority   int
	TruncateSide   TruncateSide
	Group          string
	Separator      bool
}

type CellWidths struct {
	Natural int
	Compact int
}

type Column struct {
	Offset  int
	Width   int
	Visible bool
	Compact bool
}

type ColumnPlan struct {
	Width   int
	Columns []Column
	specs   []ColumnSpec
}

type Span struct {
	ID      string
	Offset  int
	Width   int
	Visible bool
}

type Theme map[string]string

type textToken struct {
	text   string
	width  int
	escape bool
}

type RatchetState struct {
	columns []ratchetColumn
}

type ratchetColumn struct {
	spec    ColumnSpec
	natural ratchetWidth
	compact ratchetWidth
}

type ratchetWidth struct {
	width       int
	narrowerAt  time.Time
	narrowerNow bool
}

func (r *RatchetState) Reset() {
	r.columns = nil
}

func Measure(specs []ColumnSpec, rows [][]Cell) [][]CellWidths {
	measured := make([][]CellWidths, len(rows))
	for i, row := range rows {
		measured[i] = make([]CellWidths, len(specs))
		for j, spec := range specs {
			if j >= len(row) || (row[j].State != Present && row[j].State != Pending) {
				continue
			}

			measured[i][j].Natural = boundedWidth(spec, ansi.StringWidth(row[j].Text))
			if len(row[j].Variants) > 0 {
				measured[i][j].Compact = boundedWidth(spec, ansi.StringWidth(row[j].Variants[0]))
			} else {
				measured[i][j].Compact = measured[i][j].Natural
			}
		}
	}

	return measured
}

func Plan(specs []ColumnSpec, measured [][]CellWidths, width int, ratchet *RatchetState, now time.Time) ColumnPlan {
	width = max(width, 0)
	columns := make([]Column, len(specs))
	compact := make([]int, len(specs))

	if ratchet != nil && len(ratchet.columns) != len(specs) {
		ratchet.columns = make([]ratchetColumn, len(specs))
	}

	for i, spec := range specs {
		natural, shorter := measureColumn(spec, measured, i)

		if ratchet != nil {
			column := ratchet.column(i, spec)
			natural = column.natural.observe(natural, now)
			shorter = column.compact.observe(shorter, now)
		}

		compact[i] = min(shorter, natural)

		columns[i] = Column{Offset: 0, Width: natural, Visible: natural > 0, Compact: false}
	}

	total := columnTotal(columns)
	if total > width {
		total = shrinkFlexible(specs, columns, width, total)
	}

	if total > width {
		total = useCompact(specs, columns, compact, width, total)
	}

	if total > width {
		hideOverflow(specs, columns, width, total)
	}

	total = columnTotal(columns)
	if total < width {
		for i, spec := range specs {
			if spec.Grow {
				columns[i].Width += width - total
				columns[i].Visible = true

				break
			}
		}
	}

	offset := 0
	for i := range columns {
		columns[i].Offset = offset
		offset += columns[i].Width
	}

	return ColumnPlan{Width: width, Columns: columns, specs: slices.Clone(specs)}
}

func (r *RatchetState) column(index int, spec ColumnSpec) *ratchetColumn {
	if r.columns[index].spec != spec {
		r.columns[index] = ratchetColumn{spec: spec, natural: ratchetWidth{width: 0, narrowerAt: time.Time{}, narrowerNow: false}, compact: ratchetWidth{width: 0, narrowerAt: time.Time{}, narrowerNow: false}}
	}

	return &r.columns[index]
}

func (r *ratchetWidth) observe(natural int, now time.Time) int {
	if r.width == 0 || natural >= r.width {
		r.width = natural
		r.narrowerNow = false

		return r.width
	}

	if !r.narrowerNow {
		r.narrowerAt = now
		r.narrowerNow = true
	} else if !now.Before(r.narrowerAt) && now.Sub(r.narrowerAt) >= Decay {
		r.width = natural
		r.narrowerNow = false
	}

	return r.width
}

func PaintColumns(plan ColumnPlan, cells []Cell, theme Theme) ([]string, []Span) {
	spans := make([]Span, len(plan.Columns))
	parts := make([]string, len(plan.Columns))

	for i, column := range plan.Columns {
		spans[i] = Span{ID: plan.specs[i].ID, Offset: column.Offset, Width: column.Width, Visible: column.Visible}
		if !column.Visible {
			continue
		}

		parts[i] = paintColumn(plan, cells, i, theme)
	}

	used := columnTotal(plan.Columns)
	if used < plan.Width {
		padding := strings.Repeat(" ", plan.Width-used)
		if len(parts) == 0 {
			return []string{padding}, spans
		}

		parts[len(parts)-1] += padding
	}

	return parts, spans
}

func TruncateLeftWithMark(value string, width int, mark string) string {
	if width <= ansi.StringWidth(mark) {
		return mark
	}

	return mark + keepSuffix(textTokens(value), width-ansi.StringWidth(mark))
}

func measureColumn(spec ColumnSpec, measured [][]CellWidths, index int) (int, int) {
	natural := max(spec.Reserve, spec.Min, 0)
	compact := max(spec.Reserve, 0)

	for _, row := range measured {
		if index < len(row) {
			natural = max(natural, row[index].Natural)
			compact = max(compact, row[index].Compact)
		}
	}

	natural = boundedWidth(spec, natural)
	compact = min(boundedWidth(spec, compact), natural)

	return natural, compact
}

func shrinkFlexible(specs []ColumnSpec, columns []Column, width, total int) int {
	for _, i := range priorityOrder(specs, true) {
		if !specs[i].Flex || total <= width {
			continue
		}

		minimum := boundedWidth(specs[i], max(specs[i].Min, 0))

		shrink := min(columns[i].Width-minimum, total-width)
		if shrink > 0 {
			columns[i].Width -= shrink
			columns[i].Visible = columns[i].Width > 0
			total -= shrink
		}
	}

	return total
}

func useCompact(specs []ColumnSpec, columns []Column, compact []int, width, total int) int {
	for _, i := range priorityOrder(specs, true) {
		if compact[i] >= columns[i].Width {
			continue
		}

		total -= columns[i].Width - compact[i]
		columns[i].Width = compact[i]
		columns[i].Compact = true
		columns[i].Visible = compact[i] > 0

		if total <= width {
			break
		}
	}

	return total
}

func hideOverflow(specs []ColumnSpec, columns []Column, width, total int) {
	for _, i := range priorityOrder(specs, false) {
		total -= columns[i].Width
		columns[i].Width = 0
		columns[i].Visible = false

		if total <= width {
			return
		}
	}
}

func paintColumn(plan ColumnPlan, cells []Cell, index int, theme Theme) string {
	column := plan.Columns[index]
	spec := plan.specs[index]

	var cell Cell
	if index < len(cells) {
		cell = cells[index]
	}

	value := cellText(plan, cells, index)
	if ansi.StringWidth(value) > column.Width {
		value = truncate(value, column.Width, spec.TruncateSide)
	}

	padding := max(column.Width-ansi.StringWidth(value), 0)
	left := 0

	switch spec.Align {
	case Right:
		left = padding
	case Center:
		left = padding / centerPaddingSides
	case Left:
		left = 0
	}

	var out strings.Builder
	out.WriteString(strings.Repeat(" ", left))

	styled := strings.Contains(value, "\x1b")

	if color := theme[cell.Role]; color != "" && value != "" {
		open := "\x1b[" + color + "m"
		out.WriteString(open)
		writeWithBaseStyle(&out, value, open)
		out.WriteString(sgrReset)
	} else {
		out.WriteString(value)

		if styled {
			out.WriteString(sgrReset)
		}
	}

	out.WriteString(strings.Repeat(" ", padding-left))

	return out.String()
}

func cellText(plan ColumnPlan, cells []Cell, index int) string {
	if index >= len(cells) {
		return ""
	}

	cell := cells[index]
	if cell.State != Present && cell.State != Pending {
		return ""
	}

	if group := plan.specs[index].Group; group != "" && !groupHasPresentField(plan, cells, group) {
		return ""
	}

	if plan.specs[index].Separator && !separatorPresent(plan, cells, index) {
		return ""
	}

	if plan.Columns[index].Compact && len(cell.Variants) > 0 {
		return cell.Variants[0]
	}

	return cell.Text
}

func groupHasPresentField(plan ColumnPlan, cells []Cell, group string) bool {
	for i, spec := range plan.specs {
		if spec.Group == group && presentGroupField(plan, cells, i) {
			return true
		}
	}

	return false
}

func truncate(value string, width int, side TruncateSide) string {
	if width <= 0 {
		return ""
	}

	mark := "…"
	if width >= wideEllipsisWidth {
		mark = "..."
	}

	if width <= ansi.StringWidth(mark) {
		return mark
	}

	if side == TruncateLeft {
		return TruncateLeftWithMark(value, width, mark)
	}

	return keepPrefix(textTokens(value), width-ansi.StringWidth(mark)) + mark
}

func textTokens(value string) []textToken {
	tokens := make([]textToken, 0, len(value))

	var (
		state byte
		plain strings.Builder
	)

	flushPlain := func() {
		graphemes := uniseg.NewGraphemes(plain.String())
		for graphemes.Next() {
			tokens = append(tokens, textToken{text: graphemes.Str(), width: ansi.StringWidth(graphemes.Str()), escape: false})
		}

		plain.Reset()
	}

	for value != "" {
		seq, _, n, newState := ansi.DecodeSequence(value, state, nil)
		state = newState
		value = value[n:]

		if !strings.HasPrefix(seq, "\x1b") {
			plain.WriteString(seq)
			continue
		}

		flushPlain()

		tokens = append(tokens, textToken{text: seq, width: 0, escape: true})
	}

	flushPlain()

	return tokens
}

func keepPrefix(tokens []textToken, remaining int) string {
	var (
		result strings.Builder
		styled bool
	)

	for _, token := range tokens {
		if !token.escape && token.width > remaining {
			break
		}

		result.WriteString(token.text)

		remaining -= token.width
		styled = styled || token.escape
	}

	if styled {
		result.WriteString(sgrReset)
	}

	return result.String()
}

func keepSuffix(tokens []textToken, remaining int) string {
	start := len(tokens)

	for i, token := range slices.Backward(tokens) {
		if token.escape {
			continue
		}

		if token.width > remaining {
			break
		}

		remaining -= token.width
		start = i
	}

	var (
		result strings.Builder
		styled bool
	)

	for i, token := range tokens {
		if i >= start || token.escape {
			result.WriteString(token.text)
			styled = styled || token.escape
		}
	}

	if styled {
		result.WriteString(sgrReset)
	}

	return result.String()
}

func writeWithBaseStyle(out *strings.Builder, value, base string) {
	for value != "" {
		index := strings.Index(value, "\x1b[")
		if index < 0 {
			out.WriteString(value)
			return
		}

		out.WriteString(value[:index])
		value = value[index:]

		end := strings.IndexByte(value, 'm')
		if end < 0 {
			out.WriteString(value)
			return
		}

		seq := value[:end+1]
		value = value[end+1:]

		out.WriteString(seq)

		if seq == sgrReset || seq == "\x1b[0m" {
			out.WriteString(base)
		}
	}
}

func separatorPresent(plan ColumnPlan, cells []Cell, index int) bool {
	group := plan.specs[index].Group
	if group == "" {
		return true
	}

	before, after := groupNeighbors(plan, cells, group, index)
	if before < 0 || after < 0 {
		return false
	}

	for i := before + 1; i < index; i++ {
		if plan.specs[i].Group == group && plan.specs[i].Separator && plan.Columns[i].Visible {
			return false
		}
	}

	return true
}

func groupNeighbors(plan ColumnPlan, cells []Cell, group string, index int) (int, int) {
	before, after := -1, -1

	for i, spec := range plan.specs {
		if spec.Group != group || !presentGroupField(plan, cells, i) {
			continue
		}

		if i < index {
			before = i
		} else if i > index && after < 0 {
			after = i
		}
	}

	return before, after
}

func presentGroupField(plan ColumnPlan, cells []Cell, index int) bool {
	return !plan.specs[index].Separator && plan.Columns[index].Visible && index < len(cells) && cells[index].State == Present && cells[index].Text != ""
}

func boundedWidth(spec ColumnSpec, width int) int {
	width = max(width, 0)
	if spec.Max > 0 {
		width = min(width, spec.Max)
	}

	return width
}

func columnTotal(columns []Column) int {
	total := 0
	for _, column := range columns {
		total += column.Width
	}

	return total
}

func priorityOrder(specs []ColumnSpec, shrink bool) []int {
	order := make([]int, len(specs))
	for i := range order {
		order[i] = i
	}

	slices.SortStableFunc(order, func(a, b int) int {
		first, second := specs[a].HidePriority, specs[b].HidePriority
		if shrink {
			first, second = specs[a].ShrinkPriority, specs[b].ShrinkPriority
		}

		return cmp.Compare(second, first)
	})

	return order
}
