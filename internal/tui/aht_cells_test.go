package tui

import (
	"testing"
	"time"

	"github.com/zigai/aht/v2/pkg/aht"

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
		Status: session.AgentStatusWaiting,
		Detail: aht.StateDetail{Value: aht.ActivityDetailPermission, Quality: aht.DetailQualityCurrent, Authority: "hook", ObservedAt: at},
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

func TestAHTReasonCellsDistinguishInactiveMissingUnsupportedAndStale(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  session.AgentStatus
		quality aht.DetailQuality
		field   string
		value   string
		state   layout.State
		text    string
	}{
		{name: "current permission", status: session.AgentStatusWaiting, quality: aht.DetailQualityCurrent, field: display.FieldAgentWaitingReason, value: "permission", state: layout.Present, text: "permission"},
		{name: "current limit", status: session.AgentStatusFailed, quality: aht.DetailQualityCurrent, field: display.FieldAgentFailureReason, value: "usage_limit", state: layout.Present, text: "usage_limit"},
		{name: "current general failure", status: session.AgentStatusFailed, quality: aht.DetailQualityCurrent, field: display.FieldAgentFailureReason, value: "general", state: layout.Present, text: "general"},
		{name: "stale permission", status: session.AgentStatusWaiting, quality: aht.DetailQualityStale, field: display.FieldAgentWaitingReason, value: "permission", state: layout.Pending},
		{name: "stale limit", status: session.AgentStatusFailed, quality: aht.DetailQualityStale, field: display.FieldAgentFailureReason, value: "usage_limit", state: layout.Pending},
		{name: "missing waiting", status: session.AgentStatusWaiting, quality: aht.DetailQualityMissing, field: display.FieldAgentWaitingReason, state: layout.Pending},
		{name: "old producer", status: session.AgentStatusWaiting, field: display.FieldAgentWaitingReason, state: layout.Pending},
		{name: "unsupported waiting", status: session.AgentStatusWaiting, quality: aht.DetailQualityUnsupported, field: display.FieldAgentWaitingReason, state: layout.Unsupported},
		{name: "inactive waiting", status: session.AgentStatusIdle, quality: aht.DetailQualityCurrent, field: display.FieldAgentWaitingReason, value: "permission", state: layout.Absent},
		{name: "failure while waiting", status: session.AgentStatusWaiting, quality: aht.DetailQualityCurrent, field: display.FieldAgentFailureReason, value: "general", state: layout.Absent},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := session.AgentSession{Status: test.status, Detail: aht.StateDetail{Quality: test.quality}, WaitingReason: test.value, FailureReason: test.value}

			cell := ahtFieldCell(agent, test.field, time.Unix(500, 0))
			if cell.State != test.state || cell.Text != test.text {
				t.Fatalf("reason cell = %+v, want %v/%q", cell, test.state, test.text)
			}
		})
	}
}
