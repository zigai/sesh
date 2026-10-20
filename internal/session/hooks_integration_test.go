//go:build linux && integration

package session_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

const hookTestShell = "/bin/sh"

type hookReviewWriter func([]byte) (int, error)

func (w hookReviewWriter) Write(p []byte) (int, error) { return w(p) }

func hookSteppedClock(step time.Duration) func() time.Time {
	var (
		mu      sync.Mutex
		current = time.Unix(1000, 0)
	)

	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()

		now := current
		current = current.Add(step)

		return now
	}
}

func hookDecodeRecords(t *testing.T, logText string) []map[string]any {
	t.Helper()

	var records []map[string]any

	for line := range strings.SplitSeq(strings.TrimSpace(logText), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("failed to parse log line %q: %v", line, err)
		}

		for key := range record {
			switch key {
			case "level", "component", "operation", "hook_event", "hook_index", "status",
				"reason", "timeout_ms", "duration_ms", "exit_code", "message",
				"error", "error_domain", "error_code", "error_step":
			default:
				t.Fatalf("unexpected hook log field: %s", key)
			}
		}

		records = append(records, record)
	}

	return records
}

func hookAssertRecord(t *testing.T, got, want map[string]any) {
	t.Helper()

	for k, wantVal := range want {
		gotVal, exists := got[k]
		if !exists {
			t.Errorf("record missing key %q; record: %#v", k, got)
			continue
		}

		if gotVal != wantVal {
			t.Errorf("record[%q] = %#v (%T), want %#v (%T)", k, gotVal, gotVal, wantVal, wantVal)
		}
	}
}

type hookProcessIdentity struct {
	pid   int
	birth string
	state string
}

func hookParseProcessIdentity(t *testing.T, data []byte) hookProcessIdentity {
	t.Helper()

	text := string(data)

	nameStart, nameEnd := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if nameStart < 1 || nameEnd <= nameStart {
		t.Fatal("invalid helper process report")
	}

	pid, err := strconv.Atoi(strings.TrimSpace(text[:nameStart]))
	if err != nil || pid < 1 {
		t.Fatalf("invalid helper PID: %v", err)
	}

	fields := strings.Fields(text[nameEnd+1:])
	if len(fields) < 20 {
		t.Fatal("incomplete helper process report")
	}

	return hookProcessIdentity{pid: pid, birth: fields[19], state: fields[0]}
}

func hookReadChildIdentity(t *testing.T, report string) hookProcessIdentity {
	t.Helper()

	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}

	return hookParseProcessIdentity(t, data)
}

