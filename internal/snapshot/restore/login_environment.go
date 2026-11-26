//go:build linux || darwin

package restore

import (
	"context"
	"maps"
	"slices"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const updateEnvironmentOption = "update-environment"

// Set afresh for each login by PAM, the session manager or the systemd unit that started tmux; the
// rest come from tmux's update-environment.
var loginIssuedVariables = []string{
	"ICEAUTHORITY",
	"INVOCATION_ID",
	"JOURNAL_STREAM",
	"MANAGERPID",
	"MANAGERPIDFDID",
	"MEMORY_PRESSURE_WATCH",
	"MEMORY_PRESSURE_WRITE",
	"PAM_KWALLET5_LOGIN",
	"SESSION_MANAGER",
	"SYSTEMD_EXEC_PID",
	"XDG_SEAT",
	"XDG_SEAT_PATH",
	"XDG_SESSION_CLASS",
	"XDG_SESSION_ID",
	"XDG_SESSION_PATH",
	"XDG_VTNR",
}

// LoginVariables names the environment variables whose saved values belonged to the login that
// was running when the snapshot was taken, such as XAUTHORITY, which names a file created per
// login.
type LoginVariables struct {
	names map[string]bool
}

func ReadLoginVariables(ctx context.Context, server *gotmux.Server) (LoginVariables, error) {
	names, err := clientEnvironmentNames(ctx, server)
	if err != nil {
		return LoginVariables{names: nil}, err
	}

	for _, name := range loginIssuedVariables {
		names[name] = true
	}

	return LoginVariables{names: names}, nil
}

// Localize replaces every saved login variable with its value in the current login, given as
// live, and unsets the ones the current login lacks, so restored programs never see a value from
// an earlier login.
func (l LoginVariables) Localize(m model.Manifest, live map[string]string) model.Manifest {
	localized := m
	localized.Source.Environment = l.localizeVariables(m.Source.Environment, live)

	localized.Sessions = slices.Clone(m.Sessions)
	for index := range localized.Sessions {
		localized.Sessions[index].Environment = l.localizeVariables(
			localized.Sessions[index].Environment,
			live,
		)
	}

	localized.Panes = slices.Clone(m.Panes)
	for index := range localized.Panes {
		localized.Panes[index].Launch.Environment = l.localizeVariables(
			localized.Panes[index].Launch.Environment,
			live,
		)
	}

	return localized
}

func (l LoginVariables) Names() []string {
	return slices.Sorted(maps.Keys(l.names))
}

func (l LoginVariables) localizeVariables(
	variables []model.Variable,
	live map[string]string,
) []model.Variable {
	if variables == nil {
		return nil
	}

	localized := slices.Clone(variables)

	for index, variable := range localized {
		if variable.State == "absent" || !l.names[string(variable.Name)] {
			continue
		}

		value, ok := live[string(variable.Name)]
		if ok {
			localized[index].Value = model.Bytes(value)
			localized[index].State = "set"
		} else {
			localized[index].Value = nil
			localized[index].State = "removed"
		}
	}

	return localized
}

func clientEnvironmentNames(ctx context.Context, server *gotmux.Server) (map[string]bool, error) {
	entries, err := server.GlobalSessionOptions().
		ListWith(ctx, gotmux.ListOptionOptions{Inherited: false, Hooks: false})
	if err != nil {
		return nil, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "read_login_variables", "reason", "scope_list_failed").
			Wrap(err)
	}

	names := map[string]bool{}

	for _, entry := range entries {
		if optionBaseName(entry.Name) != updateEnvironmentOption {
			continue
		}

		if value, ok := entry.Value.Get(); ok && value != "" {
			names[value] = true
		}
	}

	return names, nil
}
