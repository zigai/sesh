package fields_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/fields"
)

func shellField(script string) config.FieldCommand {
	return config.FieldCommand{ShellCommand: script, Command: "", Args: nil, Refresh: ""}
}

func sessionContext(path string) fields.Context {
	return fields.Context{
		SchemaVersion: 0,
		Field:         "probe",
		Row:           fields.RowSession,
		Session: fields.Session{
			ID: "$1", Name: "notes", Kind: "live", Path: path, Origin: "local", ConnectionID: "",
			WindowCount: 1, Attached: false, Activity: time.Time{},
			ActivePaneID: "%3", ActivePanePath: path, ActivePaneCommand: "zsh", ActivePaneTitle: "",
		},
		Agent: nil,
	}
}

func TestRunReturnsWholeStdoutAsOneLine(t *testing.T) {
	tests := map[string]struct {
		script string
		want   string
	}{
		"trailing newline": {script: `echo "main"`, want: "main"},
		"several lines":    {script: `printf 'one\ntwo\r\nthree\n\n'`, want: "one two three"},
		"empty":            {script: `true`, want: ""},
		"colors kept":      {script: `printf '\033[31mred\033[0m'`, want: "\x1b[31mred\x1b[0m"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result := fields.Run(t.Context(), shellField(tt.script), sessionContext(""))
			if result.Err != nil || result.Text != tt.want || result.ExitCode != 0 {
				t.Fatalf("Run() = %+v, want text %q", result, tt.want)
			}
		})
	}
}

func TestRunReportsExitCodeAndStderr(t *testing.T) {
	result := fields.Run(t.Context(), shellField(`echo partial; echo "no remote" >&2; exit 3`), sessionContext(""))
	if result.Err == nil || result.ExitCode != 3 || result.Stderr != "no remote\n" || result.Text != "partial" {
		t.Fatalf("Run() = %+v", result)
	}
}

func TestRunMissingCommandFails(t *testing.T) {
	command := config.FieldCommand{ShellCommand: "", Command: filepath.Join(t.TempDir(), "missing"), Args: nil, Refresh: ""}

	if result := fields.Run(t.Context(), command, sessionContext("")); result.Err == nil {
		t.Fatalf("Run() = %+v, want start error", result)
	}
}

func TestRunPassesContextOnStdinAndInEnvironment(t *testing.T) {
	t.Setenv("SESH_SESSION", "inherited")

	input := agentContext()
	input.Session.Origin = "ssh"

	encoded, err := input.Encode()
	if err != nil {
		t.Fatal(err)
	}

	stdin := fields.Run(t.Context(), shellField(`cat`), input)
	if stdin.Err != nil || stdin.Text != strings.TrimSuffix(string(encoded), "\n") {
		t.Fatalf("stdin = %+v, want %s", stdin, encoded)
	}

	env := fields.Run(t.Context(), shellField(`printf '%s|' "$SESH_FIELD" "$SESH_ROW" "$SESH_SESSION" "$SESH_SESSION_ID" "$SESH_ORIGIN" "$SESH_PANE_ID" "$SESH_AGENT_STATUS" "$SESH_AGENT_CWD" "$SESH_AGENT_PID"`), input)
	if want := "build|agent|api|$4|ssh|%12|running|/work/api/server|4242|"; env.Text != want {
		t.Fatalf("environment = %q, want %q", env.Text, want)
	}

	session := fields.Run(t.Context(), shellField(`printf '%s|%s|%s' "$SESH_ROW" "$SESH_PANE_ID" "$SESH_AGENT_CWD"`), sessionContext("/work/notes"))
	if session.Text != "session|%3|" {
		t.Fatalf("session environment = %q", session.Text)
	}
}

func TestRunWorkingDirectoryFollowsLocalRows(t *testing.T) {
	rowDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	inherited, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	remote := sessionContext(rowDir)
	remote.Session.Origin = "bridge"

	agent := agentContext()
	agent.Agent.CWD = rowDir

	for name, tt := range map[string]struct {
		input fields.Context
		want  string
	}{
		"session path":    {input: sessionContext(rowDir), want: rowDir},
		"agent cwd":       {input: agent, want: rowDir},
		"missing path":    {input: sessionContext(filepath.Join(rowDir, "gone")), want: inherited},
		"remote row path": {input: remote, want: inherited},
	} {
		t.Run(name, func(t *testing.T) {
			if got := fields.Run(t.Context(), shellField(`pwd -P`), tt.input).Text; got != tt.want {
				t.Fatalf("working directory = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunUsesShForStringsAndArgsForCommands(t *testing.T) {
	t.Setenv("SHELL", "/bin/false")

	if got := fields.Run(t.Context(), shellField(`printf '%s:%s' "$0" "$1"`), sessionContext("")).Text; got != "/bin/sh:" {
		t.Fatalf("shell string ran as %q", got)
	}

	command := config.FieldCommand{ShellCommand: "", Command: "printf", Args: []string{"%s-%s", "a b", "c"}, Refresh: ""}
	if got := fields.Run(t.Context(), command, sessionContext("")).Text; got != "a b-c" {
		t.Fatalf("command form ran as %q", got)
	}
}

func TestRunCancelKillsBackgroundChildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		waitForFile(t, pidFile)
		cancel()
	}()

	result := fields.Run(ctx, shellField(`sleep 300 & echo $! > `+pidFile+`; wait`), sessionContext(""))
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", result.Err)
	}

	waitForExit(t, readPID(t, pidFile))
}

func TestRunCancelReturnsWhileDetachedChildHoldsStdout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := `perl -e 'use POSIX; POSIX::setsid() or die; open(my $f, ">", $ARGV[0]) or die; print $f "$$"; close $f; sleep 300' ` + pidFile + ` & echo shown`

	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		waitForFile(t, pidFile)
		cancel()
	}()

	result := fields.Run(ctx, shellField(script), sessionContext(""))

	pid := readPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	if !errors.Is(result.Err, context.Canceled) || result.Text != "shown" {
		t.Fatalf("Run() = %+v, want canceled with text read so far", result)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
			return
		}
	}

	t.Errorf("%s was not written", path)
}

func readPID(t *testing.T, path string) int {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}

	return pid
}

func waitForExit(t *testing.T, pid int) {
	t.Helper()

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
	}

	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("background child %d survived cancellation", pid)
}
