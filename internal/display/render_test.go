package display

import (
	"testing"
)

func TestSessionFieldMaximumWidthTruncatesBeforePadding(t *testing.T) {
	left := AlignPart(Part{
		Field:       "",
		Value:       "abcdefghijklmnop",
		Alignment:   AlignmentLeft,
		Width:       12,
		MaxWidth:    8,
		HasMaxWidth: true,
	})
	if left != "abcdefg…    " {
		t.Fatalf("left-aligned field = %q, want truncated and padded value", left)
	}

	right := AlignPart(Part{
		Field:       "",
		Value:       "abcdefghijklmnop",
		Alignment:   AlignmentRight,
		Width:       12,
		MaxWidth:    8,
		HasMaxWidth: true,
	})
	if right != "    …jklmnop" {
		t.Fatalf("right-aligned field = %q, want padded tail", right)
	}
}

func TestSessionFieldFormatsAlignByTerminalDisplayWidth(t *testing.T) {
	const wideName = "\u958b\u767a"

	if w := StringWidth(wideName); w != 4 {
		t.Fatalf("StringWidth(%q) = %d, want 4", wideName, w)
	}

	partLeft := AlignPart(Part{
		Field:       FieldSessionName,
		Value:       wideName,
		Alignment:   AlignmentLeft,
		Width:       8,
		MaxWidth:    8,
		HasMaxWidth: true,
	})
	if partLeft != wideName+"    " {
		t.Fatalf("left aligned wideName = %q, want %q", partLeft, wideName+"    ")
	}

	if w := StringWidth(partLeft); w != 8 {
		t.Fatalf("StringWidth(partLeft) = %d, want 8", w)
	}

	partCenter := AlignPart(Part{
		Field:       FieldActiveWindowName,
		Value:       "editor",
		Alignment:   AlignmentCenter,
		Width:       10,
		MaxWidth:    10,
		HasMaxWidth: true,
	})
	if partCenter != "  editor  " {
		t.Fatalf("center aligned part = %q, want %q", partCenter, "  editor  ")
	}

	if w := StringWidth(partCenter); w != 10 {
		t.Fatalf("StringWidth(partCenter) = %d, want 10", w)
	}

	partRight := AlignPart(Part{
		Field:       FieldSessionKind,
		Value:       "live",
		Alignment:   AlignmentRight,
		Width:       6,
		MaxWidth:    6,
		HasMaxWidth: true,
	})
	if partRight != "  live" {
		t.Fatalf("right aligned part = %q, want %q", partRight, "  live")
	}

	if w := StringWidth(partRight); w != 6 {
		t.Fatalf("StringWidth(partRight) = %d, want 6", w)
	}
}

