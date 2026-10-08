package layout

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"pgregory.net/rapid"
)

func generatedTable(t *rapid.T) ([]ColumnSpec, [][]Cell, int) {
	count := rapid.IntRange(1, 8).Draw(t, "columns")
	rows := rapid.IntRange(1, 12).Draw(t, "rows")
	width := rapid.IntRange(0, 90).Draw(t, "width")
	specs := make([]ColumnSpec, count)
	cells := make([][]Cell, rows)

	for i := range specs {
		specs[i] = ColumnSpec{
			ID:             string(rune('a' + i)),
			Min:            rapid.IntRange(0, 3).Draw(t, "min"),
			Reserve:        rapid.IntRange(0, 3).Draw(t, "reserve"),
			Flex:           rapid.Bool().Draw(t, "flex"),
			ShrinkPriority: rapid.IntRange(0, 5).Draw(t, "shrink priority"),
			HidePriority:   rapid.IntRange(0, 5).Draw(t, "hide priority"),
		}
	}

	for i := range cells {
		cells[i] = make([]Cell, count)
		for j := range cells[i] {
			value := rapid.StringMatching(`[a-z]{0,12}`).Draw(t, "value")
			cells[i][j] = Cell{Text: value, State: Present, Variants: []string{"x"}}
		}
	}

	return specs, cells, width
}

func TestEveryPaintedRowHasRequestedDisplayWidth(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		specs, rows, width := generatedTable(t)

		plan := Plan(specs, Measure(specs, rows), width, nil, time.Time{})
		for _, row := range rows {
			painted, _ := Paint(plan, row, nil)
			if got := ansi.StringWidth(painted); got != width {
				t.Fatalf("paint width = %d, want %d: %q", got, width, painted)
			}
		}
	})
}

func TestColumnOffsetsMatchAcrossEveryRow(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		specs, rows, width := generatedTable(t)

		plan := Plan(specs, Measure(specs, rows), width, nil, time.Time{})
		for _, row := range rows {
			_, spans := Paint(plan, row, nil)
			for i, span := range spans {
				if span.Offset != plan.Columns[i].Offset || span.Width != plan.Columns[i].Width {
					t.Fatalf("span %d = %+v, column = %+v", i, span, plan.Columns[i])
				}
			}
		}
	})
}

func TestFittingUpdatePreservesOffsets(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		specs, rows, width := generatedTable(t)
		state := &RatchetState{}
		start := time.Unix(1, 0)
		plan := Plan(specs, Measure(specs, rows), width, state, start)
		column := rapid.IntRange(0, len(specs)-1).Draw(t, "changed column")
		rows[0][column].Text = ""

		next := Plan(specs, Measure(specs, rows), width, state, start.Add(time.Second))
		for i := range plan.Columns {
			if next.Columns[i].Offset != plan.Columns[i].Offset {
				t.Fatalf("column %d offset changed despite the fitting update", i)
			}
		}
	})
}

func TestProviderUpdateOnlyChangesOwnedCells(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		specs, rows, _ := generatedTable(t)
		column := rapid.IntRange(0, len(specs)-1).Draw(t, "provider column")
		state := &RatchetState{}
		start := time.Unix(1, 0)
		plan := Plan(specs, Measure(specs, rows), 200, state, start)
		before, _ := Paint(plan, rows[0], nil)
		rows[0][column].Text = "updated"
		rows[0][column].Variants = []string{"u"}
		next := Plan(specs, Measure(specs, rows), 200, state, start.Add(time.Second))
		after, _ := Paint(next, rows[0], nil)

		assertUnownedColumns(t, plan, next, before, after, column)
	})
}

func assertUnownedColumns(t *rapid.T, plan, next ColumnPlan, before, after string, changed int) {
	for i, span := range plan.Columns {
		if i == changed || !span.Visible {
			continue
		}

		if next.Columns[i].Width != span.Width {
			t.Fatalf("unowned column %d changed width: %+v to %+v", i, span, next.Columns[i])
		}

		left := ansi.Cut(before, span.Offset, span.Offset+span.Width)

		right := ansi.Cut(after, next.Columns[i].Offset, next.Columns[i].Offset+span.Width)
		if left != right {
			t.Fatalf("unowned column %d changed: %q to %q", i, left, right)
		}
	}

	if next.Columns[changed].Width == plan.Columns[changed].Width {
		for i := range plan.Columns {
			if next.Columns[i].Offset != plan.Columns[i].Offset {
				t.Fatalf("offset %d changed without provider width growth", i)
			}
		}
	}
}

