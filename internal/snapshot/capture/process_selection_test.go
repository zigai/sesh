package capture

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestProcessSelectionBoundsBeforeServerAccess(t *testing.T) {
	cases := map[string][]string{
		"duplicate_count": make([]string, process.MaxEnvironmentKeys+1),
		"key_bytes":       {strings.Repeat("K", process.MaxEnvironmentKeyLength+1)},
		"total_bytes":     make([]string, process.MaxEnvironmentTotalKeyBytes/process.MaxEnvironmentKeyLength+1),
	}
	for i := range cases["duplicate_count"] {
		cases["duplicate_count"][i] = "TMUX"
	}

	for i := range cases["total_bytes"] {
		cases["total_bytes"][i] = strings.Repeat("K", process.MaxEnvironmentKeyLength)
	}

	for name, keys := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ObserveWithProcessEnrichment(t.Context(), nil, []gotmux.SessionID{"$0"}, keys)
			if !errors.Is(err, process.ErrEnvironmentLimitExceeded) {
				t.Fatalf("expected bounded selection rejection before server access: %v", err)
			}
		})
	}
}

func TestTerminalActivityDoesNotInvalidateStructuralObservation(t *testing.T) {
	m := model.Manifest{}
	m.Panes = []model.Pane{{ID: "p0"}}

	before, err := stabilityBytes(m)
	if err != nil {
		t.Fatal(err)
	}

	m.Panes[0].Terminal = model.Terminal{
		Artifact: "", History: 400, Alternate: true, CursorX: 13, CursorY: 8, Mode: model.Bytes("copy-mode"),
	}

	after, err := stabilityBytes(m)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(before, after) {
		t.Fatal("screen activity was treated as a changed structural graph")
	}

	if m.Panes[0].Terminal.History != 400 {
		t.Fatal("stability comparison discarded the captured terminal observation")
	}
}

func TestProcessSelectionInvalidNamesBeforeServerAccess(t *testing.T) {
	for _, key := range []string{"", "NAME=value", "NA\x00ME"} {
		_, err := ObserveWithProcessEnrichment(t.Context(), nil, []gotmux.SessionID{"$0"}, []string{key})
		if !errors.Is(err, process.ErrInvalidVariableName) {
			t.Fatalf("expected invalid name rejection before server access: %v", err)
		}
	}
}

func TestProcessRevalidationRequiresEverySelectedPane(t *testing.T) {
	err := revalidateProcesses(t.Context(), []model.Pane{{ID: "p0"}}, map[string]paneProcessRecord{})
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("missing selected process record accepted: %v", err)
	}
}
