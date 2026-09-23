package capture

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/zigai/aht/pkg/aht"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
)

type unavailableAgentSource struct{ err error }

func (s unavailableAgentSource) List(context.Context, aht.Filter) ([]aht.Session, error) {
	return nil, s.err
}

func TestMissingAgentMetadataDoesNotPreventSessionCapture(t *testing.T) {
	live, err := recoveryAgents(t.Context(), unavailableAgentSource{err: agents.ErrRegistryUnavailable})
	if err != nil || len(live) != 0 {
		t.Fatalf("optional registry unavailable: agents=%d err=%v", len(live), err)
	}
}

func TestAgentMetadataCancellationIsNotIgnored(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := recoveryAgents(ctx, unavailableAgentSource{err: agents.ErrRegistryUnavailable})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestAgentResumeRequiresUniqueExactPaneAndEndpoint(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	var agent agents.AgentSession

	agent.ServerSocket = "/selected/tmux.sock"
	agent.PaneID = "%4"
	agent.PanePID = 120
	agent.Harness = "codex"
	agent.HarnessSessionID = "conversation-id"
	agent.ResumeCommand = []string{executable, "resume", "conversation-id"}

	var pane gotmux.PaneInfo

	pane.ID, pane.PID = "%4", 120

	for _, change := range []string{"valid", "unknown_socket", "shorthand_socket", "other_socket", "other_pane", "other_process", "ambiguous", "missing_command"} {
		t.Run(change, func(t *testing.T) {
			candidate := agent
			changeAgentBinding(&candidate, change)

			live := []agents.AgentSession{candidate}
			if change == "ambiguous" {
				live = append(live, candidate)
			}

			resume := matchingAgentResume(live, agent.ServerSocket, pane)
			if change == "valid" {
				if resume == nil || resume.SessionID != agent.HarnessSessionID || resume.Executable != executable {
					t.Fatalf("exact native resume was not retained: %+v", resume)
				}
			} else if resume != nil {
				t.Fatal("unbound agent metadata authorized resume")
			}
		})
	}
}

func changeAgentBinding(candidate *agents.AgentSession, change string) {
	switch change {
	case "unknown_socket":
		candidate.ServerSocket = ""
	case "shorthand_socket":
		candidate.ServerSocket = "tmux.sock"
	case "other_socket":
		candidate.ServerSocket = "/other/tmux.sock"
	case "other_pane":
		candidate.PaneID = "%5"
	case "other_process":
		candidate.PanePID++
	case "missing_command":
		candidate.ResumeCommand = nil
	}
}
