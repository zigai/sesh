package config_test

import (
	"bytes"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/zigai/sesh/internal/config"
)

func TestAgentSortConfigDefaultsAndRoundTrip(t *testing.T) {
	cfg := config.DefaultConfig()
	if cfg.AgentSessions.PopupSort != "position" || cfg.Keys.AgentSort != "ctrl+s" {
		t.Fatal("agent sorting defaults must be position and ctrl+s")
	}

	overrideTOML := "[agent_sessions]\npopup_sort = \"attention\"\n[keys]\nagent_sort = \"alt+o\"\n"

	d := toml.NewDecoder(bytes.NewReader([]byte(overrideTOML)))
	if err := d.Decode(cfg); err != nil {
		t.Fatal(err)
	}

	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var decoded config.Config
	if err := toml.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.AgentSessions.PopupSort != "attention" || decoded.Keys.AgentSort != "alt+o" {
		t.Fatal("agent sorting config did not survive a TOML round trip")
	}
}

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
