//go:build linux || darwin

package setup_test

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/setup"
)

func tmuxBinding(key, table string, id action.ID, values action.Values) action.Binding {
	return action.Binding{Key: key, Action: id, In: action.SurfaceTmux, Table: table, Values: values}
}

func shellWords(t *testing.T, command string) []string {
	t.Helper()

	expanded := strings.NewReplacer("#{session_id}", "$1", "#{pane_id}", "%1", "##", "#").Replace(command)

	output, err := exec.CommandContext(t.Context(), "sh", "-c", `set -- `+expanded+`; printf '%s\0' "$@"`).Output()
	if err != nil {
		t.Fatalf("shell rejected %q: %v", command, err)
	}

	return strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
}

func TestBindingsPassTheCurrentSessionOrPaneAsTheSubject(t *testing.T) {
	t.Parallel()

	rendered, err := setup.Bindings([]action.Binding{
		tmuxBinding("M-s", "root", action.SnapshotSave.ID, action.Values{}),
		tmuxBinding("M-f", "root", action.AgentFocus.ID, action.Values{}),
		tmuxBinding("M-r", "root", action.SnapshotRestore.ID, action.Values{}),
	})
	if err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"sesh", "snapshot", "save", "$1"},
		{"sesh", "agent", "focus", "%1"},
		{"sesh", "snapshot", "restore"},
	}

	for index, binding := range rendered {
		words := shellWords(t, binding.Command)
		if strings.Join(words[:min(len(words), len(want[index]))], " ") != strings.Join(want[index], " ") {
			t.Fatalf("%s runs %q, want it to start with %q", binding.Action, words, want[index])
		}
	}
}

func TestBindingsQuoteParamsForTheShell(t *testing.T) {
	t.Parallel()

	name := `it's "$HOME" #1; echo injected`

	rendered, err := setup.Bindings([]action.Binding{
		tmuxBinding("M-c", "root", action.SessionCreate.ID, action.Values{"name": {name}, "dir": {"/srv/a b"}}),
	})
	if err != nil {
		t.Fatal(err)
	}

	words := shellWords(t, rendered[0].Command)
	if len(words) != 7 || words[3] != name || words[4] != "--dir" || words[5] != "/srv/a b" {
		t.Fatalf("shell words = %q, want the name and directory verbatim", words)
	}
}

func TestBindingsRejectKeysAndTablesTmuxRefuses(t *testing.T) {
	t.Parallel()

	if _, err := setup.Bindings([]action.Binding{tmuxBinding("not a key", "root", action.AgentBack.ID, action.Values{})}); !errors.Is(err, setup.ErrInvalidKey) {
		t.Fatalf("invalid key error = %v, want ErrInvalidKey", err)
	}

	for _, table := range []string{"has space", "has\ttab"} {
		if _, err := setup.Bindings([]action.Binding{tmuxBinding("M-b", table, action.AgentBack.ID, action.Values{})}); !errors.Is(err, setup.ErrInvalidTable) {
			t.Fatalf("table %q error = %v, want ErrInvalidTable", table, err)
		}
	}
}

func TestRenderBlockBindsEachKeyInItsTable(t *testing.T) {
	t.Parallel()

	bindings, err := action.Bindings(config.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	bindings = append(bindings, tmuxBinding("M-s", "prefix", action.SnapshotSave.ID, action.Values{}))

	rendered, err := setup.Bindings(bindings)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(setup.RenderBlock(rendered)), "\n")
	if len(lines) != len(rendered)+1 || !strings.HasPrefix(lines[0], "#") {
		t.Fatalf("block = %q, want a comment and one line per binding", lines)
	}

	for index, binding := range rendered {
		if !strings.HasPrefix(lines[index+1], "bind-key -T "+binding.Table+" "+binding.Key+" ") {
			t.Fatalf("line %q, want %s bound in table %s", lines[index+1], binding.Key, binding.Table)
		}
	}
}

func TestBindingTextQuotesShellMetacharacters(t *testing.T) {
	t.Parallel()

	binding := setup.Binding{Key: "F1", Table: "root", Action: "", Purpose: "test", Command: "", Program: []string{"run-shell", "-b", `echo "$HOME"`}}

	if text := binding.Text(); !strings.Contains(text, `"echo \"\$HOME\""`) {
		t.Fatalf("text = %q, want an escaped quoted argument", text)
	}
}