func TestMoreAvailableWidthKeepsVisibleColumns(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		specs, rows, width := generatedTable(t)
		measured := Measure(specs, rows)
		small := Plan(specs, measured, width, nil, time.Time{})

		large := Plan(specs, measured, width+rapid.IntRange(0, 40).Draw(t, "increase"), nil, time.Time{})
		for i, column := range small.Columns {
			if column.Visible && (!large.Columns[i].Visible || large.Columns[i].Width < column.Width) {
				t.Fatalf("column %d narrowed: %+v to %+v", i, column, large.Columns[i])
			}
		}
	})
}

func TestTruncationKeepsGraphemesWhole(t *testing.T) {
	data, err := os.ReadFile("testdata/graphemes.txt")
	if err != nil {
		t.Fatal(err)
	}

	values := strings.Split(strings.TrimSpace(string(data)), "\n")

	rapid.Check(t, func(t *rapid.T) {
		width := rapid.IntRange(1, 20).Draw(t, "width")
		value := rapid.SampledFrom(values).Draw(t, "value")
		side := rapid.SampledFrom([]TruncateSide{TruncateRight, TruncateLeft}).Draw(t, "side")
		specs := []ColumnSpec{{ID: "value", Min: width, Max: width, TruncateSide: side}}
		rows := [][]Cell{{{Text: value, State: Present}}}
		plan := Plan(specs, Measure(specs, rows), width, nil, time.Time{})
		painted, _ := Paint(plan, rows[0], nil)

		trimmed := strings.TrimSpace(painted)
		if ansi.StringWidth(painted) != width || strings.ContainsRune(trimmed, '\ufffd') {
			t.Fatalf("broken truncation: %q", painted)
		}

		if ansi.StringWidth(value) > width && !strings.Contains(trimmed, "…") && !strings.Contains(trimmed, "...") {
			t.Fatalf("missing ellipsis: %q", painted)
		}

		if ansi.StringWidth(value) > width && !wholeGraphemeTruncation(value, trimmed, side) {
			t.Fatalf("truncation split a grapheme: %q from %q", trimmed, value)
		}
	})
}

func wholeGraphemeTruncation(value, painted string, side TruncateSide) bool {
	mark := "…"
	if strings.Contains(painted, "...") {
		mark = "..."
	}

	graphemes := uniseg.NewGraphemes(value)

	parts := make([]string, 0)
	for graphemes.Next() {
		parts = append(parts, graphemes.Str())
	}

	for length := 0; length <= len(parts); length++ {
		if side == TruncateLeft && painted == mark+strings.Join(parts[len(parts)-length:], "") {
			return true
		}

		if side == TruncateRight && painted == strings.Join(parts[:length], "")+mark {
			return true
		}
	}

	return false
}

func TestGroupSeparatorsAppearOnlyBetweenPresentFields(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		first := rapid.Bool().Draw(t, "first present")
		second := rapid.Bool().Draw(t, "second present")
		third := rapid.Bool().Draw(t, "third present")
		specs := []ColumnSpec{
			{ID: "a", Group: "g"},
			{ID: "sep1", Group: "g", Separator: true},
			{ID: "b", Group: "g"},
			{ID: "sep2", Group: "g", Separator: true},
			{ID: "c", Group: "g"},
		}
		rows := [][]Cell{{
			{Text: "a", State: boolState(first)},
			{Text: "/", State: Present},
			{Text: "b", State: boolState(second)},
			{Text: "/", State: Present},
			{Text: "c", State: boolState(third)},
		}}
		painted, _ := Paint(Plan(specs, Measure(specs, rows), 10, nil, time.Time{}), rows[0], nil)

		trimmed := strings.TrimSpace(painted)
		if strings.HasPrefix(trimmed, "/") || strings.HasSuffix(trimmed, "/") || strings.Contains(trimmed, "//") {
			t.Fatalf("invalid group separators: %q", painted)
		}
	})
}

func TestStyledPaintStripsToUnstyledPaint(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		specs, rows, width := generatedTable(t)
		for i := range rows {
			for j := range rows[i] {
				rows[i][j].Role = "success"
			}
		}

		plan := Plan(specs, Measure(specs, rows), width, nil, time.Time{})
		plain, _ := Paint(plan, rows[0], nil)

		styled, _ := Paint(plan, rows[0], Theme{"success": "32"})
		if ansi.Strip(styled) != plain {
			t.Fatalf("style changed text: %q / %q", plain, styled)
		}
	})
}