func hookAssertProcessTerminated(t *testing.T, child hookProcessIdentity) {
	t.Helper()

	for range 50 {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", child.pid))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			return
		}

		if err != nil {
			t.Fatal(err)
		}

		current := hookParseProcessIdentity(t, data)
		if current.birth != child.birth || current.state == "Z" || current.state == "X" {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("owned descendant still running after process group cleanup")
}

func TestHookIntegrationShellEnvironment(t *testing.T) {
	t.Setenv("SHELL", hookTestShell)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ENV", "")
	t.Setenv("BASH_ENV", "")
	outputPath := filepath.Join(t.TempDir(), "hook.out")
	cfg := config.HookConfig{
		SessionSwitchPost: []config.HookCommand{{
			ShellCommand: fmt.Sprintf(`printf '%%s:%%s:%%s' "$SESH_HOOK_EVENT" "$SESH_PREV_SESSION" "$SESH_SESSION" > %q`, outputPath),
		}},
	}

	event := session.HookEvent{
		Name:          session.EventSessionSwitchPost,
		Action:        "switch",
		Phase:         "post",
		PrevSession:   "old_session",
		Session:       "new_session",
		PrevSessionID: "$0",
		SessionID:     "$1",
		Source:        "cli",
	}

	var logOutput bytes.Buffer

	ctx := hookLogContext(t.Context(), &logOutput)
	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    hookSteppedClock(1250 * time.Millisecond),
		Getenv: nil,
	})

	res, err := runner.Run(ctx, cfg, event)
	if err != nil {
		t.Fatalf("Run() returned unexpected error: %v", err)
	}

	if res.Attempted != 1 || res.Completed != 1 || res.Failed != 0 || res.TimedOut != 0 {
		t.Fatalf("res counts = %#v, want 1 attempted, 1 completed", res)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading hook output: %v", err)
	}

	if got := string(data); got != "session_switch_post:old_session:new_session" {
		t.Fatalf("hook output = %q, want session_switch_post:old_session:new_session", got)
	}

	records := hookDecodeRecords(t, logOutput.String())
	if len(records) != 2 {
		t.Fatalf("hook record count = %d, want 2", len(records))
	}

	hookAssertRecord(t, records[0], map[string]any{
		"level":      "info",
		"component":  "hooks",
		"operation":  "execute",
		"hook_event": session.EventSessionSwitchPost,
		"hook_index": float64(0),
		"status":     "started",
		"timeout_ms": float64(30000),
		"message":    "hook started",
	})

	hookAssertRecord(t, records[1], map[string]any{
		"level":       "info",
		"component":   "hooks",
		"operation":   "execute",
		"hook_event":  session.EventSessionSwitchPost,
		"hook_index":  float64(0),
		"status":      "ok",
		"duration_ms": float64(1250),
		"message":     "hook completed",
	})
}

func TestHookIntegrationArgvLiteralTokens(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "argv.out")
	cfg := config.HookConfig{
		SessionAttachPost: []config.HookCommand{{
			Command: hookTestShell,
			Args: []string{
				"-c",
				`printf '%s|%s|%s|%s|%s' "$1" "$2" "$3" "$4" "$5" > "$6"`,
				"sh",
				"",
				"arg with spaces",
				`quote"and'tick`,
				"$DOLLAR_EXPR",
				"hello\nworld",
				outputPath,
			},
		}},
	}

	event := session.HookEvent{
		Name:          session.EventSessionAttachPost,
		Action:        "attach",
		Phase:         "post",
		PrevSession:   "",
		Session:       "test_session",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "cli",
	}

	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    nil,
		Getenv: nil,
	})

	res, err := runner.Run(t.Context(), cfg, event)
	if err != nil {
		t.Fatalf("Run() returned unexpected error: %v", err)
	}

	if res.Attempted != 1 || res.Completed != 1 {
		t.Fatalf("res = %#v, want 1 attempted, 1 completed", res)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading argv output: %v", err)
	}

	want := "|arg with spaces|quote\"and'tick|$DOLLAR_EXPR|hello\nworld"
	if string(data) != want {
		t.Fatalf("argv output = %q, want %q", string(data), want)
	}
}

func TestHookIntegrationOrderingAndOutcomeCounts(t *testing.T) {
	cfg := config.HookConfig{
		SessionSwitchPost: []config.HookCommand{
			{Command: "/bin/true"},
			{Command: "/bin/true"},
			{Command: "/bin/true"},
		},
	}

	var logOutput bytes.Buffer

	ctx := hookLogContext(t.Context(), &logOutput)
	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    hookSteppedClock(100 * time.Millisecond),
		Getenv: nil,
	})

	res, err := runner.Run(ctx, cfg, session.HookEvent{
		Name:          session.EventSessionSwitchPost,
		Action:        "",
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	})
	if err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}

	verifyOrderingOutcomes(t, res, logOutput.String())
}

func verifyOrderingOutcomes(t *testing.T, res session.HookResult, logOutput string) {
	t.Helper()

	if res.Attempted != 3 || res.Completed != 3 || res.Failed != 0 || res.TimedOut != 0 {
		t.Fatalf("counts = attempted %d, completed %d, failed %d, timedOut %d; want 3, 3, 0, 0",
			res.Attempted, res.Completed, res.Failed, res.TimedOut)
	}

	if len(res.Outcomes) != 3 || res.Err() != nil || len(res.Causes()) != 0 {
		t.Fatalf("outcomes error state mismatch: %#v", res)
	}

	verifyHookOutcomesList(t, res.Outcomes)

	records := hookDecodeRecords(t, logOutput)
	if len(records) != 6 {
		t.Fatalf("record count = %d, want 6 (3 started + 3 completed)", len(records))
	}
}

