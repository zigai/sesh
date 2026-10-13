package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"pgregory.net/rapid"

	"github.com/zigai/sesh/internal/display"
)

func TestFormatAgentAgeAtKeepsMinutePrecisionAfterOneHour(t *testing.T) {
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "minutes", at: now.Add(-12 * time.Minute), want: "12m"},
		{name: "hours and minutes", at: now.Add(-(5*time.Hour + 17*time.Minute)), want: "5h 17m"},
		{name: "whole hours", at: now.Add(-5 * time.Hour), want: "5h 0m"},
		{name: "days hours and minutes", at: now.Add(-(72*time.Hour + 17*time.Minute)), want: "3d 0h 17m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatAgentAgeAt(tt.at, now); got != tt.want {
				t.Fatalf("formatAgentAgeAt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatAgentClockAt(t *testing.T) {
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "zero", at: time.Time{}, want: ""},
		{name: "seconds only", at: now.Add(-23 * time.Second), want: "00:00:23"},
		{name: "one minute 23 seconds", at: now.Add(-(time.Minute + 23*time.Second)), want: "00:01:23"},
		{name: "hours minutes seconds", at: now.Add(-(5*time.Hour + 17*time.Minute + 42*time.Second)), want: "05:17:42"},
		{name: "days", at: now.Add(-(72*time.Hour + 17*time.Minute)), want: "72:17:00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatAgentClockAt(tt.at, now); got != tt.want {
				t.Fatalf("formatAgentClockAt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatAgentStatusAgeAt(t *testing.T) {
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	at := now.Add(-(time.Minute + 23*time.Second))

	if got := formatAgentStatusAgeAt(at, now, "relative"); got != "1m" {
		t.Fatalf("formatAgentStatusAgeAt(relative) = %q, want 1m", got)
	}

	if got := formatAgentStatusAgeAt(at, now, "clock"); got != "00:01:23" {
		t.Fatalf("formatAgentStatusAgeAt(clock) = %q, want 00:01:23", got)
	}

	if got := formatAgentStatusAgeAt(at, now, " RELATIVE-SECONDS "); got != "1m" {
		t.Fatalf("formatAgentStatusAgeAt(relative-seconds) = %q, want 1m", got)
	}

	if got := formatAgentStatusAgeAt(at, now, "00:01:23"); got != "00:01:23" {
		t.Fatalf("formatAgentStatusAgeAt(00:01:23) = %q, want 00:01:23", got)
	}
}

func TestFormatAgentStatusElapsedHoursWithoutMinutePadding(t *testing.T) {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		age  time.Duration
		want string
	}{
		{name: "whole hours", age: 5 * time.Hour, want: "5h 0m"},
		{name: "single digit minutes", age: 5*time.Hour + 3*time.Minute, want: "5h 3m"},
		{name: "double digit minutes", age: 5*time.Hour + 17*time.Minute, want: "5h 17m"},
		{name: "before day boundary", age: 23*time.Hour + 59*time.Minute + 59*time.Second, want: "23h 59m"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatAgentStatusAgeAt(now.Add(-test.age), now, "relative-seconds"); got != test.want {
				t.Fatalf("elapsed status age = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFormatAgentStatusElapsedDaysWithoutHourPadding(t *testing.T) {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		age  time.Duration
		want string
	}{
		{name: "whole days", age: 48 * time.Hour, want: "2d 0h"},
		{name: "single digit hours", age: 51 * time.Hour, want: "2d 3h"},
		{name: "double digit hours", age: 71 * time.Hour, want: "2d 23h"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatAgentStatusAgeAt(now.Add(-test.age), now, "relative-seconds"); got != test.want {
				t.Fatalf("elapsed status age = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFormatAgentStatusHoursMinutes(t *testing.T) {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "missing", at: time.Time{}, want: ""},
		{name: "future", at: now.Add(time.Minute), want: "00:00"},
		{name: "seconds omitted", at: now.Add(-59 * time.Second), want: "00:00"},
		{name: "minutes", at: now.Add(-83 * time.Second), want: "00:01"},
		{name: "hour boundary", at: now.Add(-time.Hour), want: "01:00"},
		{name: "hours", at: now.Add(-(5*time.Hour + 17*time.Minute + 42*time.Second)), want: "05:17"},
		{name: "days", at: now.Add(-(72*time.Hour + 17*time.Minute)), want: "72:17"},
		{name: "large hours", at: now.Add(-100 * time.Hour), want: "100:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatAgentStatusAgeAt(tt.at, now, " HH:MM "); got != tt.want {
				t.Fatalf("duration = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeTerminalTextKeepSGRKeepsOnlyStyles(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		"combining accent":    {input: "e\u0301clair", want: "e\u0301clair"},
		"styled accent":       {input: "e\x1b[31m\u0301", want: "e\x1b[31m\u0301"},
		"joined emoji":        {input: "👩\u200d💻", want: "👩\u200d💻"},
		"plain":               {input: "main ✓ 漢字", want: "main ✓ 漢字"}, //nolint:gosmopolitan // Test checks that wide Han runes pass through the sanitizer.
		"sgr":                 {input: "\x1b[1;38;2;255;0:0mok\x1b[0m \x1b[m", want: "\x1b[1;38;2;255;0:0mok\x1b[0m \x1b[m"},
		"cursor movement":     {input: "a\x1b[2Jb\x1b[10;3Hc\x1b[?25l", want: "abc"},
		"osc hyperlink":       {input: "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\", want: "link"},
		"osc clipboard":       {input: "x\x1b]52;c;aGFja2Vk\ay", want: "xy"},
		"dcs":                 {input: "a\x1bPq#0;2;0;0;0\x1b\\b", want: "ab"},
		"private sgr":         {input: "\x1b[>4;2mx", want: "x"},
		"c0 and c1 controls":  {input: "a\tb\rc\x07d\u0085e", want: "abcde"},
		"bidi overrides":      {input: "a\u202ecba\u2066x\u2069", want: "acbax"},
		"bare escape":         {input: "a\x1bb", want: "a"},
		"eight bit csi bytes": {input: "a\x9b31mb", want: "ab"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := SanitizeTerminalTextKeepSGR(tt.input); got != tt.want {
				t.Fatalf("SanitizeTerminalTextKeepSGR(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTruncateStyledWidthPreservesANSIAndGraphemeClusters(t *testing.T) {
	input := "\x1b[31mA👩‍💻B\x1b[0m"

	got := truncateStyled(input, 3)

	if text := ansi.Strip(got); text != "A👩‍💻" {
		t.Fatalf("truncateStyled() text = %q, want %q", text, "A👩‍💻")
	}

	if width := ansi.StringWidth(got); width != 3 {
		t.Fatalf("truncateStyled() width = %d, want 3", width)
	}

	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("truncateStyled() = %q, want trailing style reset", got)
	}
}

func TestTruncateStyledWidthPreservesOSCSequences(t *testing.T) {
	input := "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\"

	got := truncateStyled(input, 2)

	if text := ansi.Strip(got); text != "li" {
		t.Fatalf("truncateStyled() text = %q, want %q", text, "li")
	}

	if width := ansi.StringWidth(got); width != 2 {
		t.Fatalf("truncateStyled() width = %d, want 2", width)
	}
}

func TestTruncateWidthKeepsCombiningSequenceIntact(t *testing.T) {
	got := display.Truncate("e\u0301clair", 4)

	if got != "e\u0301..." {
		t.Fatalf("display.Truncate() = %q, want %q", got, "e\u0301...")
	}

	if width := ansi.StringWidth(got); width != 4 {
		t.Fatalf("display.Truncate() width = %d, want 4", width)
	}
}

func TestTruncateLeftWidthPreservesIdentitySuffix(t *testing.T) {
	got := truncateLeftWidth("2026-07-16_session-abcdef.jsonl", 15)

	if got != "...abcdef.jsonl" {
		t.Fatalf("truncateLeftWidth() = %q, want identity suffix", got)
	}

	if width := ansi.StringWidth(got); width != 15 {
		t.Fatalf("truncateLeftWidth() width = %d, want 15", width)
	}
}

func TestCompactPromptAgeFitsThreeCellsAcrossDurations(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)

	rapid.Check(t, func(t *rapid.T) {
		seconds := rapid.Int64Range(-1_000_000, 100_000_000).Draw(t, "age seconds")

		age := formatCompactAgeAt(now.Add(-time.Duration(seconds)*time.Second), now)
		if width := ansi.StringWidth(age); width > 3 || width == 0 {
			t.Fatalf("compact age %q has width %d", age, width)
		}
	})
}
