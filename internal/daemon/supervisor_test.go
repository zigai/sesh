package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

var errFakeKilled = errors.New("fake process killed")

type fakeProcess struct {
	mu        sync.Mutex
	started   bool
	signaled  bool
	killed    bool
	wait      chan error
	startErr  error
	signalErr error
	killErr   error
}

func (p *fakeProcess) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.started = true

	return p.startErr
}

func (p *fakeProcess) Wait() error { return <-p.wait }

func (p *fakeProcess) Signal(os.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.signaled = true

	return p.signalErr
}

func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.killed = true

	return p.killErr
}

func TestSupervisorReturnsNaturalExit(t *testing.T) {
	t.Parallel()

	process := &fakeProcess{wait: make(chan error, 1)}
	process.wait <- nil

	supervisor, err := NewSupervisor(func(context.Context) (Process, error) { return process, nil }, time.Second)
	if err != nil {
		t.Fatalf("NewSupervisor failed: %v", err)
	}

	if err := supervisor.Run(t.Context()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
}

func TestSupervisorRejectsSecondRun(t *testing.T) {
	t.Parallel()

	process := &fakeProcess{wait: make(chan error, 1)}
	process.wait <- nil

	var preparations atomic.Int64

	supervisor, err := NewSupervisor(func(context.Context) (Process, error) {
		preparations.Add(1)

		return process, nil
	}, time.Second)
	if err != nil {
		t.Fatalf("NewSupervisor failed: %v", err)
	}

	if err := supervisor.Run(t.Context()); err != nil {
		t.Fatalf("first Run failed: %v", err)
	}

	if err := supervisor.Run(t.Context()); !errors.Is(err, ErrSupervisorUsed) {
		t.Fatalf("second Run error = %v, want ErrSupervisorUsed", err)
	}

	if got := preparations.Load(); got != 1 {
		t.Fatalf("process preparations = %d, want one", got)
	}
}

func TestSupervisorRejectsConcurrentRun(t *testing.T) {
	t.Parallel()

	process := &fakeProcess{wait: make(chan error, 1)}
	started := make(chan struct{})

	supervisor, err := NewSupervisor(func(context.Context) (Process, error) {
		return process, nil
	}, time.Second)
	if err != nil {
		t.Fatalf("NewSupervisor failed: %v", err)
	}

	first := make(chan error, 1)

	go func() {
		close(started)

		first <- supervisor.Run(t.Context())
	}()

	<-started

	for {
		process.mu.Lock()
		processStarted := process.started
		process.mu.Unlock()

		if processStarted {
			break
		}
	}

	if err := supervisor.Run(t.Context()); !errors.Is(err, ErrSupervisorUsed) {
		t.Fatalf("concurrent Run error = %v, want ErrSupervisorUsed", err)
	}

	process.wait <- nil

	if err := <-first; err != nil {
		t.Fatalf("first Run failed: %v", err)
	}
}

func TestSupervisorCancelsSignalsAndJoins(t *testing.T) {
	t.Parallel()

	process := &fakeProcess{wait: make(chan error, 1)}

	supervisor, err := NewSupervisor(func(context.Context) (Process, error) { return process, nil }, time.Second)
	if err != nil {
		t.Fatalf("NewSupervisor failed: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()

	for {
		process.mu.Lock()
		started := process.started
		process.mu.Unlock()

		if started {
			break
		}
	}

	cancel()

	for {
		process.mu.Lock()
		signaled := process.signaled
		process.mu.Unlock()

		if signaled {
			break
		}
	}

	process.wait <- nil

	if err := <-done; err != nil {
		t.Fatalf("Run error = %v, want graceful stop", err)
	}

	process.mu.Lock()
	defer process.mu.Unlock()

	if process.killed {
		t.Fatal("graceful stop unexpectedly killed the process")
	}
}

func TestSupervisorReportsForcedStopIncomplete(t *testing.T) {
	t.Parallel()

	process := &fakeProcess{wait: make(chan error, 1)}

	supervisor, err := NewSupervisor(func(context.Context) (Process, error) { return process, nil }, time.Millisecond)
	if err != nil {
		t.Fatalf("NewSupervisor failed: %v", err)
	}

	var logs bytes.Buffer

	ctx, cancel := context.WithCancel(zerolog.New(&logs).WithContext(t.Context()))

	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()

	for {
		process.mu.Lock()
		started := process.started
		process.mu.Unlock()

		if started {
			break
		}
	}

	cancel()

	for {
		process.mu.Lock()
		killed := process.killed
		process.mu.Unlock()

		if killed {
			break
		}
	}

	process.wait <- errFakeKilled

	if err := <-done; !errors.Is(err, ErrStopIncomplete) {
		t.Fatalf("Run error = %v, want ErrStopIncomplete", err)
	}

	var record map[string]any

	if bytes.Count(logs.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("expected one forced-stop warning: %s", logs.String())
	}

	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"component": "daemon", "operation": "stop", "status": "degraded", "level": "warn",
		"reason": "stop_grace_exceeded", "message": "foreground tmux server requires forced stop",
	}
	if len(record) != len(want) {
		t.Fatalf("unexpected forced-stop schema: %s", logs.String())
	}

	for key, value := range want {
		if record[key] != value {
			t.Fatalf("incorrect forced-stop %s: %s", key, logs.String())
		}
	}
}

func TestTmuxSupervisorRequiresExplicitSocket(t *testing.T) {
	t.Parallel()

	if _, err := NewTmuxSupervisor("tmux", "relative.sock", "/dev/null", time.Second); !errors.Is(err, ErrSocketRequired) {
		t.Fatalf("NewTmuxSupervisor error = %v, want ErrSocketRequired", err)
	}
}

func TestTmuxSupervisorCreatesPrivateSocketParents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	parent := filepath.Join(root, "runtime", "sesh")
	socket := filepath.Join(parent, "tmux.sock")

	binary := createSupervisorExecutable(t, root)

	supervisor, err := NewTmuxSupervisor(binary, socket, "/dev/null", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}

	if err := supervisor.Run(t.Context()); !errors.Is(err, ErrStart) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Run error = %v, want ErrStart and os.ErrNotExist", err)
	}

	for _, path := range []string{filepath.Join(root, "runtime"), parent} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("socket parent mode = %v, want directory with 0700 permissions", info.Mode())
		}
	}
}

