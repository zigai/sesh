//go:build linux || darwin

package cli

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/setup"
)

// tmuxBindable returns a tmux binding for every action a tmux key can run, with sample values
// for the parameters a key must supply.
func tmuxBindable(t *testing.T) []action.Binding {
	t.Helper()

	cfg := config.DefaultConfig()
	keys := []string{"M-a", "M-b", "M-c", "M-d", "M-e", "M-f", "M-g", "M-h", "M-i", "M-j", "M-k", "M-l", "M-m", "M-n", "M-o", "M-p", "M-q", "M-r", "M-s", "M-t"}

	for _, entry := range action.All() {
		descriptor := entry.Describe()
		bind := config.KeyBindConfig{Key: keys[len(cfg.Keys.Bind)], Action: string(descriptor.ID), In: "tmux", Table: "", Params: map[string]any{}}

		for _, param := range descriptor.Params {
			if param.Required {
				bind.Params[param.Name] = "sample value"
			}
		}

		cfg.Keys.Bind = append(cfg.Keys.Bind, bind)
	}

	bindings, err := action.Bindings(cfg)
	if err != nil {
		t.Fatal(err)
	}

	return bindings
}

func TestTmuxBindingsRunCommandsTheCLIAccepts(t *testing.T) {
	rendered, err := setup.Bindings(tmuxBindable(t))
	if err != nil {
		t.Fatal(err)
	}

	formats := strings.NewReplacer(
		"#{session_id}", "$1", "#{pane_id}", "%1", "#{client_name}", "/dev/pts/1", "#{client_width}", "80", "#{client_height}", "24",
	)

	for _, binding := range rendered {
		command := formats.Replace(binding.Program[2])

		output, err := exec.CommandContext(t.Context(), "sh", "-c", `set -- `+command+`; printf '%s\0' "$@"`).Output()
		if err != nil {
			t.Fatalf("shell rejected %q: %v", command, err)
		}

		args := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")[1:]

		cmd, rest, err := newRootCommand(t.Context()).Find(args)
		if err != nil || cmd.Annotations[clibase.AnnotationAction] != binding.Action {
			t.Fatalf("%q resolves to %v (%v), want the %s command", command, cmd.CommandPath(), err, binding.Action)
		}

		if err := cmd.ParseFlags(rest); err != nil {
			t.Fatalf("%s rejects the flags of %q: %v", cmd.CommandPath(), command, err)
		}

		if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
			t.Fatalf("%s rejects the arguments of %q: %v", cmd.CommandPath(), command, err)
		}
	}
}
