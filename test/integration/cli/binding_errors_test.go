//go:build linux && integration

package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli"
	"github.com/zigai/sesh/internal/setup"
)

const bindingFlag = "--" + setup.BindingFlag

func TestBindingActionFailureIsShownOnInvokingClient(t *testing.T) {
	fixture := newCLISessionFixture(t)
	origin := fixture.fixtureSession(t)
	fixture.setAmbientTmux(t, origin)
	client := fixture.attachCLIClient(t, origin.ID)

	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(t.Context(), []string{"agent", "focus", "--client", client, "--no-config", bindingFlag}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("reported binding failure = %v, want success so tmux adds no exit-status message", err)
	}

	want := "sesh: accepts 1 arg(s), received 0"
	deadline := time.Now().Add(5 * time.Second)

	for {
		messages, err := fixture.server.Messages(t.Context(), gotmux.MessagesOptions{Jobs: false, Terminal: false})
		if err != nil {
			t.Fatal(err)
		}

		if strings.Contains(strings.Join(messages, "\n"), want) {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("tmux messages %q do not contain %q", messages, want)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

func TestActionFailureInTmuxPaneKeepsExitStatus(t *testing.T) {
	fixture := newCLISessionFixture(t)
	fixture.setAmbientTmux(t, fixture.fixtureSession(t))

	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(t.Context(), []string{"agent", "focus", "--no-config"}, nil, &stdout, &stderr)
	if cli.ExitCode(err) != 2 || !strings.Contains(stderr.String(), "accepts 1 arg(s), received 0") {
		t.Fatalf("failure in a tmux pane = %v / %q, want usage exit and stderr diagnostic", err, stderr.String())
	}
}

func TestBindingActionsKeepReportsOutOfTmuxOutput(t *testing.T) {
	for _, args := range [][]string{
		{"agent", "window-status-clear", "--no-config", bindingFlag},
		{"hooks", "run", "session_switch_pre", bindingFlag},
	} {
		t.Run(strings.Join(args[:2], "_"), func(t *testing.T) {
			testBindingActionReports(t, args)
		})
	}
}

func testBindingActionReports(t *testing.T, args []string) {
	t.Helper()
	fixture := newCLISessionFixture(t)
	fixture.setAmbientTmux(t, fixture.fixtureSession(t))

	if args[0] != "hooks" {
		args = append(args, "--socket", fixture.socket)
	} else {
		configPath := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(configPath, []byte("[[hooks.session_switch_pre]]\ncommand = 'true'\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		args = append(args, "--config", configPath)
	}

	stdout, stderr, err := executeCommand(t, args...)
	if err != nil {
		t.Fatalf("binding failed: %v / %q", err, stderr)
	}

	if stdout != "" || stderr != "" {
		t.Fatalf("binding output = %q / %q, want empty", stdout, stderr)
	}

	stdout, stderr, err = executeCommand(t, append(args, "--json")...)
	if err != nil || stderr != "" || !json.Valid([]byte(stdout)) {
		t.Fatalf("JSON output = %q / %q / %v, want valid JSON and success", stdout, stderr, err)
	}

	stdout, stderr, err = executeCommand(t, slices.DeleteFunc(args, func(arg string) bool { return arg == bindingFlag })...)
	if err != nil || stdout == "" || stderr != "" {
		t.Fatalf("direct output = %q / %q / %v, want a report and success", stdout, stderr, err)
	}
}

func TestBindingJSONFailureKeepsMachineExitStatus(t *testing.T) {
	fixture := newCLISessionFixture(t)
	fixture.setAmbientTmux(t, fixture.fixtureSession(t))

	stdout, stderr, err := executeCommand(t, "agent", "focus", "--no-config", bindingFlag, "--json")
	if cli.ExitCode(err) != 2 || !json.Valid([]byte(stderr)) || stdout != "" {
		t.Fatalf("JSON failure = %q / %q / %v, want JSON on stderr and usage exit", stdout, stderr, err)
	}
}
