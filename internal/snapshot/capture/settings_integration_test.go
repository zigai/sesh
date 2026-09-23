//go:build integration

package capture

import (
	"testing"

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
