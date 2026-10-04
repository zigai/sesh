package config_test

import (
	"testing"

	"github.com/zigai/sesh/internal/config"
)

func TestAgentSortConfigValidation(t *testing.T) {
	for _, value := range []string{"", "position", "name", "attention", " Attention "} {
		if err := config.ValidateAgentSessionsConfig(config.AgentSessionsConfig{PopupSort: value}); err != nil {
			t.Fatalf("valid sort %q rejected: %v", value, err)
		}
	}

	for _, value := range []string{"harness", "recent", "unknown"} {
		if err := config.ValidateAgentSessionsConfig(config.AgentSessionsConfig{PopupSort: value}); err == nil {
			t.Fatalf("invalid sort %q accepted", value)
		}
	}

	cfg := config.DefaultConfig()

	cfg.Keys.AgentSort = cfg.Keys.CycleMode
	if err := config.ValidateKeyConfig(cfg.Keys); err == nil {
		t.Fatal("duplicate agent_sort binding accepted")
	}
}
