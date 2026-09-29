//go:build linux && integration

package cli_test

import (
	"bytes"
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
