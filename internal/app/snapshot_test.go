//go:build linux || darwin

package app

import (
	"fmt"
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
)

func TestSaveEnvironmentSelectsCapturedKeys(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Snapshot.ProcessEnvironment = []string{"CONFIGURED", "TMUX", "VIRTUAL_ENV"}

	tests := []struct {
		name  string
		input action.SaveSnapshot
		want  []string
	}{
		{"configured keys without tmux keys", saveInput(true, nil, false), []string{"CONFIGURED", "VIRTUAL_ENV"}},
		{"explicit empty list captures nothing", saveInput(true, []string{}, true), nil},
		{"explicit list replaces configured keys", saveInput(true, []string{"PATH", "TMUX_PANE", "CUSTOM"}, true), []string{"PATH", "CUSTOM"}},
		{"no enrichment captures nothing", saveInput(false, nil, false), nil},
		{"explicit list without enrichment", saveInput(false, []string{"CUSTOM"}, true), []string{"CUSTOM"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			local := &Local{cfg: cfg}

			got, err := local.saveEnvironment(test.input)
			if err != nil {
				t.Fatalf("saveEnvironment: %v", err)
			}

			if !slices.Equal(got, test.want) {
				t.Fatalf("keys = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSaveEnvironmentRejectsInvalidKeys(t *testing.T) {
	t.Parallel()

	tooMany := make([]string, 257)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("K%d", index)
	}

	for name, keys := range map[string][]string{
		"key with equals": {"INVALID=KEY"},
		"too many keys":   tooMany,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			local := &Local{cfg: config.DefaultConfig()}

			_, err := local.saveEnvironment(saveInput(true, keys, true))
			if action.CodeOf(err) != action.CodeInvalidArgument {
				t.Fatalf("error = %v (code %q), want invalid argument", err, action.CodeOf(err))
			}
		})
	}
}

func saveInput(enrichment bool, environment []string, set bool) action.SaveSnapshot {
	return action.SaveSnapshot{Sessions: nil, ProcessEnrichment: enrichment, Environment: environment, EnvironmentSet: set}
}
