//go:build linux || darwin

package agents

import (
	"context"

	"github.com/samber/oops"
	"github.com/zigai/aht/pkg/aht"
)

func Status(ctx context.Context, statePath string) (StatusSummary, error) {
	return StatusFromSource(ctx, NewAHTStore(statePath))
}

func StatusFromSource(ctx context.Context, source SessionSource) (StatusSummary, error) {
	if err := ctx.Err(); err != nil {
		return StatusSummary{}, oops.In("agents").Code("operation_canceled").With("operation", "agent_status").Wrap(err)
	}

	agents, err := LoadLiveAgents(ctx, source)
	if err != nil {
		return StatusSummary{}, err
	}

	return SummarizeAgents(agents), nil
}

func SummarizeAgents(agents []AgentSession) StatusSummary {
	summary := StatusSummary{
		Total:       len(agents),
		Live:        0,
		Waiting:     0,
		Failed:      0,
		Unread:      0,
		Idle:        0,
		Running:     0,
		Interrupted: 0,
		Unknown:     0,
	}

	for _, agent := range agents {
		if agent.Presence == string(aht.PresenceLive) {
			summary.Live++
		}

		switch agent.Attention {
		case string(StateWaiting):
			summary.Waiting++
		case string(StateFailed):
			summary.Failed++
		case string(StateUnread):
			summary.Unread++
		case "idle":
			summary.Idle++
		}

		switch agent.Activity {
		case string(aht.ActivityRunning):
			summary.Running++
		case string(aht.ActivityInterrupted):
			summary.Interrupted++
		case string(aht.ActivityUnknown), "":
			summary.Unknown++
		}
	}

	return summary
}
