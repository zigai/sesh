//go:build linux || darwin

package cmdpicker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zigai/aht/v2/pkg/aht"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/session"
)

func TestPickerConversionPreservesDetailEvidenceAndActivityAge(t *testing.T) {
	at := time.Unix(500, 0).UTC()
	source := agents.AgentSession{
		Activity: string(aht.ActivityFailed), Attention: string(agents.StateFailed),
		FailureReason: "usage_limit", ActivityChanged: at.Add(-time.Minute),
		Detail: aht.StateDetail{Value: aht.DetailUsageLimit, Quality: aht.DetailCurrent, Authority: "screen", ObservedAt: at},
	}

	agent := pickerAgentSession(source)
	if agent.Status != session.AgentStatusFailed || agent.FailureReason != "usage_limit" || agent.Detail != source.Detail || !agent.StatusSince.Equal(source.ActivityChanged) {
		t.Fatalf("picker conversion = %+v", agent)
	}

	data, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}

	var decoded session.AgentSession
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.Detail != source.Detail || decoded.FailureReason != "usage_limit" {
		t.Fatalf("JSON lost normalized evidence: %s", data)
	}
}

func TestPickerInterruptedAgentRetainsMachineStateAndCountsAsIdle(t *testing.T) {
	at := time.Unix(500, 0)

	summaries := pickerAgentSummaries([]agents.AgentSession{{
		Activity: string(aht.ActivityInterrupted), Attention: agents.AttentionFor(string(aht.ActivityInterrupted)),
		ActivityChanged: at, SessionID: "$1", PaneID: "%1",
	}})
	if len(summaries) != 1 || summaries[0].Interrupted != 1 || !summaries[0].IdleSince.Equal(at) || summaries[0].Agents[0].Status != session.AgentStatusInterrupted {
		t.Fatalf("interrupted summary = %+v", summaries)
	}
}

func TestPickerConversionPreservesInitialIdleAndAge(t *testing.T) {
	at := time.Unix(500, 0).UTC()

	agent := pickerAgentSession(agents.AgentSession{
		Activity: string(aht.ActivityIdle), Attention: "idle", InitialIdle: true, ActivityChanged: at,
	})
	if agent.Status != session.AgentStatusIdle || !agent.InitialIdle || !agent.StatusSince.Equal(at) {
		t.Fatalf("picker conversion = %+v", agent)
	}

	data, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}

	var decoded session.AgentSession
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if !decoded.InitialIdle || !decoded.StatusSince.Equal(at) {
		t.Fatalf("JSON lost initial idle or age = %s", data)
	}
}