func TestColoredCellsPaintLikePlainCellsAndCloseTheirStyles(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		words := rapid.SliceOfN(rapid.StringMatching(`[a-zé漢]{0,4}`), 1, 5).Draw(t, "words") //nolint:gosmopolitan // Generator mixes wide Han runes into colored cells to test width.
		styles := rapid.SliceOfN(rapid.SampledFrom([]string{"\x1b[31m", "\x1b[1;38;5;208m", "\x1b[0m", "\x1b[m", ""}), len(words), len(words)).Draw(t, "styles")
		width := rapid.IntRange(1, 16).Draw(t, "width")
		side := rapid.SampledFrom([]TruncateSide{TruncateRight, TruncateLeft}).Draw(t, "side")
		role := rapid.SampledFrom([]string{"", "success"}).Draw(t, "role")

		var colored strings.Builder
		for i, word := range words {
			colored.WriteString(styles[i])
			colored.WriteString(word)
		}

		specs := []ColumnSpec{{ID: "value", Min: width, Max: width, TruncateSide: side}, {ID: "next", Min: 4, Max: 4}}
		next := Cell{Text: "next", State: Present}
		coloredRow := []Cell{{Text: colored.String(), Role: role, State: Present}, next}
		plainRow := []Cell{{Text: ansi.Strip(colored.String()), Role: role, State: Present}, next}
		plan := Plan(specs, Measure(specs, [][]Cell{coloredRow}), width+4, nil, time.Time{})

		plain, _ := PaintColumns(plan, plainRow, Theme{"success": "32"})
		painted, _ := PaintColumns(plan, coloredRow, Theme{"success": "32"})

		if ansi.Strip(painted[0]) != ansi.Strip(plain[0]) || ansi.StringWidth(painted[0]) != width {
			t.Fatalf("colored paint %q differs from plain paint %q", painted[0], plain[0])
		}

		if last := strings.LastIndex(painted[0], "\x1b["); last >= 0 && !strings.HasPrefix(painted[0][last:], "\x1b[m") {
			t.Fatalf("colored cell leaves a style open: %q", painted[0])
		}
	})
}

func TestRoleColorResumesAfterCellReset(t *testing.T) {
	specs := []ColumnSpec{{ID: "value", Min: 8, Max: 8}}
	cells := []Cell{{Text: "\x1b[31mred\x1b[0m tail", Role: "success", State: Present}}

	painted, _ := Paint(Plan(specs, Measure(specs, [][]Cell{cells}), 8, nil, time.Time{}), cells, Theme{"success": "32"})
	if want := "\x1b[32m\x1b[31mred\x1b[0m\x1b[32m tail\x1b[m"; painted != want {
		t.Fatalf("painted = %q, want %q", painted, want)
	}
}

func TestSameSnapshotsPaintIdenticallyRegardlessOfArrivalOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		specs, rows, width := generatedTable(t)

		forward := Plan(specs, Measure(specs, rows), width, nil, time.Time{})
		originalRows := make([][]Cell, len(rows))
		copy(originalRows, rows)

		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}

		reverse := Plan(specs, Measure(specs, rows), width, nil, time.Time{})
		for i := range forward.Columns {
			if forward.Columns[i] != reverse.Columns[i] {
				t.Fatalf("arrival order changed column %d: %+v to %+v", i, forward.Columns[i], reverse.Columns[i])
			}
		}

		for i, row := range originalRows {
			before, _ := Paint(forward, row, nil)

			after, _ := Paint(reverse, row, nil)
			if before != after {
				t.Fatalf("arrival order changed row %d: %q to %q", i, before, after)
			}
		}
	})
}

func TestRatchetShrinksOnlyAfterDecayOrReflow(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		large := rapid.IntRange(2, 40).Draw(t, "large width")
		small := rapid.IntRange(1, large-1).Draw(t, "small width")
		specs := []ColumnSpec{{ID: "value"}}
		state := &RatchetState{}
		start := time.Unix(1, 0)
		first := Plan(specs, [][]CellWidths{{{Natural: large}}}, 80, state, start)

		within := Plan(specs, [][]CellWidths{{{Natural: small}}}, 80, state, start.Add(Decay-time.Nanosecond))
		if first.Columns[0].Width != within.Columns[0].Width {
			t.Fatalf("column shrank before decay: %d to %d", first.Columns[0].Width, within.Columns[0].Width)
		}

		decayed := Plan(specs, [][]CellWidths{{{Natural: small}}}, 80, state, start.Add(2*Decay))
		if decayed.Columns[0].Width != small {
			t.Fatalf("column did not decay: %d, want %d", decayed.Columns[0].Width, small)
		}

		state.Reset()

		reflowed := Plan(specs, [][]CellWidths{{{Natural: small}}}, 80, state, start)
		if reflowed.Columns[0].Width != small {
			t.Fatalf("reflow width = %d, want %d", reflowed.Columns[0].Width, small)
		}
	})
}

