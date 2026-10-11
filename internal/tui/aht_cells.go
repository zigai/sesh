package tui

import (
	"time"

	"github.com/zigai/aht/v2/pkg/aht"

	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui/providers"
)

func (m *Model) publishAHTCells(now time.Time) {
	definition, _ := providers.DefinitionFor(providers.AHT)
	if !m.sessionFormats.agentRowsUseAny(definition.Fields...) {
		return
	}

	rows := make(providers.Rows)

	for _, row := range m.filtered {
		for _, agent := range row.Agents {
			if agent.PaneID == "" {
				continue
			}

			fields := make(map[string]layout.Cell, len(definition.Fields))
			for _, field := range definition.Fields {
				fields[field] = ahtFieldCell(agent, field, now)
			}

			rows[agent.PaneID] = fields
		}
	}

	m.providerRunner.Publish(providers.AHT, rows)
}

func ahtFieldCell(agent session.AgentSession, field string, now time.Time) layout.Cell {
	var value string

	switch field {
	case display.FieldAgentModel:
		value = agent.Model
	case display.FieldAgentWaitingReason:
		return ahtReasonCell(agent, session.AgentStatusWaiting, agent.WaitingReason)
	case display.FieldAgentFailureReason:
		return ahtReasonCell(agent, session.AgentStatusFailed, agent.FailureReason)
	case display.FieldAgentLastTool:
		value = agent.LastTool
	case display.FieldAgentTask:
		value = agent.Task
	case display.FieldAgentPromptAge:
		if !agent.PromptAt.IsZero() {
			value = formatCompactAgeAt(agent.PromptAt, now)
		}
	}

	if value == "" {
		return layout.Cell{Text: "", Role: "", Variants: nil, State: layout.Unsupported}
	}

	return layout.Cell{Text: SanitizeTerminalText(value), Role: "", Variants: nil, State: layout.Present}
}

func ahtReasonCell(agent session.AgentSession, status session.AgentStatus, value string) layout.Cell {
	cell := layout.Cell{Text: "", Role: "", Variants: nil, State: layout.Absent}
	if agent.Status != status {
		return cell
	}

	switch agent.Detail.Quality {
	case aht.DetailQualityUnsupported:
		cell.State = layout.Unsupported
	case aht.DetailQualityMissing, aht.DetailQualityStale, "":
		cell.State = layout.Pending
	case aht.DetailQualityCurrent:
		if value != "" {
			cell.Text = SanitizeTerminalText(value)
			cell.State = layout.Present
		}
	}

	return cell
}
