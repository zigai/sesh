package clibase_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/charmbracelet/x/ansi"
	prettytable "github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/session"
)

var (
	errTestUsage   = errors.New("usage failure")
	errTestUnknown = errors.New("unknown flag: --foo")
	errTestGeneral = errors.New("general error")
)

func TestExitCode(t *testing.T) {
	t.Parallel()

	if code := clibase.ExitCode(nil); code != 0 {
		t.Fatalf("ExitCode(nil) = %d, want 0", code)
	}

	exitErr := clibase.NewExitError(clibase.ExitCodeUsage, errTestUsage)
	if code := clibase.ExitCode(exitErr); code != clibase.ExitCodeUsage {
		t.Fatalf("ExitCode(exitErr) = %d, want %d", code, clibase.ExitCodeUsage)
	}

	if code := clibase.ExitCode(syscall.EPIPE); code != clibase.ExitCodePipe {
		t.Fatalf("ExitCode(EPIPE) = %d, want %d", code, clibase.ExitCodePipe)
	}

	if code := clibase.ExitCode(context.Canceled); code != action.ExitCodeCanceled {
		t.Fatalf("ExitCode(context.Canceled) = %d, want %d", code, action.ExitCodeCanceled)
	}

	if code := clibase.ExitCode(errTestUnknown); code != clibase.ExitCodeUsage {
		t.Fatalf("ExitCode(unknown flag) = %d, want %d", code, clibase.ExitCodeUsage)
	}

	if code := clibase.ExitCode(errTestGeneral); code != clibase.ExitCodeGeneral {
		t.Fatalf("ExitCode(general error) = %d, want %d", code, clibase.ExitCodeGeneral)
	}
}

func sessionTableColumns(sessionMinWidth int) []clibase.TableColumn {
	return []clibase.TableColumn{
		{Heading: "Session", Fixed: false, MinWidth: sessionMinWidth, Wrap: clibase.WrapSession},
		{Heading: "Panes", Fixed: true, MinWidth: 0, Wrap: nil},
		{Heading: "Attached", Fixed: true, MinWidth: 0, Wrap: nil},
		{Heading: "Activity", Fixed: true, MinWidth: 0, Wrap: nil},
	}
}

func renderTableLines(t *testing.T, width int, columns []clibase.TableColumn, rows []prettytable.Row, full bool) []string {
	t.Helper()

	var buf bytes.Buffer
	if err := clibase.RenderResponsiveTable(&buf, width, columns, rows, full); err != nil {
		t.Fatalf("RenderResponsiveTable() error = %v", err)
	}

	return strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
}

func TestResponsiveTableCompactTruncatesFlexibleColumnAndKeepsFixedColumns(t *testing.T) {
	t.Parallel()

	rows := []prettytable.Row{{"extremely-long-session-name-for-testing-truncation-behavior-in-terminal", 3, "yes", "just now"}}
	lines := renderTableLines(t, 40, sessionTableColumns(20), rows, false)

	if len(lines) != 3 || strings.Trim(lines[1], "─") != "" || strings.Contains(strings.Join(lines, "\n"), "│") {
		t.Fatalf("compact table must be a header, one thin rule and one row: %q", lines)
	}

	row := lines[2]
	if width := ansi.StringWidth(row); width > 40 {
		t.Fatalf("compact row width = %d, want <= 40: %q", width, row)
	}

	if !strings.Contains(row, "extremely-lo…") || !strings.Contains(row, "yes") || !strings.Contains(row, "just now") {
		t.Fatalf("compact row must truncate the session and keep fixed columns: %q", row)
	}
}

func TestResponsiveTableLinesNeverExceedRequestedWidth(t *testing.T) {
	t.Parallel()

	rows := []prettytable.Row{{"extremely-long-session-name-for-testing-truncation-behavior-in-terminal", 3, "yes", "just now"}}

	for _, full := range []bool{false, true} {
		for _, width := range []int{40, 60, 120} {
			for _, line := range renderTableLines(t, width, sessionTableColumns(20), rows, full) {
				if got := ansi.StringWidth(line); got > width {
					t.Errorf("full=%t width=%d: line width = %d, want <= %d: %q", full, width, got, width, line)
				}
			}
		}
	}
}

func TestResponsiveTableMeasuresCellsByDisplayWidth(t *testing.T) {
	t.Parallel()

	truncated := renderTableLines(t, 5, []clibase.TableColumn{{Heading: "Name", Fixed: false, MinWidth: 0, Wrap: nil}},
		[]prettytable.Row{{"\x1b[31m\u754c\u754c\u754c\x1b[0m"}}, false)
	if cell := truncated[len(truncated)-1]; ansi.StringWidth(cell) != 5 || ansi.Strip(cell) != "\u754c\u754c…" {
		t.Fatalf("compact cell = %q, want display-aware truncation to %q", cell, "\u754c\u754c…")
	}

	columns := []clibase.TableColumn{
		{Heading: "Name", Fixed: false, MinWidth: 0, Wrap: nil},
		{Heading: "\x1b[34m\u540d\x1b[0m", Fixed: true, MinWidth: 0, Wrap: nil},
	}
	fitted := renderTableLines(t, 12, columns, []prettytable.Row{{"abcdef", "\x1b[31m\u754c\u754c\x1b[0m"}}, false)

	if row := ansi.Strip(fitted[len(fitted)-1]); row != "abcdef  \u754c\u754c" {
		t.Fatalf("row = %q, want the styled wide fixed cell measured as 4 columns so the name fits untruncated", row)
	}
}