func verifyHookOutcomesList(t *testing.T, outcomes []session.HookOutcome) {
	t.Helper()

	for i, o := range outcomes {
		if o.Index != i || o.Status != session.HookStatusCompleted || o.ExitCode != 0 || o.Err != nil {
			t.Fatalf("outcome[%d] mismatch: %#v", i, o)
		}
	}
}

func TestHookIntegrationMixedCauseIdentity(t *testing.T) {
	cfg := config.HookConfig{
		SessionSwitchPre: []config.HookCommand{
			{Command: hookTestShell, Args: []string{"-c", "exit 7"}, Timeout: "1s"},
			{Command: hookTestShell, Args: []string{"-c", "sleep 2"}, Timeout: "10ms"},
		},
	}

	var logOutput bytes.Buffer

	ctx := hookLogContext(t.Context(), &logOutput)
	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    hookSteppedClock(50 * time.Millisecond),
		Getenv: nil,
	})

	res, err := runner.Run(ctx, cfg, session.HookEvent{
		Name:          session.EventSessionSwitchPre,
		Action:        "",
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	})
	if err != nil {
		t.Fatalf("Run() err = %v, want nil (optional failures do not veto)", err)
	}

	if res.Attempted != 2 || res.Completed != 0 || res.Failed != 1 || res.TimedOut != 1 {
		t.Fatalf("counts = attempted %d, completed %d, failed %d, timedOut %d; want 2, 0, 1, 1",
			res.Attempted, res.Completed, res.Failed, res.TimedOut)
	}

	joinedErr := res.Err()
	if joinedErr == nil {
		t.Fatalf("res.Err() = nil, want joined failure error")
	}

	if !errors.Is(joinedErr, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(joinedErr, context.DeadlineExceeded) = false; want true")
	}

	var exitErr *exec.ExitError
	if !errors.As(joinedErr, &exitErr) {
		t.Fatalf("errors.As(joinedErr, &exitErr) = false; want true")
	}

	if exitErr.ExitCode() != 7 {
		t.Fatalf("exitErr.ExitCode() = %d, want 7", exitErr.ExitCode())
	}

	causes := res.Causes()
	if len(causes) != 2 {
		t.Fatalf("res.Causes() len = %d, want 2", len(causes))
	}
}

func TestHookIntegrationTimeoutAndContinues(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "continued.out")
	cfg := config.HookConfig{
		SessionSwitchPre: []config.HookCommand{
			{
				Command: hookTestShell,
				Args:    []string{"-c", "sleep 2"},
				Timeout: "25ms",
			},
			{
				Command: hookTestShell,
				Args:    []string{"-c", fmt.Sprintf("printf continued > %q", outputPath)},
				Timeout: "2s",
			},
		},
	}

	var logOutput bytes.Buffer

	ctx := hookLogContext(t.Context(), &logOutput)
	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    hookSteppedClock(10 * time.Millisecond),
		Getenv: nil,
	})

	event := session.HookEvent{
		Name:          session.EventSessionSwitchPre,
		Action:        "",
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	}

	res, err := runner.Run(ctx, cfg, event)
	if err != nil {
		t.Fatalf("Run() returned error: %v (optional failure must not veto)", err)
	}

	if res.Attempted != 2 || res.Completed != 1 || res.TimedOut != 1 || res.Failed != 0 {
		t.Fatalf("res counts = %#v, want 2 attempted, 1 completed, 1 timed out", res)
	}

	if !errors.Is(res.Err(), context.DeadlineExceeded) {
		t.Fatalf("res.Err() = %v, want context.DeadlineExceeded", res.Err())
	}

	data, err := os.ReadFile(outputPath)
	if err != nil || string(data) != "continued" {
		t.Fatalf("continued hook output = %q, err = %v", string(data), err)
	}

	records := hookDecodeRecords(t, logOutput.String())
	if len(records) != 4 {
		t.Fatalf("hook record count = %d, want 4", len(records))
	}

	hookAssertRecord(t, records[1], map[string]any{
		"level":      "warn",
		"component":  "hooks",
		"operation":  "execute",
		"hook_event": session.EventSessionSwitchPre,
		"hook_index": float64(0),
		"status":     "failed",
		"reason":     "deadline_exceeded",
		"timeout_ms": float64(25),
		"message":    "hook timed out",
	})
}

