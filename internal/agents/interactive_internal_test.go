//go:build linux || darwin

package agents

import (
	"os"
	"os/exec"
	"reflect"
	"testing"

	"github.com/zigai/aht/pkg/aht"
	"github.com/zigai/aht/pkg/registry"
)

func TestAgentProcessArguments(t *testing.T) {
	t.Parallel()

	if got := agentProcessArguments(os.Getpid()); !reflect.DeepEqual(got, os.Args) {
		t.Fatalf("process arguments = %q, want %q", got, os.Args)
	}

	if got := agentProcessArguments(-1); len(got) != 0 {
		t.Fatalf("invalid PID arguments = %q", got)
	}
}

func TestNonInteractiveAgentReadsLiveProcessArguments(t *testing.T) {
	t.Parallel()

	printPID := startSyntheticAgentProcess(t, "-p")
	idlePID := startSyntheticAgentProcess(t)

	printMode := aht.Session{
		Harness:  aht.HarnessClaude,
		Presence: aht.PresenceLive,
		Activity: new(aht.ActivityRunning),
		Process:  &registry.ProcessIdentity{PID: printPID},
	}
	interactive := aht.Session{
		Harness:  aht.HarnessClaude,
		Presence: aht.PresenceLive,
		Activity: new(aht.ActivityIdle),
		Process:  &registry.ProcessIdentity{PID: idlePID},
	}

	if !nonInteractiveAgent(printMode) {
		t.Fatal("print-mode arguments must mark the session headless")
	}

	if nonInteractiveAgent(interactive) {
		t.Fatal("interactive arguments must not mark the session headless")
	}
}

func startSyntheticAgentProcess(t *testing.T, args ...string) int {
	t.Helper()

	argv := append([]string{"-c", "printf ready; read line", "claude"}, args...)

	command := exec.CommandContext(t.Context(), "sh", argv...)

	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}

	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = input.Close()
		_ = command.Wait()
	})

	buffer := make([]byte, len("ready"))
	if _, err := output.Read(buffer); err != nil {
		t.Fatal(err)
	}

	return command.Process.Pid
}
