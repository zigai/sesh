//go:build (linux || darwin) && integration

package cli_test

import (
	"slices"
	"strings"
	"testing"
)

func TestJSONSessionWorkflowSuccessContracts(t *testing.T) {
	home := isolateJSONContractHome(t)
	fixture := newCLISessionFixture(t)

	name := "json-contract"
	createArgs := []string{sessionCmd, "create", name, "--dir", home}
	created := runJSONContract(t, "session.create", append(createArgs, fixture.sessionArgs()...)...)
	assertJSONContractFields(t, created, map[string]any{"directory": home})
	assertJSONContractFields(t, contractJSONValue[map[string]any](t, created, "session"), map[string]any{"kind": "live", "name": name})

	listed := runJSONContract(t, "session.list", sessionCmd, "list", testSocketFlag, fixture.socket)
	rows := contractJSONValue[[]any](t, listed, "sessions")
	row := contractJSONItem(t, rows, "name", name)
	assertJSONContractFields(t, row, map[string]any{"windowCount": float64(1), "attached": false, "current": false})
	_ = contractJSONValue[string](t, row, "id")
	_ = contractJSONValue[string](t, row, "activityAt")

	renamed := runJSONContract(t, "session.rename", append([]string{sessionCmd, "rename", name, "json-renamed"}, fixture.sessionArgs()...)...)
	assertJSONContractFields(t, renamed, map[string]any{"previousName": name, "name": "json-renamed"})
	_ = contractJSONValue[bool](t, renamed, "historyUpdated")

	killed := runJSONContract(t, "session.kill", append([]string{sessionCmd, "kill", "json-renamed"}, fixture.sessionArgs()...)...)
	assertJSONContractFields(t, contractJSONValue[map[string]any](t, killed, "session"), map[string]any{"kind": "live", "name": "json-renamed"})

	_ = runJSONContract(t, "session.create", append(createArgs, fixture.sessionArgs()...)...)
	deleted := runJSONContract(t, "session.delete", append([]string{sessionCmd, "delete", name}, fixture.deleteArgs()...)...)
	assertJSONContractFields(t, deleted, map[string]any{"name": name, "liveKilled": true, "savedDeleted": false})

	for _, panes := range []bool{false, true} {
		args := []string{sessionCmd, "scrollback", testSocketFlag, fixture.socket}
		mode, key := "sessions", "sessions"

		if panes {
			args = append(args, "--panes")
			mode, key = "panes", "panes"
		}

		result := runJSONContract(t, "session.scrollback", args...)
		assertJSONContractFields(t, result, map[string]any{"mode": mode})
		_ = contractJSONValue[[]any](t, result, key)
	}
}

func TestJSONSwitchAndAgentClearSuccessContracts(t *testing.T) {
	isolateJSONContractHome(t)
	fixture := newCLISessionFixture(t)
	origin := fixture.fixtureSession(t)
	fixture.setAmbientTmux(t, origin)
	client := fixture.attachCLIClient(t, origin.ID)
	target := fixture.newLiveSession(t, "json-target")

	for _, path := range [][]string{{sessionCmd, "switch"}} {
		args := slices.Clone(path)
		args = append(args, "json-target")
		result := runJSONContract(t, strings.Join(path, "."), append(args, fixture.sessionArgs()...)...)
		assertJSONContractFields(t, contractJSONValue[map[string]any](t, result, "session"), map[string]any{"kind": "live", "name": "json-target"})
		fixture.setAmbientTmux(t, target)

		if fixture.clientDisplayedSession(t, client) != target.ID {
			t.Fatal("reported switch did not reach the attached client")
		}
	}

	for _, path := range [][]string{{"agent", "window-status-clear"}} {
		runJSONContract(t, strings.Join(path, "."), append(path, testSocketFlag, fixture.socket)...)
	}
}

func TestJSONAgentNavigationSuccessContracts(t *testing.T) {
	isolateJSONContractHome(t)
	fixture := newAgentIntegrationFixture(t)

	cases := []struct {
		path []string
		pane string
		want string
	}{
		{path: []string{"agent", "next"}, pane: fixture.current.PaneID, want: fixture.target.PaneID},
		{path: []string{"agent", "back"}, pane: fixture.target.PaneID, want: fixture.current.PaneID},
		{path: []string{"agent", "focus", fixture.target.PaneID}, pane: fixture.current.PaneID, want: fixture.target.PaneID},
	}
	for _, test := range cases {
		args := slices.Clone(test.path)
		args = append(args, testSocketFlag, fixture.socket, "--agent-state", fixture.statePath, "--pane", test.pane)
		command := strings.Join(test.path[:2], ".")
		result := runJSONContract(t, command, args...)
		assertJSONContractFields(t, result, map[string]any{"pane": test.want})

		if !livePane(t, t.Context(), fixture.server, test.want).PaneActive {
			t.Fatalf("%s reported a pane that is not active", command)
		}
	}
}
