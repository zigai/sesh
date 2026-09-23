package cmdlogs_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func generateNumberedLogRecords(count int, component string, level string) string {
	var builder strings.Builder
	for i := range count {
		fmt.Fprintf(&builder, `{"time":"2026-08-06T15:45:%02dZ","level":%q,"component":%q,"command":"test","message":"event %d"}`+"\n",
			i%60,
			level,
			component,
			i+1)
	}

	return builder.String()
}

func countLogLines(output string) int {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return 0
	}

	return len(strings.Split(trimmed, "\n"))
}

func testDefaultFiftyLineLimit(t *testing.T, path string) {
	t.Helper()

	output, _, err := executeLogsCommand(t, path, testLogsCmd)
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}

	if got := countLogLines(output); got != 50 {
		t.Fatalf("line count = %d, want 50", got)
	}

	if strings.Contains(output, "event 1 ") || strings.Contains(output, "event 10 ") {
		t.Fatalf("output contains older records outside 50-line tail: %s", output)
	}

	if !strings.Contains(output, "event 60 ") || !strings.Contains(output, "event 11 ") {
		t.Fatalf("output missing newest records in tail: %s", output)
	}
}

func testExplicitLinesFlag(t *testing.T, path string) {
	t.Helper()

	output, _, err := executeLogsCommand(t, path, testLogsCmd, "-n", "5")
	if err != nil {
		t.Fatalf("execute -n 5 error: %v", err)
	}

	if got := countLogLines(output); got != 5 {
		t.Fatalf("line count = %d, want 5", got)
	}

	if !strings.Contains(output, "event 56 ") || !strings.Contains(output, "event 60 ") {
		t.Fatalf("output does not contain last 5 events: %s", output)
	}
}

func testAllFlagUnlimited(t *testing.T, path string) {
	t.Helper()

	output, _, err := executeLogsCommand(t, path, testLogsCmd, "--all")
	if err != nil {
		t.Fatalf("execute --all error: %v", err)
	}

	if got := countLogLines(output); got != 60 {
		t.Fatalf("line count = %d, want 60", got)
	}

	if !strings.Contains(output, "event 1 ") || !strings.Contains(output, "event 60 ") {
		t.Fatalf("output missing records: %s", output)
	}

	outputZero, _, err := executeLogsCommand(t, path, testLogsCmd, "-n", "0")
	if err != nil {
		t.Fatalf("execute -n 0 error: %v", err)
	}

	if got := countLogLines(outputZero); got != 60 {
		t.Fatalf("line count with -n 0 = %d, want 60", got)
	}
}

func TestLogsCommandLineBounding(t *testing.T) {
	data := generateNumberedLogRecords(60, "resurrect", "info")
	path := writeLogsTestFile(t, data)

	t.Run("default 50 lines", func(t *testing.T) {
		testDefaultFiftyLineLimit(t, path)
	})

	t.Run("lines flag -n 5", func(t *testing.T) {
		testExplicitLinesFlag(t, path)
	})

	t.Run("all flag and -n 0", func(t *testing.T) {
		testAllFlagUnlimited(t, path)
	})
}

func TestLogsCommandMultiFileBounding(t *testing.T) {
	older := generateNumberedLogRecords(30, "resurrect", "info")

	var newer strings.Builder
	for i := range 30 {
		fmt.Fprintf(
			&newer, `{"time":"2026-08-06T15:46:%02dZ","level":"info","component":"resurrect","command":"test","message":"event %d"}`+"\n",
			i%60,
			31+i,
		)
	}

	path := writeLogsTestFile(t, newer.String())
	if err := os.WriteFile(path+".1", []byte(older), 0o600); err != nil {
		t.Fatalf("writing older log file: %v", err)
	}

	output, _, err := executeLogsCommand(t, path, testLogsCmd)
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}

	if got := countLogLines(output); got != 50 {
		t.Fatalf("multi-file line count = %d, want 50", got)
	}

	if strings.Contains(output, "event 1 ") || strings.Contains(output, "event 10 ") {
		t.Fatalf("output contains events 1..10 that should have been pruned: %s", output)
	}

	if !strings.Contains(output, "event 11 ") || !strings.Contains(output, "event 60 ") {
		t.Fatalf("output missing events in 50-line window: %s", output)
	}
}