func TestHookIntegrationFailureContinuesAndLogsWarning(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "continued.out")
	cfg := config.HookConfig{
		SessionSwitchPost: []config.HookCommand{
			{
				Command: hookTestShell,
				Args:    []string{"-c", "exit 42"},
				Timeout: "2s",
			},
			{
				Command: hookTestShell,
				Args:    []string{"-c", fmt.Sprintf("printf ok > %q", outputPath)},
				Timeout: "2s",
			},
		},
	}

	var logOutput bytes.Buffer

	ctx := hookLogContext(t.Context(), &logOutput)
	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    hookSteppedClock(15 * time.Millisecond),
		Getenv: nil,
	})

	event := session.HookEvent{
		Name:          session.EventSessionSwitchPost,
		Action:        "",
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	}

	res, err := runner.Run(ctx, cfg, event)
	if err != nil {
		t.Fatalf("Run() returned error: %v (failure must not veto)", err)
	}

	if res.Attempted != 2 || res.Completed != 1 || res.Failed != 1 {
		t.Fatalf("res counts = %#v, want 2 attempted, 1 completed, 1 failed", res)
	}

	var exitErr *exec.ExitError
	if !errors.As(res.Err(), &exitErr) || exitErr.ExitCode() != 42 {
		t.Fatalf("expected ExitCode 42 in causes, got %v", res.Err())
	}

	data, err := os.ReadFile(outputPath)
	if err != nil || string(data) != "ok" {
		t.Fatalf("continued hook output = %q, err = %v", string(data), err)
	}

	records := hookDecodeRecords(t, logOutput.String())
	if len(records) != 4 {
		t.Fatalf("hook record count = %d, want 4", len(records))
	}

	hookAssertRecord(t, records[1], map[string]any{
		"level":      "warn",
		"component":  "hooks",
		"operation":  "execute",
		"hook_event": session.EventSessionSwitchPost,
		"hook_index": float64(0),
		"status":     "failed",
		"reason":     "execution_failed",
		"exit_code":  float64(42),
		"message":    "hook failed",
	})
}

