//go:build integration

package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/cli"
	"github.com/zigai/sesh/internal/tui/logview"
)

const (
	testLogsCmd         = "logs"
	formattedInfoRecord = `{"time":"2026-08-06T15:46:01Z","level":"info","command":"continuum.tick","version":"dev","commit":"abc123","pid":43,"component":"resurrect","operation":"save","status":"ok","duplicate":false,"partial":false,"duration_ms":4983,"message":"resurrect save completed"}`
	exitCodePipe        = 141
)

type logWatchTestWriter struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	writeCount int
	afterWrite func(count int)
}

func (w *logWatchTestWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.writeCount++

	// bytes.Buffer.Write always consumes all of p and never returns an error.
	_, _ = w.buf.Write(p)

	if w.afterWrite != nil {
		w.afterWrite(w.writeCount)
	}

	return len(p), nil
}

func (w *logWatchTestWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.buf.String()
}

type failingRuntimeWriter struct{}

func (failingRuntimeWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestLogsBinaryIntegration(t *testing.T) {
	binaryDir := t.TempDir()
	binaryPath := filepath.Join(binaryDir, "sesh")

	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}

	buildCmd := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-o", binaryPath, "./cmd/sesh")

	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\noutput: %s", err, out)
	}

	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "diagnostic.log")
	t.Setenv("SESH_PATHS_LOG_FILE", logPath)

	configPath := filepath.Join(tempDir, "config.toml")

	configContent := fmt.Sprintf("[paths]\nlog_file = %q\n", logPath)
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	record1 := `{"time":"2026-09-20T12:00:00Z","level":"info","component":"cli","message":"system boot"}` + "\n"

	record2 := `{"time":"2026-09-20T12:01:00Z","level":"warn","component":"engine","message":"high memory"}` + "\n"
	if err := os.WriteFile(logPath, []byte(record1+record2), 0o600); err != nil {
		t.Fatalf("writing log: %v", err)
	}

	t.Run("finite human output", func(t *testing.T) {
		testBinaryHumanOutput(t, binaryPath, configPath)
	})

	t.Run("finite JSONL stream", func(t *testing.T) {
		testBinaryJSONLStream(t, binaryPath, configPath)
	})

	t.Run("path prints configured log path", func(t *testing.T) {
		testBinaryPathText(t, binaryPath, configPath, logPath)
	})

	t.Run("path with JSON envelope", func(t *testing.T) {
		testBinaryPathJSON(t, binaryPath, configPath, logPath)
	})
	t.Run("watch exits after interrupt", func(t *testing.T) {
		testBinaryWatchInterrupt(t, binaryPath, configPath)
	})

	t.Run("broken pipe exits quietly", func(t *testing.T) {
		testBinaryLogsBrokenPipe(t, binaryPath, configPath)
	})

	t.Run("clear with JSON envelope", func(t *testing.T) {
		testBinaryClearJSON(t, binaryPath, configPath, logPath)
	})
}

func testBinaryLogsBrokenPipe(t *testing.T, binaryPath, configPath string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pipe.log")

	data := bytes.Repeat([]byte(formattedInfoRecord+"\n"), 10000)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SESH_PATHS_LOG_FILE", path)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, testConfigFlag, configPath, testLogsCmd, "--all", "--json")
	cmd.WaitDelay = time.Second

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	_, readErr := bufio.NewReader(output).ReadBytes('\n')
	closeErr := output.Close()
	waitErr := cmd.Wait()

	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}

	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("pipe close did not interrupt output: %v", waitErr)
	}

	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || (exitErr.ExitCode() != exitCodePipe && status.Signal() != syscall.SIGPIPE) {
		t.Fatalf("unexpected pipe exit: %v", waitErr)
	}

	if stderr.Len() != 0 || ctx.Err() != nil {
		t.Fatalf("pipe failure was noisy or hung: %q, %v", stderr.String(), ctx.Err())
	}
}

func testBinaryWatchInterrupt(t *testing.T, binaryPath, configPath string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, testConfigFlag, configPath, testLogsCmd, "--watch", "--json")
	cmd.WaitDelay = time.Second

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(out)
	line, readErr := reader.ReadBytes('\n')
	signalErr := cmd.Process.Signal(os.Interrupt)
	_, drainErr := io.Copy(io.Discard, reader)

	waitErr := cmd.Wait()

	if err := errors.Join(readErr, signalErr, drainErr); err != nil {
		t.Fatalf("watch interrupted: %v, %q", err, stderr.String())
	}

	exit, ok := errors.AsType[*exec.ExitError](waitErr)
	if !ok || exit.ExitCode() != 130 {
		t.Fatalf("watch exit = %v, want 130", waitErr)
	}

	if !json.Valid(line) || !bytes.Contains(line, []byte("system boot")) || stderr.Len() != 0 {
		t.Fatalf("watch streams: %q, %q", line, stderr.String())
	}
}

