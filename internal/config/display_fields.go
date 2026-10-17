package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/pelletier/go-toml/v2"

	"github.com/zigai/sesh/internal/display"
)

var (
	errFieldCommandInvalidType  = errors.New("field must be a shell command string or table")
	errFieldCommandMustBeString = errors.New("field command must be a string")
	errFieldArgsMustBeArray     = errors.New("field args must be an array")
	errFieldArgsMustBeStrings   = errors.New("field args must be strings")
	errFieldRefreshMustBeString = errors.New("field refresh must be a string")
	errUnknownFieldCommandKey   = errors.New("unknown field key")
)

type FieldCommand struct {
	ShellCommand string   `toml:"-"`
	Command      string   `toml:"command,omitempty"`
	Args         []string `toml:"args,omitempty"`
	Refresh      string   `toml:"refresh,omitempty"`
}

type FieldCommands map[string]FieldCommand

func (f *FieldCommands) UnmarshalTOML(data []byte) error {
	var raw map[string]any

	d := toml.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&raw); err != nil {
		return fmt.Errorf("decode display.fields: %w", err)
	}

	if *f == nil {
		*f = make(FieldCommands, len(raw))
	}

	for name, value := range raw {
		command, err := parseFieldCommand(value)
		if err != nil {
			return fmt.Errorf("display.fields.%s: %w", name, err)
		}

		(*f)[name] = command
	}

	return nil
}

func (f *FieldCommands) Clone() FieldCommands {
	if *f == nil {
		return nil
	}

	cloned := maps.Clone(*f)
	for name, command := range cloned {
		command.Args = slices.Clone(command.Args)
		cloned[name] = command
	}

	return cloned
}

func parseFieldCommand(value any) (FieldCommand, error) {
	var command FieldCommand

	switch v := value.(type) {
	case string:
		command.ShellCommand = v

		return command, nil
	case map[string]any:
		return parseFieldCommandTable(v)
	default:
		return command, errFieldCommandInvalidType
	}
}

func parseFieldCommandTable(table map[string]any) (FieldCommand, error) {
	var command FieldCommand

	for key, value := range table {
		switch key {
		case "command":
			s, ok := value.(string)
			if !ok {
				return command, errFieldCommandMustBeString
			}

			command.Command = s
		case "args":
			args, err := parseFieldArgs(value)
			if err != nil {
				return command, err
			}

			command.Args = args
		case "refresh":
			s, ok := value.(string)
			if !ok {
				return command, errFieldRefreshMustBeString
			}

			command.Refresh = s
		default:
			return command, fmt.Errorf("%w: %s", errUnknownFieldCommandKey, key)
		}
	}

	return command, nil
}

func parseFieldArgs(value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, errFieldArgsMustBeArray
	}

	args := make([]string, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, errFieldArgsMustBeStrings
		}

		args[i] = s
	}

	return args, nil
}

func (d DisplayConfig) UsedFieldNames() []string {
	var names []string

	for _, format := range []struct {
		kind   display.FormatKind
		format string
	}{
		{display.FormatKindSession, d.SeshFormat},
		{display.FormatKindAgentsSession, d.AgentsSessionFormat},
		{display.FormatKindAgentsAgent, d.AgentsAgentFormat},
		{display.FormatKindAgentsSession, d.InboxGroupFormat},
		{display.FormatKindAgentsAgent, d.InboxAgentFormat},
	} {
		template, err := display.Compile(format.kind, format.format)
		if err != nil {
			continue
		}

		names = append(names, template.CustomFields()...)
	}

	slices.Sort(names)

	return slices.Compact(names)
}
