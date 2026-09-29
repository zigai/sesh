package tui

import (
	"testing"
	"time"

	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui/providers"
)

func TestAHTCellsExposeObservedFieldsAndKeepMissingTelemetryUnsupported(t *testing.T) {
	at := time.Unix(500, 0)

	agent := session.AgentSession{
		PaneID: "%1", Model: "opus", WaitingReason: "permission", LastTool: "Bash",
		Task: "Review changes", PromptAt: at,
	}
	for field, want := range map[string]string{
		display.FieldAgentModel: "opus", display.FieldAgentWaitingReason: "permission",
		display.FieldAgentLastTool: "Bash", display.FieldAgentTask: "Review changes",
		display.FieldAgentPromptAge: "1m",
	} {
		cell := ahtFieldCell(agent, field, at.Add(time.Minute))
		if cell.State != layout.Present || cell.Text != want {
			t.Fatalf("%s = %+v, want %q", field, cell, want)
		}
	}

	for _, field := range []string{
		display.FieldAgentContextPct, display.FieldAgentTokens, display.FieldAgentCost,
		display.FieldAgentTodoDone, display.FieldAgentTodoTotal,
		display.FieldAgentSubagents, display.FieldSubagentCount,
	} {
		if got := ahtFieldCell(agent, field, at).State; got != layout.Unsupported {
			t.Fatalf("%s state = %v, want Unsupported", field, got)
		}
	}

	if got := ahtFieldCell(session.AgentSession{}, display.FieldAgentModel, at).State; got != layout.Unsupported {
		t.Fatalf("missing model state = %v", got)
	}
}

func TestAHTProviderPublishesPaneKeyedCells(t *testing.T) {
	model := newTestModel()
	model.filtered = []session.Session{{Agents: []session.AgentSession{{PaneID: "%1", Model: "gpt"}}}}
	model.config.Display.AgentsAgentFormat = "{agent_model}"

	formats, err := compileSessionRowFormats(model.config.Display, model.config.Settings)
	if err != nil {
		t.Fatal(err)
	}

	model.sessionFormats = formats
	model.publishAHTCells(time.Now())

	if got := model.providerRunner.Cell(providers.AHT, "%1", display.FieldAgentModel); got.Text != "gpt" || got.State != layout.Present {
		t.Fatalf("model cell = %+v", got)
	}
}
