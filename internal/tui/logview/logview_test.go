package logview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/zigai/sesh/internal/logging"
)

const (
	formattedWarningRecord = `{"time":"2026-08-06T15:45:52.328165852Z","level":"warn","command":"doctor","version":"dev","commit":"abc123","pid":42,"component":"doctor","check":"continuum-shutdown-save","status":"warning","reason":"save_failed","message":"doctor check non-ok"}`
	formattedInfoRecord    = `{"time":"2026-08-06T15:46:01Z","level":"info","command":"continuum.tick","version":"dev","commit":"abc123","pid":43,"component":"resurrect","operation":"save","status":"ok","duplicate":false,"partial":false,"duration_ms":4983,"message":"resurrect save completed"}`
)

func writeLogsTestFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sesh.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing logs test file: %v", err)
	}

	return path
}

func TestLogRendererUsesCompactComponentPrefixesAndFields(t *testing.T) {
	popupStartedRecord := []byte(`{"time":"2026-08-06T16:36:09Z","level":"info","component":"popup","command":"popup","message":"popup operation started"}` + "\n")
	popupCompletedRecord := []byte(`{"time":"2026-08-06T16:36:09Z","level":"info","component":"popup","command":"popup","message":"popup operation completed"}` + "\n")
	commandRecord := []byte(`{"time":"2026-08-06T16:36:09Z","level":"info","component":"command","command":"popup","message":"command completed"}` + "\n")
	resurrectRecord := []byte(`{"time":"2026-08-06T16:36:09Z","level":"info","component":"resurrect","command":"continuum.tick","message":"resurrect save started"}` + "\n")

	popupStartedLine := renderLogTestLine(t, popupStartedRecord)
	popupCompletedLine := renderLogTestLine(t, popupCompletedRecord)
	commandLine := renderLogTestLine(t, commandRecord)
	resurrectLine := renderLogTestLine(t, resurrectRecord)

	startedFragment := "popup: popup operation started command=popup"
	if !strings.Contains(popupStartedLine, startedFragment) {
		t.Fatalf("popup started line = %q, want compact message", popupStartedLine)
	}

	completedFragment := "popup: popup operation completed command=popup"
	if !strings.Contains(popupCompletedLine, completedFragment) {
		t.Fatalf("popup completed line = %q, want compact message", popupCompletedLine)
	}

	commandFragment := "command: command completed command=popup"
	if !strings.Contains(commandLine, commandFragment) {
		t.Fatalf("command line = %q, want compact message", commandLine)
	}

	resurrectFragment := "resurrect: resurrect save started command=continuum.tick"
	if !strings.Contains(resurrectLine, resurrectFragment) {
		t.Fatalf("resurrect line = %q, want compact message", resurrectLine)
	}

	longMessage := strings.Repeat("x", 100)
	longRecord := strings.Replace(
		string(commandRecord),
		`"message":"command completed"`,
		`"message":"`+longMessage+`"`,
		1,
	)

	longLine := renderLogTestLine(t, []byte(longRecord))

	if !strings.Contains(longLine, longMessage+" command=popup") {
		t.Fatalf("long message line = %q, want intact message and single-space field gap", longLine)
	}
}

func renderLogTestLine(t *testing.T, record []byte) string {
	t.Helper()

	line, err := RenderLogLine(record, Styles{verbose: true})
	if err != nil {
		t.Fatalf("RenderLogLine() error = %v", err)
	}

	return line
}

func TestLogWatchContinuesAfterCopyTruncateRotation(t *testing.T) {
	first := strings.Replace(formattedInfoRecord, `"message":"resurrect save completed"`, `"payload":"`+strings.Repeat("x", 128)+`","message":"first"`, 1) + "\n"
	second := strings.Replace(formattedInfoRecord, `"message":"resurrect save completed"`, `"message":"second"`, 1) + "\n"
	path := writeLogsTestFile(t, first)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	writer := &logWatchTestWriter{}
	writer.afterWrite = func(writeCount int) {
		switch writeCount {
		case 1:
			if err := os.WriteFile(path, []byte(second), 0o600); err != nil {
				t.Errorf("WriteFile() error = %v", err)
			}
		case 2:
			cancel()
		}
	}

	options := Options{Watch: true}
	if err := DisplayRetainedLogs(ctx, writer, io.Discard, path, options); err != nil {
		t.Fatalf("DisplayRetainedLogs() error = %v", err)
	}

	output := writer.String()

	if !strings.Contains(output, "resurrect") || !strings.Contains(output, "first") || !strings.Contains(output, "second") {
		t.Fatalf("watch output = %q, want records before and after truncation", output)
	}
}