func TestHookIntegrationProcessGroupCleanupDescendants(t *testing.T) {
	tempDir := t.TempDir()
	childPIDFile := filepath.Join(tempDir, "child.pid")

	script := fmt.Sprintf(`sleep 5 & child=$!; cat /proc/$child/stat > %q; wait`, childPIDFile)
	cfg := config.HookConfig{
		SessionSwitchPre: []config.HookCommand{{
			Command: hookTestShell,
			Args:    []string{"-c", script},
			Timeout: "50ms",
		}},
	}

	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    nil,
		Getenv: nil,
	})

	res, err := runner.Run(t.Context(), cfg, session.HookEvent{
		Name:          session.EventSessionSwitchPre,
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if res.TimedOut != 1 {
		t.Fatalf("res.TimedOut = %d, want 1", res.TimedOut)
	}

	childPID := hookReadChildIdentity(t, childPIDFile)
	hookAssertProcessTerminated(t, childPID)
}

func TestHookIntegrationStdoutStderrIsolation(t *testing.T) {
	t.Setenv("SHELL", hookTestShell)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ENV", "")
	t.Setenv("BASH_ENV", "")

	cfg := config.HookConfig{
		SessionSwitchPost: []config.HookCommand{{
			ShellCommand: "printf 'SUBPROCESS_LEAKED_STDOUT'; printf 'SUBPROCESS_LEAKED_STDERR' >&2",
		}},
	}

	oldStdout := os.Stdout
	oldStderr := os.Stderr

	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() err = %v", err)
	}

	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() err = %v", err)
	}

	os.Stdout = wOut
	os.Stderr = wErr

	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    nil,
		Getenv: nil,
	})

	_, runErr := runner.Run(t.Context(), cfg, session.HookEvent{
		Name:          session.EventSessionSwitchPost,
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	})

	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	var (
		stdoutBuf bytes.Buffer
		stderrBuf bytes.Buffer
	)

	_, _ = io.Copy(&stdoutBuf, rOut)
	_, _ = io.Copy(&stderrBuf, rErr)
	_ = rOut.Close()
	_ = rErr.Close()

	if runErr != nil {
		t.Fatalf("Run() err = %v", runErr)
	}

	if stdoutBuf.Len() != 0 {
		t.Fatalf("captured stdout contains %d bytes: %q (want 0)", stdoutBuf.Len(), stdoutBuf.String())
	}

	if stderrBuf.Len() != 0 {
		t.Fatalf("captured stderr contains %d bytes: %q (want 0)", stderrBuf.Len(), stderrBuf.String())
	}
}

func TestHookIntegrationParentCancellationAbortsNoAdditionalLaunch(t *testing.T) {
	tempDir := t.TempDir()
	touchFile1 := filepath.Join(tempDir, "hook1.touched")
	touchFile2 := filepath.Join(tempDir, "hook2.touched")

	cfg := config.HookConfig{
		SessionAttachPre: []config.HookCommand{
			{
				Command: hookTestShell,
				Args:    []string{"-c", "sleep 2"},
				Timeout: "5s",
			},
			{
				Command: hookTestShell,
				Args:    []string{"-c", fmt.Sprintf("touch %q", touchFile1)},
				Timeout: "1s",
			},
			{
				Command: hookTestShell,
				Args:    []string{"-c", fmt.Sprintf("touch %q", touchFile2)},
				Timeout: "1s",
			},
		},
	}

	var logOutput bytes.Buffer

	baseCtx := hookLogContext(t.Context(), &logOutput)

	ctx, cancel := context.WithCancel(baseCtx)
	defer cancel()

	var cancelWG sync.WaitGroup

	cancelWG.Add(1)

	defer cancelWG.Wait()
	go func() {
		defer cancelWG.Done()

		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    nil,
		Getenv: nil,
	})
	res, err := runner.Run(ctx, cfg, session.HookEvent{
		Name:          session.EventSessionAttachPre,
		Action:        "",
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() err = %v, want context.Canceled", err)
	}

	if res.Attempted != 1 {
		t.Fatalf("res.Attempted = %d, want 1", res.Attempted)
	}

	if _, statErr := os.Stat(touchFile1); !os.IsNotExist(statErr) {
		t.Fatalf("hook 1 ran despite parent cancellation! (statErr: %v)", statErr)
	}

	if _, statErr := os.Stat(touchFile2); !os.IsNotExist(statErr) {
		t.Fatalf("hook 2 ran despite parent cancellation! (statErr: %v)", statErr)
	}

	records := hookDecodeRecords(t, logOutput.String())
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}

	hookAssertRecord(t, records[1], map[string]any{
		"level":      "error",
		"component":  "hooks",
		"operation":  "execute",
		"hook_event": session.EventSessionAttachPre,
		"hook_index": float64(0),
		"status":     "failed",
		"reason":     "canceled",
		"message":    "hook canceled",
	})
}

