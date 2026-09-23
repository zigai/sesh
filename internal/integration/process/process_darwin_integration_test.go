//go:build integration && darwin

package process_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/zigai/sesh/internal/integration/process"
)

func TestDarwinOwnedChildIdentityIsStable(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start owned child: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	first, err := process.ReadIdentity(t.Context(), cmd.Process.Pid)
	if err != nil {
		t.Fatalf("first ReadIdentity: %v", err)
	}

	second, err := process.ReadIdentity(t.Context(), cmd.Process.Pid)
	if err != nil {
		t.Fatalf("second ReadIdentity: %v", err)
	}

	if first.IsZero() || first != second {
		t.Fatalf("owned child identity unstable: first=%v second=%v", first, second)
	}

	req := process.Request{PID: cmd.Process.Pid, ExpectedBirth: first}
	if _, err := process.Observe(t.Context(), req); !errors.Is(err, process.ErrUnsupported) {
		t.Fatalf("Observe error = %v, want %v", err, process.ErrUnsupported)
	}
}

func TestDarwinReadIdentityHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := process.ReadIdentity(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadIdentity error = %v, want context cancellation", err)
	}
}
