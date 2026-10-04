//go:build (linux || darwin) && integration

package tmux_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/testsupport/testenv"
)

const (
	envHelperFlag   = "SESH_LEASE_HELPER"
	envHelperSocket = "SESH_HELPER_SOCKET"
	envHelperRoot   = "SESH_HELPER_ROOT"
	envHelperMode   = "SESH_HELPER_MODE"
)

func createTestSocket(t *testing.T, dir string, name string) string {
	t.Helper()

	sockPath := filepath.Join(dir, name)

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "unix", sockPath)
	if err != nil {
		t.Fatalf("create test socket: %v", err)
	}

	t.Cleanup(func() {
		_ = listener.Close()
	})

	return sockPath
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv(envHelperFlag) != "1" {
		return
	}

	socket := os.Getenv(envHelperSocket)
	leaseRoot := os.Getenv(envHelperRoot)
	mode := os.Getenv(envHelperMode)

	lease, err := tmux.AcquireLease(context.Background(), leaseRoot, socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper failed to acquire lease: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("ACQUIRED")

	if mode == "hold" {
		buf := make([]byte, 1)
		_, _ = os.Stdin.Read(buf)
	}

	if err := lease.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "helper failed to close lease: %v\n", err)
		os.Exit(1)
	}

	os.Exit(0)
}

type helperProcess struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	waitOnce sync.Once
	waitErr  error
}

func (h *helperProcess) stop() error {
	h.waitOnce.Do(func() {
		if h.stdin != nil {
			_ = h.stdin.Close()
		}

		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}

		if h.cmd != nil {
			h.waitErr = h.cmd.Wait()
		}
	})

	return h.waitErr
}

func (h *helperProcess) releaseAndExit() error {
	h.waitOnce.Do(func() {
		if h.stdin != nil {
			_ = h.stdin.Close()
		}

		if h.cmd != nil {
			h.waitErr = h.cmd.Wait()
		}
	})

	return h.waitErr
}

func (h *helperProcess) killAndJoin() error {
	h.waitOnce.Do(func() {
		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}

		if h.stdin != nil {
			_ = h.stdin.Close()
		}

		if h.cmd != nil {
			h.waitErr = h.cmd.Wait()
		}
	})

	return h.waitErr
}

func spawnHelper(t *testing.T, leaseRoot string, socket string, mode string) *helperProcess {
	t.Helper()

	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, selfExe, "-test.run=^TestHelperProcess$")

	cmd.Env = []string{
		testenv.IsolatedMarker + "=1",
		envHelperFlag + "=1",
		envHelperSocket + "=" + socket,
		envHelperRoot + "=" + leaseRoot,
		envHelperMode + "=" + mode,
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}

	helper := &helperProcess{
		cmd:      cmd,
		stdin:    stdinPipe,
		waitOnce: sync.Once{},
		waitErr:  nil,
	}

	t.Cleanup(func() {
		_ = helper.stop()
	})

	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	readyCh := make(chan error, 1)

	readerDone := make(chan struct{})
	defer func() { <-readerDone }()

	go func() {
		defer close(readerDone)

		reader := bufio.NewReader(stdoutPipe)

		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				readyCh <- readErr

				return
			}

			if line == "ACQUIRED\n" {
				readyCh <- nil

				return
			}
		}
	}()

	readinessTimeout := 5 * time.Second

	select {
	case <-time.After(readinessTimeout):
		_ = helper.stop()

		t.Fatalf("helper timed out waiting for readiness (%v)", readinessTimeout)
	case err := <-readyCh:
		if err != nil {
			_ = helper.stop()

			t.Fatalf("helper failed waiting for readiness: %v", err)
		}
	}

	return helper
}

func checkExpectedKill(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected non-nil error from killed helper")
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}

	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("expected SIGKILL signal, got ws=%+v", exitErr.Sys())
	}
}

func TestLeaseExistingSocketAdmitted(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()

	existingSocket := createTestSocket(t, tempDir, "existing.sock")

	lease, err := tmux.AcquireLease(t.Context(), leaseRoot, existingSocket)
	if err != nil {
		t.Fatalf("acquire lease on genuine existing socket: %v", err)
	}

	if err := lease.Close(); err != nil {
		t.Fatalf("close lease: %v", err)
	}
}

