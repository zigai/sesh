package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/config"
)

func TestAgentStatusRunAppliesConfiguredWatchOptions(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(config.WithExplicitPath("-"), config.WithInput(strings.NewReader(`
[agent_status]
initial_backoff = "2s"
max_backoff = "30s"
backoff_factor = 3.0
quiet = true
active_bypass = false
clear_failed_on_visit = false
`)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	resolved := cfg.ResolvedAgentStatus()

	watch := agentStatusWatchOptions(resolved, false)
	if watch.InitialBackoff != 2*time.Second || watch.MaxBackoff != 30*time.Second || watch.BackoffFactor != 3.0 || !watch.Quiet {
		t.Fatalf("watch options = %+v, want configured backoff and quiet", watch)
	}

	tracker := agentStatusTrackerOptions(cfg, resolved)
	if tracker.ActiveBypass || tracker.ClearFailedOnVisit {
		t.Fatalf("tracker options = %+v, want configured bypass and visit clearing", tracker)
	}
}

func TestAgentStatusRunQuietFlagOverridesConfig(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(config.WithoutConfig())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	watch := agentStatusWatchOptions(cfg.ResolvedAgentStatus(), true)
	if !watch.Quiet {
		t.Fatal("--quiet must silence the tracker when config leaves quiet unset")
	}
}
