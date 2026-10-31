//go:build integration

package capture

import (
	"os"
	"os/exec"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestCaptureOmitsLiveOriginBindingsAndPreservesUserOptions(t *testing.T) {
	server := tmuxtest.NewServer(t)
	settings := server.Options()

	const (
		binding = "@sesh_origin_test"
		project = "@project"
	)

	if err := settings.SetUser(t.Context(), binding, "live-attachment"); err != nil {
		t.Fatal(err)
	}

	if err := settings.SetUser(t.Context(), project, "retained-setting"); err != nil {
		t.Fatal(err)
	}

	captured, err := options(t.Context(), "server", settings)
	if err != nil {
		t.Fatal(err)
	}

	byName := make(map[string]model.Option, len(captured))
	for _, option := range captured {
		byName[string(option.Name)] = option
	}

	if _, present := byName[binding]; present {
		t.Fatal("captured a live attachment binding")
	}

	if retained := byName[project]; !retained.Present || string(retained.Value) != "retained-setting" {
		t.Fatal("capture lost the unrelated user option")
	}

	value, err := settings.User(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}

	if current, present := value.Local.Get(); !present || current != "live-attachment" {
		t.Fatal("capture changed the live attachment binding")
	}
}

func TestCaptureSkipsEnvironmentNamesRestoreCannotSet(t *testing.T) {
	server := tmuxtest.NewServer(t)
	session, _ := captureSessionFixture(t, server, "exported-function")

	binary := os.Getenv("TMUX_TEST_BINARY")
	if binary == "" {
		binary = "tmux"
	}

	set := exec.CommandContext(t.Context(), binary, "-S", server.Endpoint().SocketPath, "set-environment", "-g", "BASH_FUNC_greet%%", "() { echo hi; }")
	if output, err := set.CombinedOutput(); err != nil {
		t.Fatalf("set exported function: %v: %s", err, output)
	}

	if err := server.Environment().Set(t.Context(), "PLAIN_VALUE", "kept"); err != nil {
		t.Fatal(err)
	}

	observation, err := Observe(t.Context(), server, []gotmux.SessionID{session.ID()})
	if err != nil {
		t.Fatalf("capture with an exported function in the environment: %v", err)
	}

	names := make(map[string]bool, len(observation.Manifest.Source.Environment))
	for _, variable := range observation.Manifest.Source.Environment {
		names[string(variable.Name)] = true
	}

	if names["BASH_FUNC_greet%%"] || !names["PLAIN_VALUE"] {
		t.Fatalf("captured environment names = %v, want PLAIN_VALUE without the exported function", names)
	}
}
