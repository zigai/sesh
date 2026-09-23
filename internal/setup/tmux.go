//go:build linux || darwin

package setup

import (
	"errors"
	"strings"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

const (
	DefaultNextKey      = "F12"
	DefaultLocalNextKey = "M-F12"
	DefaultBackKey      = "F11"
	DefaultPickerKey    = "C-o"
)

const DefaultTable = "root"

var (
	ErrInvalidKey   = errors.New("invalid tmux key")
	ErrDuplicateKey = errors.New("duplicate tmux key")
	errNoProgram    = errors.New("binding has no program")
)

var ErrInvalidTable = errors.New("invalid tmux key table")

type Keys struct {
	Next      string
	LocalNext string
	Back      string
	Picker    string
}

type Binding struct {
	Key     string   `json:"key"`
	Purpose string   `json:"purpose"`
	Program []string `json:"program"`
}

func DefaultKeys() Keys {
	return Keys{
		Next:      DefaultNextKey,
		LocalNext: DefaultLocalNextKey,
		Back:      DefaultBackKey,
		Picker:    DefaultPickerKey,
	}
}

func (b Binding) CommandSequence() (gotmux.CommandSequence, error) {
	if len(b.Program) == 0 {
		return gotmux.CommandSequence{}, oops.In("setup").Code("setup_tmux_failed").With("operation", "build_command_sequence").Wrap(errNoProgram)
	}

	command, err := gotmux.NewCommand(b.Program[0], b.Program[1:]...)
	if err != nil {
		return gotmux.CommandSequence{}, oops.In("setup").Code("setup_tmux_failed").With("operation", "build_command_sequence").Wrap(err)
	}

	sequence, err := gotmux.Sequence(command)
	if err != nil {
		return gotmux.CommandSequence{}, oops.In("setup").Code("setup_tmux_failed").With("operation", "build_command_sequence").Wrap(err)
	}

	return sequence, nil
}

func (b Binding) Text(table string) string {
	parts := make([]string, 0, len(b.Program))
	for _, argument := range b.Program {
		parts = append(parts, quote(argument))
	}

	return "bind-key -T " + table + " " + b.Key + " " + strings.Join(parts, " ")
}

func Bindings(keys Keys) ([]Binding, error) {
	err := validateKeys(keys)
	if err != nil {
		return nil, err
	}

	return []Binding{
		{
			Key:     keys.Next,
			Purpose: "Switch to the next coding agent needing attention",
			Program: []string{"run-shell", "-b", "sesh action agent-next"},
		},
		{
			Key:     keys.LocalNext,
			Purpose: "Switch to the next coding agent in the current tmux session",
			Program: []string{"run-shell", "-b", "sesh action agent-next --local"},
		},
		{
			Key:     keys.Back,
			Purpose: "Return through agent navigation history",
			Program: []string{"run-shell", "-b", "sesh action agent-back"},
		},
		{
			Key:     keys.Picker,
			Purpose: "Open the sesh session picker popup",
			Program: []string{"run-shell", "-b", "sesh action picker-open -c '#{client_name}' -t '#{pane_id}' --current-session-id '#{session_id}' -w '#{client_width}' --client-height '#{client_height}'"},
		},
	}, nil
}

func ValidateTable(table string) error {
	if table == "" || strings.ContainsAny(table, " \t\n\x00") {
		return ErrInvalidTable
	}

	return nil
}

func RenderBlock(bindings []Binding, table string) string {
	var out strings.Builder

	out.WriteString("# sesh native bindings\n")

	for _, binding := range bindings {
		out.WriteString(binding.Text(table))
		out.WriteString("\n")
	}

	return out.String()
}

func validateKeys(keys Keys) error {
	selected := []string{keys.Next, keys.LocalNext, keys.Back, keys.Picker}
	seen := make(map[string]bool, len(selected))

	for _, key := range selected {
		if !gotmux.Key(key).Valid() {
			return ErrInvalidKey
		}

		if seen[key] {
			return ErrDuplicateKey
		}

		seen[key] = true
	}

	return nil
}

func quote(argument string) string {
	if argument == "" {
		return `""`
	}

	if !strings.ContainsAny(argument, " \t\"'\\$;#") {
		return argument
	}

	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(argument) + `"`
}
