//go:build linux || darwin

package setup

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/remote"
)

const (
	// BindingFlag marks a command run by a rendered key binding, so the CLI reports through tmux
	// messages instead of run-shell output and exit status.
	BindingFlag = "tmux-binding"
	rootTable   = "root"

	popupOptionPrefix = "@sesh_popup_"
	popupDefaultMode  = "default"
)

var (
	ErrInvalidKey   = errors.New("invalid tmux key")
	ErrInvalidTable = errors.New("invalid tmux key table")
	errNoProgram    = errors.New("binding has no program")
)

var callerFormats = []string{
	"--client", "'#{client_name}'",
	"--pane", "'#{pane_id}'",
	"--client-session", "'#{session_id}'",
	"--client-width", "'#{client_width}'",
	"--client-height", "'#{client_height}'",
}

type Binding struct {
	Key     string   `json:"key"`
	Table   string   `json:"table"`
	Action  string   `json:"action"`
	Purpose string   `json:"purpose"`
	Command string   `json:"command"`
	Program []string `json:"program"`
}

func (b Binding) CommandSequence() (gotmux.CommandSequence, error) {
	if len(b.Program) == 0 {
		return gotmux.CommandSequence{}, oops.In("setup").
			Code("setup_tmux_failed").
			With("operation", "build_command_sequence").
			Wrap(errNoProgram)
	}

	command, err := gotmux.NewCommand(b.Program[0], b.Program[1:]...)
	if err != nil {
		return gotmux.CommandSequence{}, oops.In("setup").
			Code("setup_tmux_failed").
			With("operation", "build_command_sequence").
			Wrap(err)
	}

	sequence, err := gotmux.Sequence(command)
	if err != nil {
		return gotmux.CommandSequence{}, oops.In("setup").
			Code("setup_tmux_failed").
			With("operation", "build_command_sequence").
			Wrap(err)
	}

	return sequence, nil
}

func (b Binding) Text() string {
	parts := make([]string, 0, len(b.Program))
	for _, argument := range b.Program {
		parts = append(parts, quote(argument))
	}

	return "bind-key -T " + b.Table + " " + b.Key + " " + strings.Join(parts, " ")
}

func Bindings(bindings []action.Binding) ([]Binding, error) {
	tmux := action.SurfaceBindings(bindings, action.SurfaceTmux)
	rendered := make([]Binding, 0, len(tmux))

	for _, binding := range tmux {
		if !gotmux.Key(binding.Key).Valid() {
			return nil, fmt.Errorf("%w: %s", ErrInvalidKey, binding.Key)
		}

		if err := ValidateTable(binding.Table); err != nil {
			return nil, err
		}

		entry, ok := action.Lookup(binding.Action)
		if !ok {
			return nil, fmt.Errorf("%w: %s", action.ErrUnknownAction, binding.Action)
		}

		descriptor := entry.Describe()
		command := actionCommand(descriptor, binding.Values)
		rendered = append(rendered, Binding{
			Key:     binding.Key,
			Table:   binding.Table,
			Action:  string(binding.Action),
			Purpose: purpose(descriptor, binding.Values),
			Command: command,
			Program: bindingProgram(binding, command),
		})
	}

	return rendered, nil
}

func ValidateTable(table string) error {
	if table == "" || strings.ContainsAny(table, " \t\n\x00") {
		return ErrInvalidTable
	}

	return nil
}

func RenderBlock(bindings []Binding) string {
	var out strings.Builder

	out.WriteString("# sesh native bindings\n")

	for _, binding := range bindings {
		out.WriteString(binding.Text())
		out.WriteString("\n")
	}

	return out.String()
}

