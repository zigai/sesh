package cmdlogs_test

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/cli/clibase"
)

const (
	formattedWarningRecord = `{"time":"2026-08-06T15:45:52.328165852Z","level":"warn","command":"doctor","version":"dev","commit":"abc123","pid":42,"component":"doctor","check":"continuum-shutdown-save","status":"warning","reason":"save_failed","message":"doctor check non-ok"}`
	formattedInfoRecord    = `{"time":"2026-08-06T15:46:01Z","level":"info","command":"continuum.tick","version":"dev","commit":"abc123","pid":43,"component":"resurrect","operation":"save","status":"ok","duplicate":false,"partial":false,"duration_ms":4983,"message":"resurrect save completed"}`
)

func TestLogsCommandFormatsStructuredRecordsWithoutSelfLogging(t *testing.T) {
	path := writeLogsTestFile(t, formattedWarningRecord+"\n"+formattedInfoRecord+"\n")

	output, stderr, err := executeLogsCommand(t, path, testLogsCmd)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	stored, readErr := os.ReadFile(path)
	if readErr != nil || string(stored) != formattedWarningRecord+"\n"+formattedInfoRecord+"\n" {
		t.Fatalf("viewing changed log contents: %v", readErr)
	}

	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	want := "" +
		"2026-08-06 15:45:52  WARN   doctor          " +
		"doctor check non-ok" + strings.Repeat(" ", 19) +
		"status=warning, reason=save_failed, check=continuum-shutdown-save\n" +
		"2026-08-06 15:46:01  INFO   resurrect       " +
		"resurrect save completed" + strings.Repeat(" ", 14) +
		"command=continuum.tick, operation=save, status=ok, duration=5.0s\n"
	if output != want {
		t.Fatalf("logs output =\n%q\nwant:\n%q", output, want)
	}
}

func TestLogsCommandVerboseShowsAllFields(t *testing.T) {
	path := writeLogsTestFile(t, formattedWarningRecord+"\n"+formattedInfoRecord+"\n")

	output, stderr, err := executeLogsCommand(t, path, testLogsCmd, "--verbose")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	if !strings.Contains(output, "command=doctor") || !strings.Contains(output, "duplicate=false") {
		t.Fatalf("verbose output missing unpruned fields: %s", output)
	}
}

