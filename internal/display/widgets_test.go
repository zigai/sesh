package display

import (
	"strconv"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"pgregory.net/rapid"
)

func TestBarWidgetStaysWithinDeclaredWidth(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		width := rapid.IntRange(1, 40).Draw(t, "width")
		percent := rapid.IntRange(-300, 400).Draw(t, "percent")

		bar := percentageBar(percent, width)
		if got := ansi.StringWidth(bar); got != width {
			t.Fatalf("bar width = %d, want %d for %d%%", got, width, percent)
		}
	})
}

func TestWidgetsRenderValuesAndStates(t *testing.T) {
	for _, tc := range []struct {
		source   string
		values   testResolver
		want     string
		wantRole string
	}{
		{source: "{bar:context_pct:8}", values: testResolver{FieldAgentContextPct: "50"}, want: "████░░░░"},
		{source: "{todo_progress:5}", values: testResolver{FieldAgentTodoDone: "3", FieldAgentTodoTotal: "7"}, want: " 3/ 7 ▰▰▱▱▱"},
		{source: "{spinner}", values: testResolver{FieldStatus: "running"}, want: "⠋"},
		{source: "{badge:subagent_count@warn>0}", values: testResolver{FieldSubagentCount: "101"}, want: "99+", wantRole: "warn"},
	} {
		template, err := Compile(FormatKindAgentsAgent, tc.source)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tc.source, err)
		}

		part := template.Render(tc.values).Left[0]
		if part.Value != tc.want || part.Role != tc.wantRole {
			t.Fatalf("Render(%q) = %+v, want %q role %q", tc.source, part, tc.want, tc.wantRole)
		}

		if got := ansi.StringWidth(part.Value); got > part.Width {
			t.Fatalf("Render(%q) width = %d, reserve %d", tc.source, got, part.Width)
		}
	}
}

func TestFractionWidgetStaysWithinReservedWidth(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		width := rapid.IntRange(1, 30).Draw(t, "bar width")
		done := rapid.IntRange(-200, 400).Draw(t, "done")
		total := rapid.IntRange(1, 400).Draw(t, "total")

		template, err := Compile(FormatKindAgentsAgent, "{todo_progress:"+strconv.Itoa(width)+"}")
		if err != nil {
			t.Fatal(err)
		}

		part := template.Render(testResolver{
			FieldAgentTodoDone: strconv.Itoa(done), FieldAgentTodoTotal: strconv.Itoa(total),
		}).Left[0]
		if got := ansi.StringWidth(part.Value); got != part.Width {
			t.Fatalf("fraction width = %d, want %d, done=%d total=%d", got, part.Width, done, total)
		}
	})
}