func testBinaryHumanOutput(t *testing.T, binaryPath, configPath string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), binaryPath, "--config", configPath, testLogsCmd)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("logs error: %v\noutput: %s", err, out)
	}

	if !strings.Contains(string(out), "system boot") || !strings.Contains(string(out), "high memory") {
		t.Fatalf("missing records in human output: %s", out)
	}
}

func testBinaryJSONLStream(t *testing.T, binaryPath, configPath string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), binaryPath, "--config", configPath, testLogsCmd, "--json")

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout

	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("logs --json error: %v\nstderr: %s", err, stderr.String())
	}

	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr in JSONL mode: %s", stderr.String())
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d JSONL lines, want 2: %s", len(lines), stdout.String())
	}

	for _, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line is not valid JSON: %s (err: %v)", line, err)
		}
	}
}

func testBinaryPathText(t *testing.T, binaryPath, configPath, logPath string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), binaryPath, "--config", configPath, testLogsCmd, "--path")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("logs --path error: %v\noutput: %s", err, out)
	}

	if strings.TrimSpace(string(out)) != logPath {
		t.Fatalf("logs --path = %q, want %q", strings.TrimSpace(string(out)), logPath)
	}
}

func testBinaryPathJSON(t *testing.T, binaryPath, configPath, logPath string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), binaryPath, "--config", configPath, testLogsCmd, "--path", "--json")

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout

	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("logs --path --json error: %v\nstderr: %s", err, stderr.String())
	}

	var env testJSONEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\noutput: %s", err, stdout.String())
	}

	if !env.OK || env.Command != testLogsCmd || env.SchemaVersion != 1 {
		t.Fatalf("unexpected envelope: %+v", env)
	}

	var res map[string]any
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if res["path"] != logPath {
		t.Fatalf("result.path = %v, want %q", res["path"], logPath)
	}

	assertAllKeysCamelCase(t, res, "result")
}

func testBinaryClearJSON(t *testing.T, binaryPath, configPath, logPath string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), binaryPath, "--config", configPath, testLogsCmd, "--clear", "--json")

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout

	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("logs --clear --json error: %v\nstderr: %s", err, stderr.String())
	}

	var env testJSONEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\noutput: %s", err, stdout.String())
	}

	if !env.OK || env.Command != testLogsCmd || env.SchemaVersion != 1 {
		t.Fatalf("unexpected envelope: %+v", env)
	}

	var res map[string]any
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if res["cleared"] != true || res["path"] != logPath {
		t.Fatalf("result = %+v", res)
	}

	assertAllKeysCamelCase(t, res, "result")
}

func TestLogsFollowRenameAndTruncateContinuity(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "continuity.log")
	rec1 := `{"time":"2026-09-20T12:00:00Z","level":"info","component":"cli","payload":"` + strings.Repeat("x", 128) + `","message":"msg1"}` + "\n"
	rec2 := `{"time":"2026-09-20T12:01:00Z","level":"info","component":"cli","message":"msg2"}` + "\n"
	rec3 := `{"time":"2026-09-20T12:02:00Z","level":"info","component":"cli","message":"msg3"}` + "\n"

	if err := os.WriteFile(logPath, []byte(rec1), 0o600); err != nil {
		t.Fatalf("writing log: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	writer := &logWatchTestWriter{
		mu:         sync.Mutex{},
		buf:        bytes.Buffer{},
		writeCount: 0,
		afterWrite: nil,
	}
	writer.afterWrite = func(count int) {
		switch count {
		case 1:
			if err := os.WriteFile(logPath, []byte(rec2), 0o600); err != nil {
				t.Errorf("overwriting log for copy-truncate: %v", err)
			}
		case 2:
			if err := os.Rename(logPath, logPath+".1"); err != nil {
				t.Errorf("rename rotation: %v", err)
			}

			if err := os.WriteFile(logPath, []byte(rec3), 0o600); err != nil {
				t.Errorf("write new log after rename: %v", err)
			}
		case 3:
			cancel()
		}
	}

	options := logview.Options{
		Raw:       true,
		Pretty:    false,
		Watch:     true,
		Color:     false,
		Verbose:   false,
		Relative:  false,
		Lines:     0,
		Level:     0,
		HasLevel:  false,
		Component: "",
		Since:     time.Time{},
		HasSince:  false,
		Until:     time.Time{},
		HasUntil:  false,
		Grep:      nil,
	}
	if err := logview.DisplayRetainedLogs(ctx, writer, io.Discard, logPath, options); err != nil {
		t.Fatalf("displayRetainedLogs: %v", err)
	}

	out := writer.String()
	if !strings.Contains(out, "msg1") || !strings.Contains(out, "msg2") || !strings.Contains(out, "msg3") {
		t.Fatalf("output missing records across copy-truncate and rename: %s", out)
	}
}

