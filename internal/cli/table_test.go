package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	prettytable "github.com/jedib0t/go-pretty/v6/table"
)

func TestWriteResponsiveTableCompactTruncation(t *testing.T) {
	t.Parallel()

	columns := []tableColumn{
		{Heading: "Session", Fixed: false, MinWidth: 20, Wrap: wrapSession},
		{Heading: "Panes", Fixed: true, MinWidth: 0, Wrap: nil},
		{Heading: "Attached", Fixed: true, MinWidth: 0, Wrap: nil},
		{Heading: "Activity", Fixed: true, MinWidth: 0, Wrap: nil},
	}

	rows := []prettytable.Row{
		{"extremely-long-session-name-for-testing-truncation-behavior-in-terminal", 3, "yes", "just now"},
	}

	sizedCols := allocateCompactResponsiveColumns(columns, rows, 40)
	output := &bytes.Buffer{}

	if err := writeCompactResponsiveTable(output, sizedCols, rows); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("compact table must be 2 lines (header + row), got %d lines:\n%s", len(lines), output.String())
	}

	if !strings.Contains(lines[1], "…") {
		t.Fatalf("expected ellipsis in truncated compact table: %s", lines[1])
	}

	if !strings.Contains(lines[1], "yes") || !strings.Contains(lines[1], "just now") {
		t.Fatalf("fixed columns lost in compact table: %s", lines[1])
	}
}

func TestWriteResponsiveTableCompactUsesDisplayWidth(t *testing.T) {
	t.Parallel()

	columns := []sizedResponsiveColumn{{heading: "Name", width: 5, wrap: nil}}
	rows := []prettytable.Row{{"\x1b[31m\u754c\u754c\u754c\x1b[0m"}}
	output := &bytes.Buffer{}

	if err := writeCompactResponsiveTable(output, columns, rows); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d:\n%s", len(lines), output.String())
	}

	cell := strings.TrimSpace(lines[1])
	if width := ansi.StringWidth(cell); width != 5 {
		t.Fatalf("compact cell width = %d, want 5 (cell: %q)", width, cell)
	}

	if plain := ansi.Strip(cell); plain != "\u754c\u754c…" {
		t.Fatalf("compact cell = %q, want display-aware truncation %q", plain, "\u754c\u754c…")
	}
}

func TestResponsiveTextUsesDisplayWidth(t *testing.T) {
	t.Parallel()

	columns := []tableColumn{{Heading: "\x1b[34m\u540d\x1b[0m", Fixed: false, MinWidth: 0, Wrap: nil}}
	rows := []prettytable.Row{{"\x1b[31me\u0301\u754c\x1b[0m"}}
	widths := measureResponsiveNaturalWidths(columns, rows)

	if len(widths) != 1 || widths[0] != 3 {
		t.Fatalf("natural widths = %v, want [3]", widths)
	}

	value := "\x1b[31mab/cd/e\u0301\u754c\x1b[0m"

	lines := wrapIdentifier(value, 5)
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > 5 {
			t.Fatalf("wrapped line display width = %d, want <= 5: %q", width, line)
		}
	}

	if first := ansi.Strip(lines[0]); first != "ab/" {
		t.Fatalf("first wrapped line = %q, want delimiter-aware break %q", first, "ab/")
	}

	if got := ansi.Strip(strings.Join(lines, "")); got != ansi.Strip(value) {
		t.Fatalf("wrapped text = %q, want all input text %q", got, ansi.Strip(value))
	}
}

func TestWriteResponsiveTableFullWrapped(t *testing.T) {
	t.Parallel()

	columns := []tableColumn{
		{Heading: "Session", Fixed: false, MinWidth: 10, Wrap: wrapSession},
		{Heading: "Panes", Fixed: true, MinWidth: 0, Wrap: nil},
		{Heading: "Attached", Fixed: true, MinWidth: 0, Wrap: nil},
		{Heading: "Activity", Fixed: true, MinWidth: 0, Wrap: nil},
	}

	rows := []prettytable.Row{
		{"session-one-two-three", 3, "yes", "just now"},
	}

	sizedCols, fits := allocateFullResponsiveColumns(columns, rows, 40)
	if !fits {
		t.Fatal("expected columns to fit in full mode at width 40")
	}

	output := &bytes.Buffer{}

	if err := writeWrappedResponsiveTable(output, sizedCols, rows); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("full wrapped table must wrap content across multiple lines, got %d lines:\n%s", len(lines), output.String())
	}

	if strings.Contains(output.String(), "…") {
		t.Fatalf("full wrapped table should wrap without truncation: %s", output.String())
	}
}

func TestWriteResponsiveTableFullStackedFallback(t *testing.T) {
	t.Parallel()

	columns := []tableColumn{
		{Heading: "Session", Fixed: false, MinWidth: 10, Wrap: wrapSession},
		{Heading: "Panes", Fixed: true, MinWidth: 0, Wrap: nil},
		{Heading: "Attached", Fixed: true, MinWidth: 0, Wrap: nil},
		{Heading: "Activity", Fixed: true, MinWidth: 0, Wrap: nil},
	}

	rows := []prettytable.Row{
		{"a-very-long-session-name-that-cannot-fit", 3, "yes", "just now"},
	}

	output := &bytes.Buffer{}

	if err := renderResponsiveTable(output, 20, columns, rows, true); err != nil {
		t.Fatal(err)
	}

	rendered := output.String()
	if !strings.Contains(rendered, "Session:") {
		t.Fatalf("expected stacked layout with field labels, got:\n%s", rendered)
	}

	if !strings.Contains(rendered, "Panes:") || !strings.Contains(rendered, "Attached:") || !strings.Contains(rendered, "Activity:") {
		t.Fatalf("stacked layout missing field labels:\n%s", rendered)
	}
}