func TestHookIntegrationCancellationBetweenHooks(t *testing.T) {
	tempDir := t.TempDir()
	touchFile := filepath.Join(tempDir, "hook2.touched")

	cfg := config.HookConfig{
		SessionAttachPre: []config.HookCommand{
			{Command: "/bin/true"},
			{Command: hookTestShell, Args: []string{"-c", fmt.Sprintf("touch %q", touchFile)}},
		},
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var logOutput bytes.Buffer

	logger := zerolog.New(hookReviewWriter(func(p []byte) (int, error) {
		n, err := logOutput.Write(p)
		if bytes.Contains(p, []byte(`"status":"ok"`)) {
			cancel()
		}

		if err != nil {
			return n, fmt.Errorf("write hook test record: %w", err)
		}

		return n, nil
	}))
	ctx = logger.WithContext(ctx)

	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    nil,
		Getenv: nil,
	})
	res, err := runner.Run(ctx, cfg, session.HookEvent{
		Name:          session.EventSessionAttachPre,
		Action:        "",
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() err = %v, want context.Canceled", err)
	}

	if res.Attempted != 1 || res.Completed != 1 {
		t.Fatalf("res.Attempted = %d, completed = %d; want 1, 1", res.Attempted, res.Completed)
	}

	if _, statErr := os.Stat(touchFile); !os.IsNotExist(statErr) {
		t.Fatalf("hook 2 ran despite cancellation between hooks")
	}

	records := hookDecodeRecords(t, logOutput.String())
	if len(records) != 3 {
		t.Fatalf("sequence cancellation record count = %d, want 3", len(records))
	}

	hookAssertRecord(t, records[2], map[string]any{
		"level": "error", "component": "hooks", "operation": "execute",
		"hook_event": session.EventSessionAttachPre, "hook_index": float64(1),
		"status": "failed", "reason": "canceled", "message": "hook sequence canceled",
	})
}

func TestHookIntegrationCancellationLastHook(t *testing.T) {
	cfg := config.HookConfig{
		SessionAttachPost: []config.HookCommand{
			{Command: "/bin/true"},
			{Command: hookTestShell, Args: []string{"-c", "sleep 2"}, Timeout: "5s"},
		},
	}

	var logOutput bytes.Buffer

	baseCtx := hookLogContext(t.Context(), &logOutput)

	ctx, cancel := context.WithCancel(baseCtx)
	defer cancel()

	var cancelWG sync.WaitGroup

	cancelWG.Add(1)

	defer cancelWG.Wait()
	go func() {
		defer cancelWG.Done()

		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    nil,
		Getenv: nil,
	})
	res, err := runner.Run(ctx, cfg, session.HookEvent{
		Name:          session.EventSessionAttachPost,
		Action:        "",
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() err = %v, want context.Canceled", err)
	}

	if res.Attempted != 2 || res.Completed != 1 || res.Failed != 1 {
		t.Fatalf("res = %#v, want attempted=2, completed=1, failed=1", res)
	}

	records := hookDecodeRecords(t, logOutput.String())
	if len(records) != 4 {
		t.Fatalf("record count = %d, want 4", len(records))
	}

	hookAssertRecord(t, records[3], map[string]any{
		"level":      "error",
		"component":  "hooks",
		"operation":  "execute",
		"hook_event": session.EventSessionAttachPost,
		"hook_index": float64(1),
		"status":     "failed",
		"reason":     "canceled",
		"message":    "hook canceled",
	})
}

