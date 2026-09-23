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

	for _, prefix := range [][]string{{actionCmd}} {
		t.Run(strings.Join(prefix, " ")+"sessions", func(t *testing.T) {
			name := "json-contract"
			newArgs := append(sessionContractCommand(prefix, "new"), name, "--dir", home)
			created := runJSONContract(t, sessionContractName(prefix, "new"), append(newArgs, fixture.sessionArgs()...)...)
			assertJSONContractFields(t, created, map[string]any{"session": name, "directory": home, "created": true})

			listed := runJSONContract(t, "list", "list", testSocketFlag, fixture.socket)
			rows := contractJSONValue[[]any](t, listed, "sessions")
			row := contractJSONItem(t, rows, "name", name)
			assertJSONContractFields(t, row, map[string]any{"windowCount": float64(1), "attached": false, "current": false})
			_ = contractJSONValue[string](t, row, "id")
			_ = contractJSONValue[string](t, row, "activityAt")

			renameArgs := append(sessionContractCommand(prefix, "rename"), "json-renamed", testSessionFlag, name)
			renamed := runJSONContract(t, sessionContractName(prefix, "rename"), append(renameArgs, fixture.sessionArgs()...)...)
			assertJSONContractFields(t, renamed, map[string]any{"previousName": name, "name": "json-renamed", "renamed": true})

			killArgs := append(sessionContractCommand(prefix, "kill"), "json-renamed")
			killed := runJSONContract(t, sessionContractName(prefix, "kill"), append(killArgs, fixture.sessionArgs()...)...)
			assertJSONContractFields(t, killed, map[string]any{"session": "json-renamed", "killed": true})

			_ = runJSONContract(t, sessionContractName(prefix, "new"), append(newArgs, fixture.sessionArgs()...)...)
			deleteArgs := append(sessionContractCommand(prefix, "delete"), name)
			deleted := runJSONContract(t, sessionContractName(prefix, "delete"), append(deleteArgs, fixture.deleteArgs()...)...)
			assertJSONContractFields(t, deleted, map[string]any{"session": name, "liveKilled": true, "savedDeleted": false})
		})
	}

	for _, panes := range []bool{false, true} {
		args := []string{testHistoryCmd, testSocketFlag, fixture.socket}
		mode, key := "sessions", "sessions"

		if panes {
			args = append(args, "--panes")
			mode, key = "panes", "panes"
		}

		result := runJSONContract(t, testHistoryCmd, args...)
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

	for _, path := range [][]string{{actionCmd, "session-switch"}} {
		args := slices.Clone(path)
		args = append(args, "json-target")
		result := runJSONContract(t, strings.Join(path, "."), append(args, fixture.sessionArgs()...)...)
		assertJSONContractFields(t, result, map[string]any{"session": "json-target", "switched": true})
		fixture.setAmbientTmux(t, target)

		if fixture.clientDisplayedSession(t, client) != target.ID {
			t.Fatal("reported switch did not reach the attached client")
		}
	}

	for _, path := range [][]string{{actionCmd, "agent-status", "clear"}} {
		result := runJSONContract(t, strings.Join(path, "."), append(path, testSocketFlag, fixture.socket)...)
		assertJSONContractFields(t, result, map[string]any{"cleared": true})
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
		{path: []string{actionCmd, "agent-next"}, pane: fixture.current.PaneID, want: fixture.target.PaneID},
		{path: []string{actionCmd, "agent-back"}, pane: fixture.target.PaneID, want: fixture.current.PaneID},
		{path: []string{actionCmd, "agent-focus", "--target-pane", fixture.target.PaneID}, pane: fixture.current.PaneID, want: fixture.target.PaneID},
	}
	for _, test := range cases {
		args := slices.Clone(test.path)
		args = append(args, testSocketFlag, fixture.socket, "--state", fixture.statePath, "--pane", test.pane)
		command := strings.Join(test.path[:2], ".")
		result := runJSONContract(t, command, args...)
		assertJSONContractFields(t, result, map[string]any{"targetPaneId": test.want})

		if !livePane(t, t.Context(), fixture.server, test.want).PaneActive {
			t.Fatalf("%s reported a pane that is not active", command)
		}
	}
}
