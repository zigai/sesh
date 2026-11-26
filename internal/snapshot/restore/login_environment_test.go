//go:build linux || darwin

package restore

import (
	"testing"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func variable(name string, value string, state string) model.Variable {
	return model.Variable{
		Name:   model.Bytes(name),
		Value:  model.Bytes(value),
		State:  state,
		Hidden: false,
	}
}

func TestLocalizeReplacesSavedLoginVariablesInEveryScope(t *testing.T) {
	login := LoginVariables{names: map[string]bool{"XAUTHORITY": true, "SESSION_MANAGER": true}}

	saved := model.Manifest{}
	saved.Source.Environment = []model.Variable{
		variable("XAUTHORITY", "/run/user/1000/xauth_old", "set"),
		variable("EDITOR", "nano", "set"),
	}
	saved.Sessions = []model.Session{{
		Environment: []model.Variable{
			variable("SESSION_MANAGER", "local/host:@/tmp/.ICE-unix/1", "set"),
			variable("XAUTHORITY", "", "absent"),
		},
	}}
	saved.Panes = []model.Pane{{
		Launch: model.Launch{Environment: []model.Variable{
			variable("XAUTHORITY", "/run/user/1000/xauth_old", "set"),
		}},
	}}

	localized := login.Localize(saved, map[string]string{"XAUTHORITY": "/run/user/1000/xauth_new"})

	assertVariables(t, "global", localized.Source.Environment, []model.Variable{
		variable("XAUTHORITY", "/run/user/1000/xauth_new", "set"),
		variable("EDITOR", "nano", "set"),
	})
	assertVariables(t, "session", localized.Sessions[0].Environment, []model.Variable{
		variable("SESSION_MANAGER", "", "removed"),
		variable("XAUTHORITY", "", "absent"),
	})
	assertVariables(t, "pane launch", localized.Panes[0].Launch.Environment, []model.Variable{
		variable("XAUTHORITY", "/run/user/1000/xauth_new", "set"),
	})
	assertVariables(t, "saved global", saved.Source.Environment, []model.Variable{
		variable("XAUTHORITY", "/run/user/1000/xauth_old", "set"),
		variable("EDITOR", "nano", "set"),
	})
}

func assertVariables(t *testing.T, scope string, got []model.Variable, want []model.Variable) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("%s environment = %+v, want %+v", scope, got, want)
	}

	for index := range want {
		if string(got[index].Name) != string(want[index].Name) ||
			string(got[index].Value) != string(want[index].Value) ||
			got[index].State != want[index].State {
			t.Fatalf("%s environment = %+v, want %+v", scope, got, want)
		}
	}
}
