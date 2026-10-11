package task_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/task"
)

var errNotOnPath = errors.New("not on PATH")

func TestHarnessesOfferConfiguredEntriesInOrderAndResolveNameOnlyOnes(t *testing.T) {
	cfg := config.DefaultTasksConfig()
	cfg.Harness = []config.HarnessConfig{
		{Name: "pi", Command: nil},
		{Name: "claude-yolo", Command: []string{"claude", "--dangerously-skip-permissions"}},
		{Name: "codex", Command: nil},
	}

	offered := task.Harnesses(cfg, onPath("pi"))

	want := []task.Harness{
		{Name: "pi", Command: []string{"pi"}},
		{Name: "claude-yolo", Command: []string{"claude", "--dangerously-skip-permissions"}},
	}
	if !slices.EqualFunc(offered, want, func(a, b task.Harness) bool {
		return a.Name == b.Name && slices.Equal(a.Command, b.Command)
	}) {
		t.Fatalf("Harnesses() = %+v, want %+v: name-only pi resolved, uninstalled codex left out", offered, want)
	}
}

func TestHarnessesOfferEveryInstalledSupportedHarnessWithoutConfiguration(t *testing.T) {
	offered := task.Harnesses(config.DefaultTasksConfig(), onPath("pi", "claude"))

	names := make([]string, 0, len(offered))
	for _, harness := range offered {
		names = append(names, harness.Name)
	}

	slices.Sort(names)

	if !slices.Equal(names, []string{"claude", "pi"}) {
		t.Fatalf("Harnesses() = %v, want the installed claude and pi", names)
	}
}

func onPath(executables ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		if slices.Contains(executables, name) {
			return "/usr/bin/" + name, nil
		}

		return "", errNotOnPath
	}
}