func TestLogsHostileRecordsAndTerminalSanitization(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "hostile.log")

	hostileRecord := "{\"time\":\"2026-09-20T12:00:00Z\",\"level\":\"error\",\"component\":\"evil\\u001b[2J\",\"message\":\"msg\\u001b[H\\u001b[?25l\"}\n"
	if err := os.WriteFile(logPath, []byte(hostileRecord), 0o600); err != nil {
		t.Fatalf("writing hostile log: %v", err)
	}

	var buf bytes.Buffer

	options := logview.Options{
		Raw:       false,
		Pretty:    false,
		Watch:     false,
		Color:     false,
		Verbose:   false,
		Relative:  false,
		Lines:     10,
		Level:     0,
		HasLevel:  false,
		Component: "",
		Since:     time.Time{},
		HasSince:  false,
		Until:     time.Time{},
		HasUntil:  false,
		Grep:      nil,
	}
	if err := logview.DisplayRetainedLogs(t.Context(), &buf, io.Discard, logPath, options); err != nil {
		t.Fatalf("displayRetainedLogs error: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("human output leaked raw terminal escape byte: %q", out)
	}

	if !strings.Contains(out, `evil\x1b[2J`) {
		t.Fatalf("expected escaped graphic control in output: %q", out)
	}
}

func TestLogsBrokenWriterNoRecursion(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "broken.log")

	rec := `{"time":"2026-09-20T12:00:00Z","level":"info","component":"cli","message":"hello"}` + "\n"
	if err := os.WriteFile(logPath, []byte(rec), 0o600); err != nil {
		t.Fatalf("writing log: %v", err)
	}

	options := logview.Options{
		Raw:       false,
		Pretty:    false,
		Watch:     false,
		Color:     false,
		Verbose:   false,
		Relative:  false,
		Lines:     10,
		Level:     0,
		HasLevel:  false,
		Component: "",
		Since:     time.Time{},
		HasSince:  false,
		Until:     time.Time{},
		HasUntil:  false,
		Grep:      nil,
	}

	err := logview.DisplayRetainedLogs(t.Context(), failingRuntimeWriter{}, io.Discard, logPath, options)
	if err == nil {
		t.Fatalf("expected error for failing writer")
	}

	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("expected io.ErrClosedPipe, got %v", err)
	}
}

func TestLogsMissingLogPathDoesNotCreateFiles(t *testing.T) {
	tempDir := t.TempDir()
	nonexistentLog := filepath.Join(tempDir, "nonexistent", "sub", "sesh.log")
	t.Setenv("SESH_PATHS_LOG_FILE", nonexistentLog)

	configFile := filepath.Join(tempDir, "config.toml")

	configContent := fmt.Sprintf("[paths]\nlog_file = %q\n", nonexistentLog)
	if err := os.WriteFile(configFile, []byte(configContent), 0o600); err != nil {
		t.Fatalf("WriteFile(config.toml): %v", err)
	}

	cmd := cli.NewRootCommand(t.Context())

	stdout, stderr, err := executeCommand(cmd, "--config", configFile, testLogsCmd, "--path")
	if err != nil {
		t.Fatalf("logs --path failed: %v, stderr: %s", err, stderr)
	}

	if strings.TrimSpace(stdout) != nonexistentLog {
		t.Fatalf("logs --path = %q, want %q", strings.TrimSpace(stdout), nonexistentLog)
	}

	if _, err := os.Stat(filepath.Dir(nonexistentLog)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logs --path created directory: %v", err)
	}

	cmd = cli.NewRootCommand(t.Context())

	stdout, stderr, err = executeCommand(cmd, "--config", configFile, testLogsCmd, "--clear")
	if err != nil {
		t.Fatalf("logs --clear failed: %v, stderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "Cleared diagnostic log") {
		t.Fatalf("logs --clear output = %q, want 'Cleared diagnostic log'", stdout)
	}

	if _, err := os.Stat(filepath.Dir(nonexistentLog)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logs --clear created directory: %v", err)
	}
}
