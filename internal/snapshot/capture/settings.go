package capture

import (
	"context"
	"slices"
	"strings"

	"github.com/samber/oops"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/snapshot/model"
)

type optionLister interface {
	ListWith(ctx context.Context, opts gotmux.ListOptionOptions) ([]gotmux.OptionEntry, error)
}

func options(ctx context.Context, scope string, source optionLister) ([]model.Option, error) {
	entries, err := source.ListWith(ctx, gotmux.ListOptionOptions{Inherited: true, Hooks: true})
	if err != nil {
		return nil, oops.In("snapshot.capture").With("operation", "observe_scoped_options").Wrapf(err, "observe scoped options")
	}

	result := make([]model.Option, 0, len(entries))
	for _, entry := range entries {
		if scope == "server" && strings.HasPrefix(entry.Name, "@sesh_origin_") {
			continue
		}

		value, present := entry.Value.Get()
		result = append(result, model.Option{Scope: scope, Name: model.Bytes(entry.Name), Value: model.Bytes(value), Present: present, Inherited: entry.Inherited})
	}

	return result, nil
}

func environment(ctx context.Context, scope gotmux.EnvironmentScope) ([]model.Variable, error) {
	var result []model.Variable

	seen := make(map[string]bool)

	for _, hidden := range []bool{false, true} {
		entries, err := scope.ListWith(ctx, gotmux.ListEnvironmentOptions{Hidden: hidden})
		if err != nil {
			return nil, oops.In("snapshot.capture").With("operation", "enumerate_environment").Wrapf(err, "enumerate environment")
		}

		for _, entry := range entries {
			if seen[entry.Name] {
				if hidden {
					continue
				}

				return nil, environmentUnsupported("ambiguous_enumeration")
			}

			variable, err := environmentEntry(entry, hidden)
			if err != nil {
				return nil, err
			}

			seen[entry.Name] = true

			result = append(result, variable)
		}
	}

	slices.SortFunc(result, func(a, b model.Variable) int { return strings.Compare(string(a.Name), string(b.Name)) })

	return result, nil
}

func environmentUnsupported(reason string) error {
	return oops.In("snapshot.capture").
		Code("capture_unsupported").
		With("operation", "observe_environment", "domain", "S05", "reason", reason).
		Wrap(ErrUnsupported)
}

func environmentEntry(entry gotmux.EnvironmentEntry, hidden bool) (model.Variable, error) {
	// tmux lists names such as exported bash functions (BASH_FUNC_name%%) that restore cannot set again.
	if !shellName(entry.Name) {
		return model.Variable{}, environmentUnsupported("name_unsupported")
	}

	content, present := entry.Value.Value.Get()

	state := "set"
	if entry.Value.Unset {
		state = "removed"
	} else if !present {
		return model.Variable{}, environmentUnsupported("value_unavailable")
	}

	return model.Variable{Name: model.Bytes(entry.Name), Value: model.Bytes(content), State: state, Hidden: hidden}, nil
}

func shellName(name string) bool {
	if name == "" {
		return false
	}

	for i, c := range []byte(name) {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}

	return true
}