func TestWrapIdentifierBreaksAtDelimitersByDisplayWidth(t *testing.T) {
	t.Parallel()

	value := "\x1b[31mab/cd/e\u0301\u754c\x1b[0m"

	lines := clibase.WrapIdentifier(value, 5)
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

func TestResponsiveTableFullModeWrapsInsteadOfTruncating(t *testing.T) {
	t.Parallel()

	lines := renderTableLines(t, 40, sessionTableColumns(10), []prettytable.Row{{"session-one-two-three", 3, "yes", "just now"}}, true)

	rendered := strings.Join(lines, "\n")
	if strings.Contains(rendered, "…") || strings.Contains(rendered, "session-one-two-three") {
		t.Fatalf("full table must wrap the session across lines without truncation:\n%s", rendered)
	}

	if len(lines) != 4 || !strings.HasPrefix(lines[2], "session-one-") || !strings.Contains(lines[2], "just now") ||
		strings.TrimSpace(lines[3]) != "two-three" {
		t.Fatalf("full table must continue the wrapped session on its own line:\n%s", rendered)
	}
}

func TestResponsiveTableFullModeStacksRowsThatCannotFit(t *testing.T) {
	t.Parallel()

	lines := renderTableLines(t, 20, sessionTableColumns(10), []prettytable.Row{{"a-very-long-session-name-that-cannot-fit", 3, "yes", "just now"}}, true)

	labels := make([]string, 0, 4)

	for _, line := range lines {
		if width := ansi.StringWidth(line); width > 20 {
			t.Fatalf("stacked line width = %d, want <= 20: %q", width, line)
		}

		if label, _, ok := strings.Cut(line, ":"); ok && !strings.HasPrefix(line, " ") {
			labels = append(labels, label)
		}
	}

	if strings.Join(labels, ",") != "Session,Panes,Attached,Activity" {
		t.Fatalf("stacked labels = %v, want every column labeled in order:\n%s", labels, strings.Join(lines, "\n"))
	}
}

func TestJSONHelpers(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "test"}
	clibase.AddJSONFlag(cmd)

	if clibase.JSONOutputEnabled(cmd) {
		t.Fatal("JSONOutputEnabled should be false by default")
	}

	_ = cmd.Flags().Set(clibase.JSONFlagName, "true")
	if !clibase.JSONOutputEnabled(cmd) {
		t.Fatal("JSONOutputEnabled should be true after setting flag")
	}

	code := clibase.JSONErrorCode(session.ErrSessionNotFound)
	if code != "notFound" {
		t.Fatalf("JSONErrorCode(ErrSessionNotFound) = %q, want 'notFound'", code)
	}

	outputLimit := fmt.Errorf("capture /private/path: %w", gotmux.ErrOutputLimit)
	if code := clibase.JSONErrorCode(outputLimit); code != "outputLimitExceeded" {
		t.Fatalf("JSONErrorCode(ErrOutputLimit) = %q, want 'outputLimitExceeded'", code)
	}

	if msg := clibase.SafeCommandErrorMessage(outputLimit); msg != "tmux command output exceeded its byte limit" {
		t.Fatalf("SafeCommandErrorMessage(ErrOutputLimit) = %q", msg)
	}

	msg := clibase.SafeCommandErrorMessage(session.ErrSessionNotFound)
	if msg != "session not found" {
		t.Fatalf("SafeCommandErrorMessage(ErrSessionNotFound) = %q, want 'session not found'", msg)
	}
}

func TestCommandContextAndConfig(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("config", "", "")
	cmd.Flags().Bool("no-config", true, "")

	ctx := clibase.CommandContext(cmd)
	if ctx == nil {
		t.Fatal("CommandContext returned nil")
	}

	cfg, err := clibase.LoadCommandConfig(ctx, cmd)
	if err != nil {
		t.Fatalf("LoadCommandConfig failed: %v", err)
	}

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	cfg2, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		t.Fatalf("LoadCommandConfig cached failed: %v", err)
	}

	if cfg2 == nil {
		t.Fatal("expected non-nil cached config")
	}

	tempDir := t.TempDir()
	invalidConfig := filepath.Join(tempDir, "invalid.toml")
	_ = os.WriteFile(invalidConfig, []byte("invalid = = = toml"), 0o600)

	invalidCmd := &cobra.Command{Use: "invalid"}
	invalidCmd.Flags().String("config", invalidConfig, "")
	invalidCmd.Flags().Bool("no-config", false, "")

	_, err = clibase.LoadCommandConfig(context.Background(), invalidCmd)
	if err == nil {
		t.Fatal("expected error loading invalid config, got nil")
	}

	if clibase.JSONErrorCode(err) != "configurationFailed" {
		t.Fatalf("JSONErrorCode(err) = %q, want 'configurationFailed'", clibase.JSONErrorCode(err))
	}
}
