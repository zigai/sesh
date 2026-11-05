//go:build (linux || darwin) && integration

package app

import (
	"os"
	"path/filepath"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

func TestMoveWindowDispatchSupportsPickerBindingAndStaleDestination(t *testing.T) {
	server := tmuxtest.NewServer(t)

	for _, name := range []string{"stale-source", "source", "partial-source"} {
		stale := name == "stale-source"
		partial := name == "partial-source"

		source, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{Name: name})
		if err != nil {
			t.Fatal(err)
		}

		dest, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{Name: name + "-dest"})
		if err != nil {
			t.Fatal(err)
		}

		rows, err := session.ListLive(t.Context(), server, "")
		if err != nil {
			t.Fatal(err)
		}

		var sourceRow session.Session

		for _, row := range rows {
			if row.ID == string(source.ID()) {
				sourceRow = row
			}
		}

		panes, err := source.Panes(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		subject := action.Subject{Sessions: []session.Session{sourceRow}, Pane: string(panes[0].ID)}

		input, err := action.SessionMoveWindow.Bind(
			subject,
			action.Values{"destination": {string(dest.ID())}},
		)
		if err != nil {
			t.Fatal(err)
		}

		if stale {
			if err := dest.Kill(t.Context()); err != nil {
				t.Fatal(err)
			}
		}

		history := ""
		if partial {
			history = filepath.Join(t.TempDir(), "history.json")
			if err := os.WriteFile(history, []byte("invalid history"), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		local := NewLocal(LocalOptions{Server: server, HistoryPath: history})

		output, err := local.Dispatch(
			t.Context(),
			action.Invocation{Action: action.SessionMoveWindow.ID, Input: input},
		)
		if stale {
			if action.CodeOf(err) != action.CodeTargetChanged {
				t.Fatalf("stale destination error = %v", err)
			}

			if _, err := source.Info(t.Context()); err != nil {
				t.Fatalf("stale destination destroyed source: %v", err)
			}

			continue
		}

		if partial {
			if action.CodeOf(err) != action.CodePartial {
				t.Fatalf("partial move error = %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}

		result, ok := output.(action.MovedWindow)
		if !ok || !result.Moved || result.Window != string(panes[0].WindowID) ||
			result.Destination.ID != string(dest.ID()) ||
			result.Destination.Name != name+"-dest" ||
			result.Source.Name != name || result.HistoryUpdated == partial {
			t.Fatalf("move result = %+v", output)
		}
	}
}
