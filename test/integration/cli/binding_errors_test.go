//go:build linux && integration

package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli"
)

func TestBindingActionFailureIsShownOnInvokingClient(t *testing.T) {
	fixture := newCLISessionFixture(t)
	origin := fixture.fixtureSession(t)
	fixture.setAmbientTmux(t, origin)
	client := fixture.attachCLIClient(t, origin.ID)

	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(t.Context(), []string{"action", "agent-focus", "--client", client, "--no-config"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("reported binding failure = %v, want success so tmux adds no exit-status message", err)
	}

	want := "sesh: --target-pane is required"
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

func TestBindingActionFailureKeepsExitStatusOutsideTmux(t *testing.T) {
	fixture := newCLISessionFixture(t)
	clearAmbientTmux(t)

	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(t.Context(), []string{"action", "agent-focus", "--socket", fixture.socket, "--no-config"}, nil, &stdout, &stderr)
	if cli.ExitCode(err) != 2 || !strings.Contains(stderr.String(), "--target-pane is required") {
		t.Fatalf("failure outside tmux = %v / %q, want usage exit and stderr diagnostic", err, stderr.String())
	}
}

func TestBindingActionsKeepReportsOutOfTmuxOutput(t *testing.T) {
	for _, args := range [][]string{
		{"action", "agent-window-status-clear", "--no-config"},
		{"action", "autosave-save-if-due", "--no-config"},
		{"hooks", "run", "session_switch_pre"},
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

	if args[0] == "action" {
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

	clearAmbientTmux(t)

	stdout, stderr, err = executeCommand(t, args...)
	if err != nil || stdout == "" || stderr != "" {
		t.Fatalf("direct output = %q / %q / %v, want a report and success", stdout, stderr, err)
	}
}

func TestBindingJSONFailureKeepsMachineExitStatus(t *testing.T) {
	fixture := newCLISessionFixture(t)
	fixture.setAmbientTmux(t, fixture.fixtureSession(t))

	stdout, stderr, err := executeCommand(t, "action", "agent-focus", "--no-config", "--json")
	if cli.ExitCode(err) != 2 || !json.Valid([]byte(stderr)) || stdout != "" {
		t.Fatalf("JSON failure = %q / %q / %v, want JSON on stderr and usage exit", stdout, stderr, err)
	}
}
