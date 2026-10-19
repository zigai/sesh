//go:build linux || darwin

package restore

import (
	"bytes"
	"context"
	"slices"
	"strings"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/snapshot/model"
)

// TargetOptions lets restore leave out options the target tmux no longer has; tmux rejects those
// without changing anything, so the rejection is not an uncertain effect.
type TargetOptions struct {
	session map[string]bool
	window  map[string]bool
}

func ReadTargetOptions(ctx context.Context, server *gotmux.Server) (TargetOptions, error) {
	// The global session and window tables name every option and hook the server knows.
	session, err := optionNames(ctx, server.GlobalSessionOptions())
	if err != nil {
		return TargetOptions{session: nil, window: nil}, err
	}

	window, err := optionNames(ctx, server.GlobalWindowOptions())
	if err != nil {
		return TargetOptions{session: nil, window: nil}, err
	}

	return TargetOptions{session: session, window: window}, nil
}

func optionNames(ctx context.Context, scope optionScope) (map[string]bool, error) {
	entries, err := scope.ListWith(ctx, gotmux.ListOptionOptions{Inherited: false, Hooks: true})
	if err != nil {
		return nil, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "read_target_options", "reason", "scope_list_failed").
			Wrap(err)
	}

	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		names[optionBaseName(entry.Name)] = true
	}

	return names, nil
}

func optionBaseName(name string) string {
	base, _, _ := strings.Cut(name, "[")

	return base
}

func (t TargetOptions) Adapt(m model.Manifest) (model.Manifest, int) {
	adapted := m
	dropped := 0

	keep := func(options []model.Option) []model.Option {
		kept := slices.DeleteFunc(slices.Clone(options), t.unsupported)
		dropped += len(options) - len(kept)

		return kept
	}

	adapted.Source.Options = keep(m.Source.Options)

	adapted.Sessions = slices.Clone(m.Sessions)
	for index := range adapted.Sessions {
		adapted.Sessions[index].Options = keep(adapted.Sessions[index].Options)
	}

	adapted.Windows = slices.Clone(m.Windows)
	for index := range adapted.Windows {
		adapted.Windows[index].Options = keep(adapted.Windows[index].Options)
	}

	adapted.Panes = slices.Clone(m.Panes)
	for index := range adapted.Panes {
		adapted.Panes[index].Options = keep(adapted.Panes[index].Options)
	}

	return adapted, dropped
}

func (t TargetOptions) unsupported(option model.Option) bool {
	if len(option.Name) == 0 || bytes.HasPrefix(option.Name, []byte("@")) {
		return false
	}

	name := optionBaseName(string(option.Name))

	switch option.Scope {
	case "session", "global-session":
		return !t.session[name]
	case "window", "global-window", "pane":
		return !t.window[name]
	default:
		return false
	}
}