func TestAlignPartAllAlignments(t *testing.T) {
	tests := []struct {
		name string
		part Part
		want string
	}{
		{
			name: "none alignment defaults to left padding",
			part: Part{
				Field:       "test",
				Value:       "foo",
				Alignment:   AlignmentNone,
				Width:       6,
				MaxWidth:    0,
				HasMaxWidth: false,
			},
			want: "foo   ",
		},
		{
			name: "left alignment pads right",
			part: Part{
				Field:       "test",
				Value:       "foo",
				Alignment:   AlignmentLeft,
				Width:       6,
				MaxWidth:    0,
				HasMaxWidth: false,
			},
			want: "foo   ",
		},
		{
			name: "right alignment pads left",
			part: Part{
				Field:       "test",
				Value:       "foo",
				Alignment:   AlignmentRight,
				Width:       6,
				MaxWidth:    0,
				HasMaxWidth: false,
			},
			want: "   foo",
		},
		{
			name: "center alignment even padding",
			part: Part{
				Field:       "test",
				Value:       "foo",
				Alignment:   AlignmentCenter,
				Width:       7,
				MaxWidth:    0,
				HasMaxWidth: false,
			},
			want: "  foo  ",
		},
		{
			name: "center alignment odd padding",
			part: Part{
				Field:       "test",
				Value:       "foo",
				Alignment:   AlignmentCenter,
				Width:       6,
				MaxWidth:    0,
				HasMaxWidth: false,
			},
			want: " foo  ",
		},
		{
			name: "no padding needed when value width exceeds width",
			part: Part{
				Field:       "test",
				Value:       "long_value",
				Alignment:   AlignmentLeft,
				Width:       5,
				MaxWidth:    0,
				HasMaxWidth: false,
			},
			want: "long_value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AlignPart(tt.part)
			if got != tt.want {
				t.Fatalf("AlignPart() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAlignPartAndTruncateUnusualUnicode(t *testing.T) {
	rocket := "🚀"
	if w := StringWidth(rocket); w != 2 {
		t.Fatalf("StringWidth(rocket) = %d, want 2", w)
	}

	alignedRocket := AlignPart(Part{
		Field:       "glyph",
		Value:       rocket,
		Alignment:   AlignmentRight,
		Width:       5,
		MaxWidth:    0,
		HasMaxWidth: false,
	})
	if alignedRocket != "   🚀" {
		t.Fatalf("aligned rocket = %q, want %q", alignedRocket, "   🚀")
	}

	if w := StringWidth(alignedRocket); w != 5 {
		t.Fatalf("StringWidth(alignedRocket) = %d, want 5", w)
	}

	japanese := "\u65e5\u672c\u8a9e\u30d7\u30ed\u30b0\u30e9\u30df\u30f3\u30b0"
	if w := StringWidth(japanese); w != 20 {
		t.Fatalf("StringWidth(japanese) = %d, want 20", w)
	}

	truncatedJapanese := TruncatePart(Part{
		Field:       "name",
		Value:       japanese,
		Alignment:   AlignmentLeft,
		Width:       12,
		MaxWidth:    9,
		HasMaxWidth: true,
	})
	if truncatedJapanese != "\u65e5\u672c\u8a9e\u30d7…" {
		t.Fatalf("truncatedJapanese = %q, want %q", truncatedJapanese, "\u65e5\u672c\u8a9e\u30d7…")
	}

	alignedJapanese := AlignPart(Part{
		Field:       "name",
		Value:       japanese,
		Alignment:   AlignmentLeft,
		Width:       12,
		MaxWidth:    9,
		HasMaxWidth: true,
	})
	if alignedJapanese != "\u65e5\u672c\u8a9e\u30d7…   " {
		t.Fatalf("alignedJapanese = %q, want %q", alignedJapanese, "\u65e5\u672c\u8a9e\u30d7…   ")
	}

	if w := StringWidth(alignedJapanese); w != 12 {
		t.Fatalf("StringWidth(alignedJapanese) = %d, want 12", w)
	}

	truncatedLeftJapanese := AlignPart(Part{
		Field:       "name",
		Value:       japanese,
		Alignment:   AlignmentRight,
		Width:       12,
		MaxWidth:    9,
		HasMaxWidth: true,
	})
	if truncatedLeftJapanese != "   …\u30e9\u30df\u30f3\u30b0" {
		t.Fatalf("truncatedLeftJapanese = %q, want %q", truncatedLeftJapanese, "   …\\u30e9\\u30df\\u30f3\\u30b0")
	}

	if w := StringWidth(truncatedLeftJapanese); w != 12 {
		t.Fatalf("StringWidth(truncatedLeftJapanese) = %d, want 12", w)
	}
}

func TestTruncateAndTruncateLeftEdgeCases(t *testing.T) {
	truncateTests := []struct {
		input string
		width int
		want  string
	}{
		{input: "hello", width: 0, want: ""},
		{input: "hello", width: -5, want: ""},
		{input: "abcdef", width: 3, want: "ab…"},
		{input: "abcdef", width: 2, want: "a…"},
		{input: "abcdef", width: 1, want: "a"},
		{input: "abc", width: 5, want: "abc"},
		{input: "abc", width: 3, want: "abc"},
	}

	for _, tt := range truncateTests {
		if got := Truncate(tt.input, tt.width); got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.input, tt.width, got, tt.want)
		}
	}

	truncateLeftTests := []struct {
		input string
		width int
		want  string
	}{
		{input: "hello", width: 0, want: ""},
		{input: "hello", width: -1, want: ""},
		{input: "abcdef", width: 3, want: "…ef"},
		{input: "abcdef", width: 2, want: "…f"},
		{input: "abc", width: 5, want: "abc"},
		{input: "abc", width: 3, want: "abc"},
	}

	for _, tt := range truncateLeftTests {
		if got := TruncateLeft(tt.input, tt.width); got != tt.want {
			t.Errorf("TruncateLeft(%q, %d) = %q, want %q", tt.input, tt.width, got, tt.want)
		}
	}
}

func TestLineAndPartMethods(t *testing.T) {
	part1 := Part{
		Field:       FieldSessionName,
		Value:       "sesh",
		Alignment:   AlignmentLeft,
		Width:       8,
		MaxWidth:    0,
		HasMaxWidth: false,
	}
	part2 := Part{
		Field:       "",
		Value:       "[active]",
		Alignment:   AlignmentNone,
		Width:       0,
		MaxWidth:    0,
		HasMaxWidth: false,
	}
	partRight := Part{
		Field:       FieldWindowCount,
		Value:       "5 windows",
		Alignment:   AlignmentRight,
		Width:       12,
		MaxWidth:    0,
		HasMaxWidth: false,
	}

	if part1.Render() != "sesh    " {
		t.Fatalf("Part.Render() = %q, want %q", part1.Render(), "sesh    ")
	}

	if part1.String() != "sesh    " {
		t.Fatalf("Part.String() = %q, want %q", part1.String(), "sesh    ")
	}

	line := Line{
		Left:  []Part{part1, part2},
		Right: []Part{partRight},
	}

	if line.RenderLeft() != "sesh    [active]" {
		t.Fatalf("Line.RenderLeft() = %q, want %q", line.RenderLeft(), "sesh    [active]")
	}

	if line.RenderRight() != "   5 windows" {
		t.Fatalf("Line.RenderRight() = %q, want %q", line.RenderRight(), "   5 windows")
	}
}