func testLevelFiltering(t *testing.T, path string) {
	t.Helper()

	output, _, err := executeLogsCommand(t, path, testLogsCmd, "-a", "--level", "warn")
	if err != nil {
		t.Fatalf("execute --level warn error: %v", err)
	}

	if strings.Contains(output, "alpha-info") || strings.Contains(output, "delta-debug") || strings.Contains(output, "echo-info") {
		t.Fatalf("output contains records below warn: %s", output)
	}

	if !strings.Contains(output, "bravo-warn") || !strings.Contains(output, "charlie-error") {
		t.Fatalf("output missing warn or error records: %s", output)
	}

	outputError, _, err := executeLogsCommand(t, path, testLogsCmd, "-a", "-l", "error")
	if err != nil {
		t.Fatalf("execute -l error: %v", err)
	}

	if strings.Contains(outputError, "bravo-warn") || strings.Contains(outputError, "alpha-info") {
		t.Fatalf("output contains records below error: %s", outputError)
	}

	if !strings.Contains(outputError, "charlie-error") {
		t.Fatalf("output missing error record: %s", outputError)
	}
}

func testComponentFiltering(t *testing.T, path string) {
	t.Helper()

	output, _, err := executeLogsCommand(t, path, testLogsCmd, "-a", "--component", "remote")
	if err != nil {
		t.Fatalf("execute --component remote error: %v", err)
	}

	if strings.Contains(output, "resurrect") || strings.Contains(output, "tui") {
		t.Fatalf("output contains non-remote components: %s", output)
	}

	if !strings.Contains(output, "charlie-error") || !strings.Contains(output, "delta-debug") {
		t.Fatalf("output missing remote records: %s", output)
	}
}

func testCombinedAndInvalidFilters(t *testing.T, path string) {
	t.Helper()

	output, _, err := executeLogsCommand(t, path, testLogsCmd, "-a", "-l", "warn", "-c", "resurrect")
	if err != nil {
		t.Fatalf("execute combined filter error: %v", err)
	}

	if got := countLogLines(output); got != 1 {
		t.Fatalf("line count = %d, want 1", got)
	}

	if !strings.Contains(output, "bravo-warn") {
		t.Fatalf("output missing combined match: %s", output)
	}

	out, stderr, err := executeLogsCommand(t, path, testLogsCmd, "--level", "invalid_level")
	_ = out
	_ = stderr

	if err == nil {
		t.Fatal("execute with invalid level error = nil, want usage error")
	}
}

func TestLogsCommandFiltering(t *testing.T) {
	dataset := strings.Join([]string{
		`{"time":"2026-08-06T15:45:01Z","level":"info","component":"resurrect","command":"test","message":"alpha-info"}`,
		`{"time":"2026-08-06T15:45:02Z","level":"warn","component":"resurrect","command":"test","message":"bravo-warn"}`,
		`{"time":"2026-08-06T15:45:03Z","level":"error","component":"remote","command":"test","message":"charlie-error"}`,
		`{"time":"2026-08-06T15:45:04Z","level":"debug","component":"remote","command":"test","message":"delta-debug"}`,
		`{"time":"2026-08-06T15:45:05Z","level":"info","component":"tui","command":"test","message":"echo-info"}`,
	}, "\n") + "\n"

	path := writeLogsTestFile(t, dataset)

	t.Run("level filtering", func(t *testing.T) {
		testLevelFiltering(t, path)
	})

	t.Run("component filtering", func(t *testing.T) {
		testComponentFiltering(t, path)
	})

	t.Run("combined and invalid filters", func(t *testing.T) {
		testCombinedAndInvalidFilters(t, path)
	})
}