func TestLogRendererPreservesExactNumbers(t *testing.T) {
	line := []byte(`{"level":"info","message":"observed","sequence":9007199254740993,"duration_ms":4983}`)

	text, err := RenderLogLine(line, NewLogStyles(false))
	if err != nil || !strings.Contains(text, "sequence=9007199254740993") || !strings.Contains(text, "duration=5.0s") {
		t.Fatalf("numbers changed in rendering: %q, %v", text, err)
	}
}

type failingLogOutput struct{ err error }

func (w failingLogOutput) Write([]byte) (int, error) { return 0, w.err }

func TestDisplayRetainedLogsPreservesJoinedOutputFailure(t *testing.T) {
	path := writeLogsTestFile(t, formattedInfoRecord+"\n")
	cause := errors.Join(context.Canceled, io.ErrUnexpectedEOF)

	err := DisplayRetainedLogs(t.Context(), failingLogOutput{err: cause}, io.Discard, path, Options{Raw: true})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("joined output failure was discarded: %v", err)
	}
}

func TestLogStreamWriterRejectsOversizedRecord(t *testing.T) {
	writer := NewLogStreamWriter(io.Discard, io.Discard, Options{Raw: true}, NewLogStyles(false))
	data := bytes.Repeat([]byte{'x'}, MaxLogRecordBytes+1)

	data[len(data)-1] = '\n'
	if _, err := writer.Write(data); !errors.Is(err, ErrLogRecordTooLong) {
		t.Fatalf("oversized complete record accepted: %v", err)
	}
}

func TestLogRendererAppliesStylesAndEscapesTerminalControlCharacters(t *testing.T) {
	colored, err := RenderLogLine([]byte(formattedWarningRecord+"\n"), NewLogStyles(true))
	if err != nil {
		t.Fatalf("RenderLogLine() color error = %v", err)
	}

	wantStyledParts := []string{
		lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render("2026-08-06 15:45:52"),
		lipgloss.NewStyle().Foreground(lipgloss.Color("192")).Bold(true).Render("WARN "),
		lipgloss.NewStyle().Bold(true).Faint(true).Render("doctor:"),
		lipgloss.NewStyle().Faint(true).Render("reason") +
			lipgloss.NewStyle().Faint(true).Render("=") + "save_failed",
	}
	for _, part := range wantStyledParts {
		if !strings.Contains(colored, part) {
			t.Fatalf("colored output = %q, want styled part %q", colored, part)
		}
	}

	unsafeRecord := []byte("{\"time\":\"2026-08-06T15:45:52Z\",\"level\":\"error\",\"component\":\"bad\\u001b[2J\",\"message\":\"line\\nnext\"}\n")

	safe, err := RenderLogLine(unsafeRecord, Styles{})
	if err != nil {
		t.Fatalf("RenderLogLine() safety error = %v", err)
	}

	if strings.Contains(safe, "\x1b") || strings.Contains(safe, "line\nnext") {
		t.Fatalf("formatted output contains terminal control characters: %q", safe)
	}

	if !strings.Contains(safe, `bad\x1b[2J`) || !strings.Contains(safe, `line\nnext`) {
		t.Fatalf("formatted output did not visibly escape controls: %q", safe)
	}
}

func TestHumanizeDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{-5, "0ms"},
		{0, "0ms"},
		{25, "25ms"},
		{954, "954ms"},
		{1623, "1.6s"},
		{2205, "2.2s"},
		{65000, "1.1m"},
		{86012836, "23.9h"},
	}
	for _, tt := range tests {
		if got := humanizeDuration(tt.ms); got != tt.want {
			t.Errorf("humanizeDuration(%d) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}

func TestLogRendererPrunesRedundantFieldsInCompactMode(t *testing.T) {
	record := []byte(`{"time":"2026-09-09T11:08:36Z","level":"info","component":"continuum","command":"continuum.startup","operation":"startup","status":"ok","boot_reconciled":false,"default_session_changed":false,"duration_ms":25,"hook_dispatched":true,"restore_partial":false,"restore_skipped_reason":"not_startup","restore_status":"skipped","message":"continuum startup completed"}` + "\n")

	compactLine, err := RenderLogLine(record, NewLogStyles(false, false))
	if err != nil {
		t.Fatalf("render compact: %v", err)
	}

	if strings.Contains(compactLine, "command=continuum.startup") {
		t.Errorf("compact line unexpectedly contains redundant command: %s", compactLine)
	}

	if strings.Contains(compactLine, "boot_reconciled=false") {
		t.Errorf("compact line unexpectedly contains false boolean: %s", compactLine)
	}

	if !strings.Contains(compactLine, "duration=25ms") {
		t.Errorf("compact line missing humanized duration: %s", compactLine)
	}

	verboseLine, err := RenderLogLine(record, NewLogStyles(false, true))
	if err != nil {
		t.Fatalf("render verbose: %v", err)
	}

	if !strings.Contains(verboseLine, "command=continuum.startup") {
		t.Errorf("verbose line missing command: %s", verboseLine)
	}

	if !strings.Contains(verboseLine, "boot_reconciled=false") {
		t.Errorf("verbose line missing false boolean: %s", verboseLine)
	}
}

func TestLineMatchesFilterWithSinceUntilAndGrep(t *testing.T) {
	warningLine := []byte(formattedWarningRecord + "\n")
	infoLine := []byte(formattedInfoRecord + "\n")

	t1, _ := time.Parse(time.RFC3339, "2026-08-06T15:46:00Z")

	sinceOpts := Options{Since: t1, HasSince: true}
	if lineMatchesFilter(warningLine, sinceOpts) {
		t.Error("warningLine unexpectedly matched Since filter")
	}

	if !lineMatchesFilter(infoLine, sinceOpts) {
		t.Error("infoLine should match Since filter")
	}

	untilOpts := Options{Until: t1, HasUntil: true}
	if !lineMatchesFilter(warningLine, untilOpts) {
		t.Error("warningLine should match Until filter")
	}

	if lineMatchesFilter(infoLine, untilOpts) {
		t.Error("infoLine unexpectedly matched Until filter")
	}

	grepOpts := Options{Grep: regexp.MustCompile(`doctor`)}
	if !lineMatchesFilter(warningLine, grepOpts) {
		t.Error("warningLine should match Grep filter")
	}

	if lineMatchesFilter(infoLine, grepOpts) {
		t.Error("infoLine unexpectedly matched Grep filter")
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		t    time.Time
		want string
	}{
		{now.Add(-2 * time.Second), "just now"},
		{now.Add(-30 * time.Second), "30s ago"},
		{now.Add(-15 * time.Minute), "15m ago"},
		{now.Add(-4 * time.Hour), "4h ago"},
		{now.Add(-3 * 24 * time.Hour), "3d ago"},
		{now.Add(-60 * 24 * time.Hour), "2026-07-11"},
	}

	for _, tt := range tests {
		if got := formatRelativeTime(tt.t, now); got != tt.want {
			t.Errorf("formatRelativeTime(%v) = %q, want %q", tt.t, got, tt.want)
		}
	}
}

func TestLogStylesStatusHighlighting(t *testing.T) {
	styles := NewLogStyles(true)

	degradedRecord := []byte(`{"time":"2026-08-06T15:46:01Z","level":"warn","component":"resurrect","status":"degraded","message":"save degraded"}` + "\n")

	rendered, err := RenderLogLine(degradedRecord, styles)
	if err != nil {
		t.Fatal(err)
	}

	wantDegraded := styles.warn.Render("degraded")
	if !strings.Contains(rendered, wantDegraded) {
		t.Errorf("rendered output missing warn-styled degraded: %s", rendered)
	}
}

type logWatchTestWriter struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	writeCount int
	afterWrite func(int)
}

func (w *logWatchTestWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	written, err := w.buf.Write(data)
	w.writeCount++

	if w.afterWrite != nil {
		w.afterWrite(w.writeCount)
	}

	if err != nil {
		return written, fmt.Errorf("writing watch test buffer: %w", err)
	}

	return written, nil
}

func (w *logWatchTestWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.buf.String()
}

func lineMatchesFilter(line []byte, options Options) bool {
	return logging.LineMatchesFilter(line, options.toLoggingFilter())
}
