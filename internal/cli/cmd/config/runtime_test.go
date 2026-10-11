package config

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
	cmdsession "github.com/zigai/sesh/internal/cli/cmd/session"
	"github.com/zigai/sesh/internal/session"
)

func TestCommandConfigurationSnapshotIsNotChangedByCallers(t *testing.T) {
	root := newRootCommand(t.Context())
	root.SetContext(t.Context())
	root.SetIn(strings.NewReader("[settings]\ndisplay_modes = ['agents']\n"))

	if err := root.ParseFlags([]string{testConfigFlag, "-"}); err != nil {
		t.Fatal(err)
	}

	first, err := clibase.LoadCommandConfig(root.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	first.Settings.DisplayModes[0] = "changed"

	second, err := clibase.LoadCommandConfig(root.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	if second.Settings.DisplayModes[0] != "agents" {
		t.Fatal("a caller changed the invocation's resolved configuration")
	}

	second.Settings.DisplayModes[0] = "also-changed"

	third, err := clibase.LoadCommandConfig(root.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	if third.Settings.DisplayModes[0] != "agents" {
		t.Fatal("a subsequent caller changed the cached configuration")
	}
}

func TestCommandConfigurationSnapshotProcessEnvironmentIsNotChangedByCallers(t *testing.T) {
	root := newRootCommand(t.Context())
	root.SetContext(t.Context())
	root.SetIn(strings.NewReader("[snapshot]\nprocess_environment = ['ORIGINAL_VAR']\n"))

	if err := root.ParseFlags([]string{testConfigFlag, "-"}); err != nil {
		t.Fatal(err)
	}

	first, err := clibase.LoadCommandConfig(root.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	if len(first.Snapshot.ProcessEnvironment) != 1 || first.Snapshot.ProcessEnvironment[0] != "ORIGINAL_VAR" {
		t.Fatalf("first = %#v, want [ORIGINAL_VAR]", first.Snapshot.ProcessEnvironment)
	}

	first.Snapshot.ProcessEnvironment[0] = "MUTATED_VAR"

	second, err := clibase.LoadCommandConfig(root.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	if len(second.Snapshot.ProcessEnvironment) != 1 || second.Snapshot.ProcessEnvironment[0] != "ORIGINAL_VAR" {
		t.Fatalf("second = %#v, want [ORIGINAL_VAR]", second.Snapshot.ProcessEnvironment)
	}
}

func TestSeparateInvocationsResolveTheirOwnConfiguration(t *testing.T) {
	for _, key := range []string{"f2", "f3"} {
		root := newRootCommand(t.Context())
		root.SetContext(t.Context())
		root.SetIn(bytes.NewBufferString(fmt.Sprintf("[keys]\nhelp = %q\n", key)))

		if err := root.ParseFlags([]string{testConfigFlag, "-"}); err != nil {
			t.Fatal(err)
		}

		cfg, err := clibase.LoadCommandConfig(root.Context(), root)
		if err != nil {
			t.Fatal(err)
		}

		if cfg.Keys.Help != key {
			t.Fatalf("resolved key = %q, want %q", cfg.Keys.Help, key)
		}
	}
}

func TestListSharesWorkflowHistoryPath(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	for _, explicit := range []bool{false, true} {
		cmd, _, err := cmdsession.NewCommand().Find([]string{"list"})
		if err != nil {
			t.Fatal(err)
		}

		path := filepath.Join(stateHome, "sesh", "history.json")
		if explicit {
			path = filepath.Join(t.TempDir(), "private-state", "explicit-history.json")
			if err := cmd.ParseFlags([]string{"--history", path}); err != nil {
				t.Fatal(err)
			}
		}

		sessions := []session.Session{
			{Name: "alphabetically-first", Origin: session.OriginLocal, TmuxServerSocket: "/isolated/unused.sock"},
			{Name: "recently-switched", Origin: session.OriginLocal, TmuxServerSocket: "/isolated/unused.sock"},
		}

		if _, err := session.UpdateState(t.Context(), path, func(state *session.State) {
			state.RecordSwitchIdentity(sessions[1].SemanticIdentity())
		}); err != nil {
			t.Fatal(err)
		}

		state, err := cmdsession.LoadHistoryState(cmd)
		if err != nil {
			t.Fatal(err)
		}

		session.SortSessions(sessions, state)

		if sessions[0].Name != "recently-switched" {
			t.Fatal("list ignored the history written by session workflows")
		}
	}
}