func TestWidthsUseAllFilteredRows(t *testing.T) {
	specs := []ColumnSpec{{ID: "name"}, {ID: "status", Reserve: 3}}
	rows := [][]Cell{
		{{Text: "a", State: Present}, {Text: "ok", State: Present}},
		{{Text: "long name", State: Present}, {Text: "idle", State: Present}},
	}

	plan := Plan(specs, Measure(specs, rows), 40, nil, time.Time{})
	if plan.Columns[0].Width != 9 || plan.Columns[1].Width != 4 {
		t.Fatalf("widths = %+v, want widths 9 and 4", plan.Columns)
	}

	for _, row := range rows {
		_, spans := Paint(plan, row, nil)
		if spans[1].Offset != 9 {
			t.Fatalf("status offset = %d, want 9", spans[1].Offset)
		}
	}
}

func TestBoundedFrameSequenceKeepsOffsets(t *testing.T) {
	specs := []ColumnSpec{
		{ID: "age", Reserve: 3, Max: 3},
		{ID: "git", Reserve: 4, Max: 4},
		{ID: "cpu", Reserve: 4, Max: 4},
		{ID: "name", Min: 4},
	}
	frames := [][]Cell{
		{{Text: "0s", State: Present}, {Text: "+0", State: Present}, {Text: "0%", State: Present}, {Text: "work", State: Present}},
		{{Text: "3h", State: Present}, {Text: "+150", State: Present}, {Text: "400%", State: Present}, {Text: "work", State: Present}},
	}
	state := &RatchetState{}
	first := Plan(specs, Measure(specs, frames[:1]), 30, state, time.Unix(1, 0))

	second := Plan(specs, Measure(specs, frames[1:]), 30, state, time.Unix(2, 0))
	for i := range first.Columns {
		if first.Columns[i].Offset != second.Columns[i].Offset {
			t.Fatalf("column %d shifted across bounded updates: %+v to %+v", i, first.Columns[i], second.Columns[i])
		}
	}
}

func TestUnboundedColumnGrowsOncePerNewMaximum(t *testing.T) {
	specs := []ColumnSpec{{ID: "value"}, {ID: "next", Reserve: 2}}
	state := &RatchetState{}
	start := time.Unix(1, 0)
	widths := []int{2, 6, 4, 6, 8}
	wantOffsets := []int{2, 6, 6, 6, 8}

	for i, width := range widths {
		plan := Plan(specs, [][]CellWidths{{{Natural: width}}}, 40, state, start.Add(time.Duration(i)*time.Second))
		if got := plan.Columns[1].Offset; got != wantOffsets[i] {
			t.Fatalf("frame %d offset = %d, want %d", i, got, wantOffsets[i])
		}
	}
}

func TestPendingCellPaintsDeclaredPlaceholder(t *testing.T) {
	specs := []ColumnSpec{{ID: "bar", Reserve: 4}}
	rows := [][]Cell{{{Text: "░░░░", Role: "pending", State: Pending}}}
	plan := Plan(specs, Measure(specs, rows), 4, nil, time.Time{})

	painted, _ := Paint(plan, rows[0], Theme{"pending": "2"})
	if got := ansi.Strip(painted); got != "░░░░" {
		t.Fatalf("pending placeholder = %q", got)
	}
}

func TestGrowingSpacerAnchorsRightColumns(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		width := rapid.IntRange(10, 80).Draw(t, "table width")
		left := rapid.IntRange(1, 5).Draw(t, "left width")
		right := rapid.IntRange(1, 4).Draw(t, "right width")
		specs := []ColumnSpec{
			{ID: "left", Min: left},
			{ID: "spacer", Min: 1, Reserve: 1, Grow: true},
			{ID: "right", Min: right},
		}
		rows := [][]Cell{{{Text: strings.Repeat("l", left), State: Present}, {Text: " ", State: Present}, {Text: strings.Repeat("r", right), State: Present}}}
		plan := Plan(specs, Measure(specs, rows), width, nil, time.Time{})

		painted, spans := Paint(plan, rows[0], nil)
		if spans[0].Offset != 0 || spans[2].Offset+spans[2].Width != width || ansi.StringWidth(painted) != width {
			t.Fatalf("right column is not anchored: plan=%+v spans=%+v paint=%q", plan.Columns, spans, painted)
		}
	})
}

func boolState(present bool) State {
	if present {
		return Present
	}

	return Absent
}

func Paint(plan ColumnPlan, cells []Cell, theme Theme) (string, []Span) {
	parts, spans := PaintColumns(plan, cells, theme)

	return strings.Join(parts, ""), spans
}