func TestTmuxSupervisorPreservesExistingSocketParent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	parent := filepath.Join(root, "runtime")

	if err := os.Mkdir(parent, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(parent, 0o750); err != nil {
		t.Fatal(err)
	}

	state := filepath.Join(parent, "state")
	if err := os.WriteFile(state, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := createSupervisorExecutable(t, root)

	supervisor, err := NewTmuxSupervisor(binary, filepath.Join(parent, "tmux.sock"), "/dev/null", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}

	if err := supervisor.Run(t.Context()); !errors.Is(err, ErrStart) {
		t.Fatalf("Run error = %v, want ErrStart", err)
	}

	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o750 {
		t.Fatalf("existing parent permissions = %v, want 0750", info.Mode().Perm())
	}

	data, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "retained" {
		t.Fatalf("existing state = %q, want retained", data)
	}
}

func TestTmuxSupervisorPropagatesSocketParentFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	parent := filepath.Join(root, "blocked")

	if err := os.WriteFile(parent, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}

	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	supervisor, err := NewTmuxSupervisor(binary, filepath.Join(parent, "sesh", "tmux.sock"), "/dev/null", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	err = supervisor.Run(t.Context())
	if !errors.Is(err, ErrPrepare) || !errors.Is(err, syscall.ENOTDIR) || errors.Is(err, ErrStart) {
		t.Fatalf("Run error = %v, want ErrPrepare and ENOTDIR without ErrStart", err)
	}

	data, err := os.ReadFile(parent)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "retained" {
		t.Fatalf("blocking file = %q, want retained", data)
	}
}

func createSupervisorExecutable(t *testing.T, root string) string {
	t.Helper()

	binary := filepath.Join(root, "tmux")
	if err := os.WriteFile(binary, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(binary, 0o700); err != nil {
		t.Fatal(err)
	}

	return binary
}