func TestLogsCommandJSONWritesRawLogBytes(t *testing.T) {
	raw := formattedWarningRecord + "\nnot-json\n"
	path := writeLogsTestFile(t, raw)

	output, stderr, err := executeLogsCommand(t, path, testLogsCmd, "--json")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if output != raw {
		t.Fatalf("raw logs output = %q, want %q", output, raw)
	}

	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

func TestLogsCommandPrettyPrintsIndentedJSON(t *testing.T) {
	path := writeLogsTestFile(t, formattedWarningRecord+"\n")

	output, stderr, err := executeLogsCommand(t, path, testLogsCmd, "--pretty")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	if !strings.Contains(output, "  \"level\": \"warn\",\n") || !strings.Contains(output, "  \"command\": \"doctor\",\n") {
		t.Fatalf("pretty output not indented:\n%s", output)
	}
}

func TestLogsCommandPath(t *testing.T) {
	path := writeLogsTestFile(t, formattedWarningRecord+"\n")

	output, stderr, err := executeLogsCommand(t, path, testLogsCmd, "--path")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	if strings.TrimSpace(output) != path {
		t.Fatalf("logs --path output = %q, want %q", strings.TrimSpace(output), path)
	}
}

func TestLogsCommandClear(t *testing.T) {
	path := writeLogsTestFile(t, formattedWarningRecord+"\n")
	if err := os.WriteFile(path+".1", []byte("old backup\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	output, stderr, err := executeLogsCommand(t, path, testLogsCmd, "--clear")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	if !strings.Contains(output, "Cleared diagnostic log") {
		t.Fatalf("clear output = %q, want Cleared diagnostic log", output)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) != 0 {
		t.Fatalf("cleared file len = %d, want 0", len(data))
	}

	if _, err := os.Stat(path + ".1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup still exists after clear: %v", err)
	}
}

func TestLogsCommandColorFlag(t *testing.T) {
	path := writeLogsTestFile(t, formattedWarningRecord+"\n")

	alwaysOut, _, err := executeLogsCommand(t, path, testLogsCmd, "--color", "always")
	if err != nil {
		t.Fatalf("Execute(--color always) error = %v", err)
	}

	if !strings.Contains(alwaysOut, "\x1b[") {
		t.Fatalf("output = %q, want ANSI escape codes with --color always", alwaysOut)
	}

	neverOut, _, err := executeLogsCommand(t, path, testLogsCmd, "--color", "never")
	if err != nil {
		t.Fatalf("Execute(--color never) error = %v", err)
	}

	if strings.Contains(neverOut, "\x1b[") {
		t.Fatalf("output = %q, want no ANSI escape codes with --color never", neverOut)
	}
}

func TestLogsCommandGrep(t *testing.T) {
	path := writeLogsTestFile(t, formattedWarningRecord+"\n"+formattedInfoRecord+"\n")

	output, _, err := executeLogsCommand(t, path, testLogsCmd, "--grep", "doctor")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !strings.Contains(output, "doctor check non-ok") {
		t.Fatalf("grep output missing matching record: %s", output)
	}

	if strings.Contains(output, "resurrect save completed") {
		t.Fatalf("grep output contains non-matching record: %s", output)
	}
}

func TestLogsCommandSinceFilter(t *testing.T) {
	path := writeLogsTestFile(t, formattedWarningRecord+"\n"+formattedInfoRecord+"\n")

	output, _, err := executeLogsCommand(t, path, testLogsCmd, "--since", "2026-08-06 15:46:00")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if strings.Contains(output, "doctor check non-ok") {
		t.Fatalf("output contains record before since: %s", output)
	}

	if !strings.Contains(output, "resurrect save completed") {
		t.Fatalf("output missing record after since: %s", output)
	}
}

func TestLogsCommandRelative(t *testing.T) {
	recentRecord := fmt.Sprintf(`{"time":%q,"level":"info","component":"popup","message":"recent event"}`+"\n",
		time.Now().UTC().Format(time.RFC3339Nano))
	path := writeLogsTestFile(t, recentRecord)

	output, _, err := executeLogsCommand(t, path, testLogsCmd, "--relative")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !strings.Contains(output, "just now") && !strings.Contains(output, "s ago") {
		t.Fatalf("output = %q, want relative time indicator", output)
	}
}

func TestLogsCommandDisplaysRetainedBackupsOldestFirst(t *testing.T) {
	active := strings.Replace(formattedInfoRecord, `"message":"resurrect save completed"`, `"message":"active"`, 1) + "\n"
	newer := strings.Replace(formattedInfoRecord, `"message":"resurrect save completed"`, `"message":"newer backup"`, 1) + "\n"
	oldest := strings.Replace(formattedInfoRecord, `"message":"resurrect save completed"`, `"message":"oldest backup"`, 1) + "\n"

	path := writeLogsTestFile(t, active)
	if err := os.WriteFile(path+".1", []byte(newer), 0o600); err != nil {
		t.Fatalf("WriteFile(.1) error = %v", err)
	}

	if err := os.WriteFile(path+".3", []byte(oldest), 0o600); err != nil {
		t.Fatalf("WriteFile(.3) error = %v", err)
	}

	output, _, err := executeLogsCommand(t, path, testLogsCmd, "--json")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if want := oldest + newer + active; output != want {
		t.Fatalf("retained logs output = %q, want %q", output, want)
	}
}

func TestLogsCommandWatchDisplaysExistingRawRecordsAndStopsOnCancellation(t *testing.T) {
	path := writeLogsTestFile(t, formattedWarningRecord+"\n")

	output, _, err := executeCanceledLogsCommand(t, path, testLogsCmd, "--watch", "--json")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if output != formattedWarningRecord+"\n" {
		t.Fatalf("watch output = %q", output)
	}
}

func TestFormattedLogsOmitCorruptRecordsAndContinue(t *testing.T) {
	active := strings.Replace(formattedInfoRecord, `"message":"resurrect save completed"`, `"message":"active"`, 1) + "\n"

	path := writeLogsTestFile(t, active)
	if err := os.WriteFile(path+".1", []byte("not-json\n"+formattedWarningRecord+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(.1) error = %v", err)
	}

	output, stderr, err := executeLogsCommand(t, path, testLogsCmd)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	for _, want := range []string{
		"resurrect       active",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("logs output = %q, want containing %q", output, want)
		}
	}

	if !strings.Contains(stderr, "warning: omitted malformed diagnostic log record 1") || strings.Contains(output, "warning:") {
		t.Fatalf("malformed-record diagnostic streams: stdout=%q stderr=%q", output, stderr)
	}
}

func TestLogsCommandRejectsNegativeLines(t *testing.T) {
	path := writeLogsTestFile(t, formattedInfoRecord+"\n")

	for _, flag := range [][]string{{"-n", "-1"}, {"--lines", "-5"}, {"--tail", "-2"}} {
		args := append([]string{testLogsCmd}, flag...)

		_, _, err := executeLogsCommand(t, path, args...)
		if err == nil {
			t.Fatalf("executeLogsCommand(%v) succeeded, want error", flag)
		}

		if clibase.ExitCode(err) != exitCodeUsage {
			t.Fatalf("executeLogsCommand(%v) exit code = %d, want %d", flag, clibase.ExitCode(err), exitCodeUsage)
		}
	}
}

func TestLogsCommandFlagIncompatibilities(t *testing.T) {
	path := writeLogsTestFile(t, formattedInfoRecord+"\n")

	incompatibleCases := [][]string{
		{testLogsCmd, "--path", "--clear"},
		{testLogsCmd, "--path", "--watch"},
		{testLogsCmd, "--path", "--pretty"},
		{testLogsCmd, "--path", "--verbose"},
		{testLogsCmd, "--path", "--lines", "10"},
		{testLogsCmd, "--path", "--all"},
		{testLogsCmd, "--path", "--level", "info"},
		{testLogsCmd, "--path", "--component", "cli"},
		{testLogsCmd, "--path", "--since", "1h"},
		{testLogsCmd, "--path", "--until", "1h"},
		{testLogsCmd, "--path", "--grep", "event"},
		{testLogsCmd, "--path", "--relative"},
		{testLogsCmd, "--clear", "--watch"},
		{testLogsCmd, "--clear", "--pretty"},
		{testLogsCmd, "--clear", "--verbose"},
		{testLogsCmd, "--clear", "--lines", "10"},
		{testLogsCmd, "--clear", "--all"},
		{testLogsCmd, "--clear", "--level", "info"},
		{testLogsCmd, "--clear", "--component", "cli"},
		{testLogsCmd, "--clear", "--since", "1h"},
		{testLogsCmd, "--clear", "--until", "1h"},
		{testLogsCmd, "--clear", "--grep", "event"},
		{testLogsCmd, "--clear", "--relative"},
		{testLogsCmd, "--all", "--lines", "10"},
		{testLogsCmd, "--color", "invalid_color"},
		{testLogsCmd, "--since", "invalid_time"},
		{testLogsCmd, "--until", "invalid_time"},
		{testLogsCmd, "--since", "-10m"},
		{testLogsCmd, "--since", "1h", "--until", "2h"},
	}

	for _, tc := range incompatibleCases {
		t.Run(strings.Join(tc, " "), func(t *testing.T) {
			_, stderr, err := executeLogsCommand(t, path, tc...)
			if err == nil {
				t.Fatalf("expected error for incompatible flags %v", tc)
			}

			if clibase.ExitCode(err) != exitCodeUsage {
				t.Fatalf("exit code = %d, want %d (usage)", clibase.ExitCode(err), exitCodeUsage)
			}

			for _, privateArg := range []string{"invalid_color", "invalid_time", "-10m"} {
				if strings.Contains(stderr, privateArg) {
					t.Fatalf("stderr leaked private argument %q: %s", privateArg, stderr)
				}
			}
		})
	}
}

func TestLogsCommandJSONEnvelopesForPathAndClear(t *testing.T) {
	path := writeLogsTestFile(t, formattedInfoRecord+"\n")

	testLogsPathJSONEnvelope(t, path)
	testLogsClearJSONEnvelope(t, path)
}

func testLogsPathJSONEnvelope(t *testing.T, path string) {
	t.Helper()

	out, stderr, err := executeLogsCommand(t, path, testLogsCmd, "--path", "--json")
	if err != nil {
		t.Fatalf("execute logs --path --json error = %v", err)
	}

	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}

	if !strings.Contains(out, `"schemaVersion": 1`) || !strings.Contains(out, `"command": "logs"`) || !strings.Contains(out, `"ok": true`) || !strings.Contains(out, path) {
		t.Fatalf("unexpected envelope for --path: %s", out)
	}
}

func testLogsClearJSONEnvelope(t *testing.T, path string) {
	t.Helper()

	out, stderr, err := executeLogsCommand(t, path, testLogsCmd, "--clear", "--json")
	if err != nil {
		t.Fatalf("execute logs --clear --json error = %v", err)
	}

	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}

	if !strings.Contains(out, `"schemaVersion": 1`) || !strings.Contains(out, `"command": "logs"`) || !strings.Contains(out, `"cleared": true`) || !strings.Contains(out, path) {
		t.Fatalf("unexpected envelope for --clear: %s", out)
	}
}
