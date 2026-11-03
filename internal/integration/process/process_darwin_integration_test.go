//go:build integration && darwin

package process_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/testsupport/testenv"
)

const (
	darwinHelperFlag  = "GO_PROCESS_DARWIN_HELPER"
	darwinObservedVar = "OBSERVED_TEST_VAR"
	darwinAbsentVar   = "ABSENT_TEST_VAR"
)

func TestDarwinHelperProcess(t *testing.T) {
	if os.Getenv(darwinHelperFlag) != "1" {
		return
	}

	var buf [1]byte

	_, _ = os.Stdin.Read(buf[:])

	os.Exit(0)
}

func TestDarwinObserveReportsOwnedChildLaunch(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}

	cmd := exec.CommandContext(t.Context(), "/bin/sleep", "30")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	startOwnedChild(t, cmd)

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

	observation, err := process.Observe(t.Context(), process.Request{PID: cmd.Process.Pid, ExpectedBirth: first})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if observation.PID != cmd.Process.Pid || observation.Birth != first {
		t.Fatalf("observed pid/birth = %d/%v, want %d/%v", observation.PID, observation.Birth, cmd.Process.Pid, first)
	}

	if string(observation.Executable) != "/bin/sleep" || observation.ExecutableDeleted {
		t.Fatalf("executable = %q deleted=%v, want /bin/sleep", observation.Executable, observation.ExecutableDeleted)
	}

	wantArgs := [][]byte{[]byte("/bin/sleep"), []byte("30")}
	if len(observation.Arguments) != len(wantArgs) {
		t.Fatalf("arguments = %q, want %q", observation.Arguments, wantArgs)
	}

	for i, arg := range observation.Arguments {
		if !bytes.Equal(arg, wantArgs[i]) {
			t.Fatalf("arguments = %q, want %q", observation.Arguments, wantArgs)
		}
	}

	if string(observation.Directory) != dir || observation.DirectoryDeleted {
		t.Fatalf("directory = %q deleted=%v, want %q", observation.Directory, observation.DirectoryDeleted, dir)
	}
}

func TestDarwinObserveReportsRequestedEnvironment(t *testing.T) {
	selfExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	cmd := exec.CommandContext(t.Context(), selfExe, "-test.run=^TestDarwinHelperProcess$")
	cmd.Env = []string{
		testenv.IsolatedMarker + "=1",
		darwinHelperFlag + "=1",
		"PATH=/usr/bin:/bin",
		darwinObservedVar + "=observed value",
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}

	startOwnedChild(t, cmd)
	t.Cleanup(func() { _ = stdin.Close() })

	birth, err := process.ReadIdentity(t.Context(), cmd.Process.Pid)
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}

	observation, err := process.Observe(t.Context(), process.Request{
		PID:             cmd.Process.Pid,
		ExpectedBirth:   birth,
		EnvironmentKeys: []string{darwinObservedVar, darwinAbsentVar, darwinObservedVar},
	})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	if len(observation.Environment) != 2 {
		t.Fatalf("environment = %+v, want the two distinct requested keys", observation.Environment)
	}

	observed, absent := observation.Environment[0], observation.Environment[1]
	if string(observed.Name) != darwinObservedVar || observed.State != "set" || string(observed.Value) != "observed value" {
		t.Fatalf("observed variable = %+v, want %s set to %q", observed, darwinObservedVar, "observed value")
	}

	if string(absent.Name) != darwinAbsentVar || absent.State != "absent" || absent.Value != nil {
		t.Fatalf("absent variable = %+v, want %s absent", absent, darwinAbsentVar)
	}
}

func TestDarwinObserveReportsExitedChildAsVanished(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start owned child: %v", err)
	}

	birth, err := process.ReadIdentity(t.Context(), cmd.Process.Pid)
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	_, err = process.Observe(t.Context(), process.Request{PID: cmd.Process.Pid, ExpectedBirth: birth})
	if !errors.Is(err, process.ErrProcessVanished) && !errors.Is(err, process.ErrIdentityMismatch) {
		t.Fatalf("Observe error = %v, want vanished or identity mismatch", err)
	}
}

func TestDarwinReadIdentityHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := process.ReadIdentity(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadIdentity error = %v, want context cancellation", err)
	}
}

func startOwnedChild(t *testing.T, cmd *exec.Cmd) {
	t.Helper()

	if err := cmd.Start(); err != nil {
		t.Fatalf("start owned child: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
}