// ShellWord quotes value for the shell run-shell starts and doubles '#' so tmux does not expand it
// as a format.
func ShellWord(value string) string {
	value = strings.ReplaceAll(value, "#", "##")
	if value != "" &&
		strings.Trim(
			value,
			"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:=@%+-",
		) == "" {
		return value
	}

	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// PopupCommandOption names the server option holding the tmux command that opens the picker popup
// for mode, or for the saved mode when mode is empty.
func PopupCommandOption(mode string) string {
	return popupOptionPrefix + "command_" + popupOptionMode(mode)
}

// PopupSizeOption names the server option holding the client size PopupCommandOption was sized for.
func PopupSizeOption(mode string) string {
	return popupOptionPrefix + "size_" + popupOptionMode(mode)
}

func popupOptionMode(mode string) string {
	if mode == "" {
		return popupDefaultMode
	}

	return mode
}

func bindingProgram(binding action.Binding, command string) []string {
	program := []string{"run-shell", "-b", command}
	if binding.Action == action.PickPopup.ID {
		program = popupProgram(binding.Values.String("mode"), command)
	}

	if binding.Table != rootTable {
		return program
	}

	return forwardInRemoteAttachment(binding.Key, program)
}

// popupProgram opens the popup from the command the picker saved, without starting a process first.
// The launching command runs instead until one is saved for this client size.
func popupProgram(mode string, command string) []string {
	return []string{
		"if-shell",
		"-F",
		"#{==:#{" + PopupSizeOption(mode) + "},#{client_width}x#{client_height}}",
		"run-shell -C " + quote("#{E:"+PopupCommandOption(mode)+"}"),
		"run-shell -b " + quote(command),
	}
}

func forwardInRemoteAttachment(key string, program []string) []string {
	words := make([]string, 0, len(program))
	for _, word := range program {
		words = append(words, quote(word))
	}

	return []string{
		"if-shell",
		"-F",
		"#{" + remote.AttachmentPaneOption + "}",
		"send-keys " + quote(key),
		strings.Join(words, " "),
	}
}

func actionCommand(descriptor action.Descriptor, values action.Values) string {
	words := []string{"sesh", descriptor.ID.Group(), descriptor.ID.Verb()}

	switch {
	case descriptor.Subject == action.SubjectAgentPane:
		words = append(words, "'#{pane_id}'")
	case descriptor.Subject == action.SubjectTask:
		words = append(words, "'#{@sesh_task}'")
	case descriptor.Subject != action.SubjectNone && descriptor.Arity != action.ArityOptional:
		words = append(words, "'#{session_id}'")
	}

	words = append(words, paramWords(descriptor.Params, values)...)

	if descriptor.NeedsClient {
		words = append(words, callerFormats...)
	}

	words = append(words, "--"+BindingFlag)

	return strings.Join(words, " ")
}

func paramWords(params []action.Param, values action.Values) []string {
	var words []string

	for _, param := range params {
		if param.Positional && values.Has(param.Name) {
			words = append(words, ShellWord(values.String(param.Name)))
		}
	}

	for _, param := range params {
		if param.Positional || !values.Has(param.Name) {
			continue
		}

		switch param.Kind {
		case action.ParamBool:
			words = append(words, "--"+param.Name+"="+values.String(param.Name))
		case action.ParamList:
			for _, item := range values.Strings(param.Name) {
				words = append(words, "--"+param.Name, ShellWord(item))
			}
		case action.ParamText, action.ParamPath, action.ParamChoice:
			words = append(words, "--"+param.Name, ShellWord(values.String(param.Name)))
		}
	}

	return words
}

func purpose(descriptor action.Descriptor, values action.Values) string {
	if len(values) == 0 {
		return descriptor.Summary
	}

	names := slices.Sorted(maps.Keys(values))
	settings := make([]string, 0, len(names))

	for _, name := range names {
		settings = append(settings, name+"="+strings.Join(values[name], ","))
	}

	return descriptor.Summary + " (" + strings.Join(settings, ", ") + ")"
}

func quote(argument string) string {
	if argument == "" {
		return `""`
	}

	if !strings.ContainsAny(argument, " \t\"'\\$;#") {
		return argument
	}

	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(argument) + `"`
}