func TestLeaseHardlinkSocketRejected(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()

	sock1 := createTestSocket(t, tempDir, "sock1.sock")
	sock2 := filepath.Join(tempDir, "sock2.sock")

	if err := os.Link(sock1, sock2); err != nil {
		t.Fatalf("os.Link socket: %v", err)
	}

	t.Cleanup(func() {
		_ = os.Remove(sock2)
	})

	leaseA, errA := tmux.AcquireLease(t.Context(), leaseRoot, sock2)
	if leaseA != nil {
		_ = leaseA.Close()

		t.Fatal("expected nil lease for hard-linked socket alias")
	}

	if !errors.Is(errA, tmux.ErrEndpoint) || !errors.Is(errA, tmux.ErrEndpointAlias) {
		t.Fatalf("expected ErrEndpoint and ErrEndpointAlias, got %v", errA)
	}

	leaseB, errB := tmux.AcquireLease(t.Context(), leaseRoot, sock1)
	if leaseB != nil {
		_ = leaseB.Close()

		t.Fatal("expected nil lease for original socket with nlink=2")
	}

	if !errors.Is(errB, tmux.ErrEndpoint) || !errors.Is(errB, tmux.ErrEndpointAlias) {
		t.Fatalf("expected ErrEndpoint and ErrEndpointAlias, got %v", errB)
	}

	if err := os.Remove(sock2); err != nil {
		t.Fatalf("remove hardlink: %v", err)
	}

	leaseC, errC := tmux.AcquireLease(t.Context(), leaseRoot, sock1)
	if errC != nil {
		t.Fatalf("acquire lease on socket after unlinking hardlink alias: %v", errC)
	}

	if err := leaseC.Close(); err != nil {
		t.Fatalf("close leaseC: %v", err)
	}
}

func TestLeaseCrossProcessContention(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()
	sock := createTestSocket(t, tempDir, "contention.sock")

	helper := spawnHelper(t, leaseRoot, sock, "hold")

	timeoutCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	lease, err := tmux.AcquireLease(timeoutCtx, leaseRoot, sock)
	if lease != nil {
		_ = lease.Close()

		t.Fatal("expected failure while helper holds lease")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}

	if err := helper.releaseAndExit(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}

	lease2, err := tmux.AcquireLease(t.Context(), leaseRoot, sock)
	if err != nil {
		t.Fatalf("acquire lease after helper exit: %v", err)
	}

	if err := lease2.Close(); err != nil {
		t.Fatalf("close lease2: %v", err)
	}
}

func getLockInode(t *testing.T, path string) uint64 {
	t.Helper()

	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat lock file %s: %v", path, err)
	}

	sysStat, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("failed to get syscall.Stat_t")
	}

	return sysStat.Ino
}

func TestLeaseSIGKILLHolderReleasesLockWithoutStaleLockDeletion(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	tempDir := t.TempDir()
	sock := createTestSocket(t, tempDir, "sigkill.sock")

	helper1 := spawnHelper(t, leaseRoot, sock, "hold")

	hash := sha256.Sum256([]byte(sock))
	lockPath := filepath.Join(leaseRoot, hex.EncodeToString(hash[:])+".lock")

	inoBefore := getLockInode(t, lockPath)

	killErr := helper1.killAndJoin()
	checkExpectedKill(t, killErr)

	helper2 := spawnHelper(t, leaseRoot, sock, "hold")

	inoAfter := getLockInode(t, lockPath)
	if inoAfter != inoBefore {
		t.Fatalf("inode changed after SIGKILL recovery: %d vs %d (stale lock was recreated)", inoAfter, inoBefore)
	}

	if err := helper2.releaseAndExit(); err != nil {
		t.Fatalf("helper2 exit: %v", err)
	}
}

func TestLeaseWithDisposableTmuxServer(t *testing.T) {
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	server := tmuxtest.NewServer(t)

	socket := server.Endpoint().SocketPath
	if socket == "" {
		t.Fatal("empty socket path from tmuxtest server")
	}

	lease, err := tmux.AcquireLease(t.Context(), leaseRoot, socket)
	if err != nil {
		t.Fatalf("acquire lease on disposable gotmux server endpoint: %v", err)
	}

	sessions, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatalf("query sessions while holding lease: %v", err)
	}

	if len(sessions) == 0 {
		t.Fatal("expected at least 1 session from tmuxtest server")
	}

	if err := lease.Close(); err != nil {
		t.Fatalf("close lease on disposable server: %v", err)
	}
}
