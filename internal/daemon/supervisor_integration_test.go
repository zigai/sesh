//go:build integration

package daemon_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/daemon"
)

func TestTmuxSupervisorOwnsDisposableForegroundServer(t *testing.T) {
	if os.Getenv("TMUX_INTEGRATION_REQUIRED") != "1" {
		t.Skip("set TMUX_INTEGRATION_REQUIRED=1 to run disposable tmux integration tests")
	}

	parent := filepath.Join(t.TempDir(), "runtime", "sesh")
	socket := filepath.Join(parent, "supervised.sock")

	supervisor, err := daemon.NewTmuxSupervisor("tmux", socket, "/dev/null", 2*time.Second)
	if err != nil {
		t.Fatalf("NewTmuxSupervisor failed: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())

	var runErr error

	done := make(chan struct{})

	go func() {
		runErr = supervisor.Run(ctx)

		close(done)
	}()

	t.Cleanup(func() {
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("supervisor did not stop during cleanup")
		}
	})

	server, err := gotmux.New(gotmux.Config{SocketPath: socket, ConfigFile: "/dev/null"})
	if err != nil {
		t.Fatalf("gotmux.New failed: %v", err)
	}

	readyCtx, readyCancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer readyCancel()

	if err := daemon.WaitReady(readyCtx, socket); err != nil {
		t.Fatalf("WaitReady failed: %v", err)
	}

	state := checkSupervisorSocketAndWriteState(t, parent, socket)

	if _, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    "supervisor-fixture",
		Program: gotmux.Exec("sleep", "30"),
		Start:   gotmux.StartPolicyExistingOnly,
	}); err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not join after cancellation")
	}

	if runErr != nil {
		t.Fatalf("Run shutdown failed: %v", runErr)
	}

	if _, err := server.Probe(t.Context()); !errors.Is(err, gotmux.ErrNoServer) {
		t.Fatalf("Probe after join = %v, want ErrNoServer", err)
	}

	checkSupervisorStateRetained(t, state)
}

func checkSupervisorSocketAndWriteState(t *testing.T, parent, socket string) string {
	t.Helper()

	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}

	if parentInfo.Mode().Perm() != 0o700 {
		t.Fatalf("socket parent permissions = %v, want 0700", parentInfo.Mode().Perm())
	}

	socketInfo, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}

	if socketInfo.Mode()&os.ModeSocket == 0 || socketInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("socket mode = %v, want private Unix socket", socketInfo.Mode())
	}

	state := filepath.Join(parent, "state")
	if err := os.WriteFile(state, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}

	return state
}

func checkSupervisorStateRetained(t *testing.T, state string) {
	t.Helper()

	data, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "retained" {
		t.Fatalf("runtime state after stop = %q, want retained", data)
	}
}
