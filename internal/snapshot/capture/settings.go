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

			variable, err := environmentEntry(ctx, scope, entry.Name, hidden)
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

func environmentEntry(ctx context.Context, scope gotmux.EnvironmentScope, name string, hidden bool) (model.Variable, error) {
	value, err := scope.Get(ctx, name, hidden)
	if err != nil {
		return model.Variable{}, oops.In("snapshot.capture").With("operation", "observe_environment_value").Wrapf(err, "observe environment value")
	}

	content, present := value.Value.Get()

	state := "set"
	if value.Unset {
		state = "removed"
	} else if !present {
		return model.Variable{}, environmentUnsupported("value_unavailable")
	}

	return model.Variable{Name: model.Bytes(name), Value: model.Bytes(content), State: state, Hidden: hidden}, nil
}
