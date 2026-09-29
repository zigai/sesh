//go:build linux || darwin

package agents_test

import (
	"testing"
	"time"

	"github.com/zigai/aht/v2/pkg/aht"
	"github.com/zigai/aht/v2/pkg/registry"

	"github.com/zigai/sesh/internal/agents"
)

func TestObservedAgentSessionsDoesNotInferWaitingFromConfigurationOrRuleNames(t *testing.T) {
	t.Parallel()

	for _, activity := range []aht.Activity{aht.ActivityWaiting, aht.ActivityRunning, aht.ActivityIdle} {
		t.Run(string(activity), func(t *testing.T) {
			observed := agents.ObservedAgentSessions([]aht.Session{{
				Harness: aht.HarnessClaude, Liveness: aht.Live{Activity: activity},
				Location: aht.Location{Kind: aht.MultiplexerTmux, PaneID: "%1"},
				Observations: registry.Observations{
					Native: &registry.NativeObservation{Attributes: map[string]string{"claude_permission_mode": "bypassPermissions"}},
					Screen: &registry.ScreenObservation{RuleID: "question_prompt"},
				},
			}})
			if len(observed) != 1 || observed[0].WaitingReason != "" {
				t.Fatalf("untyped observation fabricated a waiting reason: %+v", observed)
			}
		})
	}
}

func TestObservedAgentSessionsUsesNormalizedDetailAndPreservesEvidence(t *testing.T) {
	t.Parallel()

	at := time.Unix(500, 0)
	for _, test := range []struct {
		name     string
		activity aht.Activity
		detail   aht.StateDetail
		waiting  string
		failure  string
	}{
		{name: "native permission", activity: aht.ActivityWaiting, detail: aht.StateDetail{Value: aht.DetailPermission, Quality: aht.DetailCurrent, Authority: "hook", ObservedAt: at}, waiting: "permission"},
		{name: "screen question overrides native metadata", activity: aht.ActivityWaiting, detail: aht.StateDetail{Value: aht.DetailQuestion, Quality: aht.DetailCurrent, Authority: "screen", ObservedAt: at}, waiting: "question"},
		{name: "stale permission stays generic", activity: aht.ActivityWaiting, detail: aht.StateDetail{Value: aht.DetailPermission, Quality: aht.DetailStale, Authority: "hook", ObservedAt: at}},
		{name: "unsupported waiting", activity: aht.ActivityWaiting, detail: aht.StateDetail{Quality: aht.DetailUnsupported}},
		{name: "missing waiting", activity: aht.ActivityWaiting, detail: aht.StateDetail{Quality: aht.DetailMissing}},
		{name: "terminal limit", activity: aht.ActivityFailed, detail: aht.StateDetail{Value: aht.DetailUsageLimit, Quality: aht.DetailCurrent, Authority: "hook", ObservedAt: at}, failure: "usage_limit"},
		{name: "stale limit becomes general", activity: aht.ActivityFailed, detail: aht.StateDetail{Value: aht.DetailUsageLimit, Quality: aht.DetailStale, Authority: "screen", ObservedAt: at}, failure: "general"},
		{name: "old failure is general", activity: aht.ActivityFailed, failure: "general"},
	} {
		t.Run(test.name, func(t *testing.T) {
			detail := test.detail
			source := aht.Session{
				Harness: aht.HarnessClaude, Liveness: aht.Live{Activity: test.activity},
				Location:          aht.Location{Kind: aht.MultiplexerTmux, PaneID: "%1"},
				ActivityChangedAt: at, Detail: &detail,
				Observations: registry.Observations{Native: &registry.NativeObservation{Attributes: map[string]string{"claude_notification_type": "permission_prompt"}}},
			}

			observed := agents.ObservedAgentSessions([]aht.Session{source})
			if len(observed) != 1 {
				t.Fatalf("observed agents = %+v", observed)
			}

			agent := observed[0]
			if agent.WaitingReason != test.waiting || agent.FailureReason != test.failure || agent.Detail != test.detail || !agent.ActivityChanged.Equal(at) {
				t.Fatalf("normalized detail = %+v", agent)
			}

			source.Detail.Value = aht.DetailGeneral
			if agent.Detail != test.detail {
				t.Fatal("conversion retained mutable registry evidence")
			}
		})
	}
}