func TestHookIntegrationPrivacyNoRawPayloadsInLogs(t *testing.T) {
	const (
		mockArg     = "SECRET_ARG_TOKEN"
		mockEnv     = "SECRET_ENV_TOKEN"
		mockSession = "SECRET_SESSION_TOKEN"
	)

	t.Setenv("SESH_TEST_FIXTURE", mockEnv)

	cfg := config.HookConfig{
		SessionSwitchPost: []config.HookCommand{
			{Command: hookTestShell, Args: []string{"-c", "echo " + mockArg}, Timeout: "2s"},
		},
	}
	event := session.HookEvent{
		Name:          session.EventSessionSwitchPost,
		Action:        "switch",
		Phase:         "post",
		PrevSession:   mockSession,
		Session:       mockSession,
		PrevSessionID: mockSession,
		SessionID:     mockSession,
		Source:        mockSession,
	}

	var logOutput bytes.Buffer

	ctx := hookLogContext(t.Context(), &logOutput)
	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    hookSteppedClock(25 * time.Millisecond),
		Getenv: nil,
	})

	res, err := runner.Run(ctx, cfg, event)
	if err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}

	if res.Completed != 1 {
		t.Fatalf("res.Completed = %d, want 1", res.Completed)
	}

	logText := logOutput.String()
	for _, token := range []string{mockArg, mockEnv, mockSession} {
		if strings.Contains(logText, token) {
			t.Fatalf("hook records contain raw secret payload %q:\n%s", token, logText)
		}
	}
}

func TestHookIntegrationBuildCmdFailure(t *testing.T) {
	cfg := config.HookConfig{
		SessionSwitchPost: []config.HookCommand{
			{Command: "", ShellCommand: ""},
		},
	}

	var logOutput bytes.Buffer

	ctx := hookLogContext(t.Context(), &logOutput)

	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    nil,
		Getenv: nil,
	})

	res, err := runner.Run(ctx, cfg, session.HookEvent{
		Name:          session.EventSessionSwitchPost,
		Action:        "",
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	})
	if !errors.Is(err, session.ErrHookCommandEmpty) {
		t.Fatalf("expected ErrHookCommandEmpty, got %v", err)
	}

	if res.Attempted != 0 {
		t.Fatalf("expected 0 attempted on config validation error, got %d", res.Attempted)
	}
}

func TestHookIntegrationLeaderExitsWithBackgroundChild(t *testing.T) {
	tempDir := t.TempDir()
	childPIDFile := filepath.Join(tempDir, "bg_child.pid")

	script := fmt.Sprintf(`sleep 5 & child=$!; cat /proc/$child/stat > %q; exit 0`, childPIDFile)
	cfg := config.HookConfig{
		SessionSwitchPost: []config.HookCommand{{
			Command: hookTestShell,
			Args:    []string{"-c", script},
			Timeout: "5s",
		}},
	}

	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    nil,
		Getenv: nil,
	})

	res, err := runner.Run(t.Context(), cfg, session.HookEvent{
		Name:          session.EventSessionSwitchPost,
		Action:        "",
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	})
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if res.Completed != 1 {
		t.Fatalf("res.Completed = %d, want 1", res.Completed)
	}

	childPID := hookReadChildIdentity(t, childPIDFile)
	hookAssertProcessTerminated(t, childPID)
}

func TestHookIntegrationCancelReapInterleavings(t *testing.T) {
	cfg := config.HookConfig{
		SessionSwitchPre: []config.HookCommand{{
			Command: hookTestShell,
			Args:    []string{"-c", "sleep 10"},
			Timeout: "5s",
		}},
	}

	ctx, cancel := context.WithCancel(t.Context())

	var cancelWg sync.WaitGroup

	cancelWg.Go(func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	})

	runner := session.NewHookRunner(session.HookRunnerDeps{
		Now:    nil,
		Getenv: nil,
	})

	res, err := runner.Run(ctx, cfg, session.HookEvent{
		Name:          session.EventSessionSwitchPre,
		Action:        "",
		Phase:         "",
		PrevSession:   "",
		Session:       "",
		PrevSessionID: "",
		SessionID:     "",
		Source:        "",
	})

	cancelWg.Wait()

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() err = %v, want context.Canceled", err)
	}

	if res.Attempted != 1 || res.Failed != 1 {
		t.Fatalf("res = %#v, want attempted 1, failed 1", res)
	}

	if len(res.Outcomes) != 1 || res.Outcomes[0].Status != session.HookStatusCanceled {
		t.Fatalf("outcome = %#v, want canceled", res.Outcomes)
	}
}
