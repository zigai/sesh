package automation_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/automation"
)

func TestAcquireSaveLock_ContentionAndRelease(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime", "sesh", automation.SaveLockName)

	lock, acquired, err := automation.AcquireSaveLock(ctx, path, automation.SaveLockNonBlocking)
	if err != nil || !acquired {
		t.Fatalf("first acquisition = (%v, %t), want a held lock", err, acquired)
	}

	if _, acquired, err := automation.AcquireSaveLock(ctx, path, automation.SaveLockNonBlocking); err != nil || acquired {
		t.Fatalf("second non-blocking acquisition = (%v, %t), want contention without error", err, acquired)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("second Release failed: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file must remain after release: %v", err)
	}

	second, acquired, err := automation.AcquireSaveLock(ctx, path, automation.SaveLockNonBlocking)
	if err != nil || !acquired {
		t.Fatalf("reacquisition = (%v, %t), want a held lock", err, acquired)
	}

	if err := second.Release(); err != nil {
		t.Fatalf("second Release failed: %v", err)
	}
}

func TestAcquireSaveLock_BlockingWaitHonoursCancellation(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime", "sesh", automation.SaveLockName)

	holder, acquired, err := automation.AcquireSaveLock(ctx, path, automation.SaveLockNonBlocking)
	if err != nil || !acquired {
		t.Fatalf("holder acquisition = (%v, %t), want a held lock", err, acquired)
	}

	defer func() {
		if err := holder.Release(); err != nil {
			t.Fatalf("holder Release failed: %v", err)
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	if _, acquired, err := automation.AcquireSaveLock(waitCtx, path, automation.SaveLockBlocking); !errors.Is(err, context.DeadlineExceeded) || acquired {
		t.Fatalf("blocking acquisition = (%v, %t), want a deadline error without the lock", err, acquired)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("holder Release failed: %v", err)
	}

	waited, acquired, err := automation.AcquireSaveLock(ctx, path, automation.SaveLockBlocking)
	if err != nil || !acquired {
		t.Fatalf("blocking acquisition after release = (%v, %t), want a held lock", err, acquired)
	}

	if err := waited.Release(); err != nil {
		t.Fatalf("waited Release failed: %v", err)
	}
}

func TestTryAcquireSaveLock_ReportsBusyAsAnError(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime", "sesh", automation.SaveLockName)

	holder, acquired, err := automation.AcquireSaveLock(ctx, path, automation.SaveLockNonBlocking)
	if err != nil || !acquired {
		t.Fatalf("holder acquisition = (%v, %t), want a held lock", err, acquired)
	}

	if _, err := automation.TryAcquireSaveLock(ctx, path); !errors.Is(err, automation.ErrSaveLockBusy) {
		t.Fatalf("TryAcquireSaveLock error = %v, want ErrSaveLockBusy", err)
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("holder Release failed: %v", err)
	}

	lock, err := automation.TryAcquireSaveLock(ctx, path)
	if err != nil {
		t.Fatalf("TryAcquireSaveLock after release failed: %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
}

func TestSaveLockPath_UsesPrivateRuntimeDirectory(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	want := filepath.Join(runtimeDir, "sesh", automation.SaveLockName)
	if got := automation.SaveLockPath(); got != want {
		t.Fatalf("SaveLockPath() = %q, want %q", got, want)
	}

	t.Setenv("XDG_RUNTIME_DIR", "")

	fallback := automation.SaveLockPath()
	if filepath.Base(fallback) != automation.SaveLockName {
		t.Fatalf("fallback lock path = %q, want the autosave lock name", fallback)
	}
}

const helperSaveLockEnv = "SESH_TEST_SAVE_LOCK_PATH"

func TestSaveLockHelperProcess(t *testing.T) {
	path := os.Getenv(helperSaveLockEnv)
	if path == "" {
		t.Skip("helper process")
	}

	lock, acquired, err := automation.AcquireSaveLock(context.Background(), path, automation.SaveLockBlocking)
	if err != nil || !acquired {
		t.Fatalf("helper acquisition failed: %v (acquired: %t)", err, acquired)
	}

	if _, err := os.Stdout.WriteString("locked\n"); err != nil {
		t.Fatalf("helper could not report readiness: %v", err)
	}

	_, _ = io.Copy(io.Discard, os.Stdin)

	if err := lock.Release(); err != nil {
		t.Fatalf("helper Release failed: %v", err)
	}
}

type lockHelper struct {
	command *exec.Cmd
	stdin   io.WriteCloser
}

func startLockHelper(t *testing.T, path string) *lockHelper {
	t.Helper()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable failed: %v", err)
	}

	command := exec.CommandContext(t.Context(), executable, "-test.run=TestSaveLockHelperProcess$", "-test.v")

	command.Env = append(os.Environ(), helperSaveLockEnv+"="+path)

	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe failed: %v", err)
	}

	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe failed: %v", err)
	}

	if err := command.Start(); err != nil {
		t.Fatalf("helper Start failed: %v", err)
	}

	helper := &lockHelper{command: command, stdin: stdin}

	if !waitForHelperLock(stdout) {
		_ = stdin.Close()
		_ = command.Wait()

		t.Fatal("helper did not report the held lock")
	}

	return helper
}

func waitForHelperLock(stdout io.Reader) bool {
	ready := make(chan struct{})

	go func() {
		scanner := bufio.NewScanner(stdout)

		for scanner.Scan() {
			if scanner.Text() == "locked" {
				close(ready)

				break
			}
		}

		_, _ = io.Copy(io.Discard, stdout)
	}()

	select {
	case <-ready:
		return true
	case <-time.After(30 * time.Second):
		return false
	}
}

func (h *lockHelper) stop(t *testing.T) {
	t.Helper()

	if err := h.stdin.Close(); err != nil {
		t.Fatalf("closing helper stdin failed: %v", err)
	}

	if err := h.command.Wait(); err != nil {
		t.Fatalf("helper exit failed: %v", err)
	}
}

func TestSaveLockContentionAcrossProcesses(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "runtime", "sesh", automation.SaveLockName)

	helper := startLockHelper(t, path)

	if _, err := automation.TryAcquireSaveLock(ctx, path); !errors.Is(err, automation.ErrSaveLockBusy) {
		t.Fatalf("cross-process acquisition error = %v, want ErrSaveLockBusy", err)
	}

	helper.stop(t)

	lock, err := automation.TryAcquireSaveLock(ctx, path)
	if err != nil {
		t.Fatalf("acquisition after helper exit failed: %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
}
