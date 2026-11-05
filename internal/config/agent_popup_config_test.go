package config_test

import (
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/config"
)

func TestAgentPopupEmptySessionsDefaultAndOverrides(t *testing.T) {
	if !config.DefaultConfig().AgentSessions.PopupShowEmptySessions {
		t.Fatal("empty sessions must be shown by default")
	}

	for _, test := range []struct {
		name string
		toml string
		env  string
		want bool
	}{
		{name: "omitted", want: true},
		{name: "disabled", toml: "[agent_sessions]\npopup_show_empty_sessions = false", want: false},
		{name: "enabled", toml: "[agent_sessions]\npopup_show_empty_sessions = true", want: true},
		{name: "environment disables", env: "false", want: false},
		{
			name: "environment overrides file",
			toml: "[agent_sessions]\npopup_show_empty_sessions = false",
			env:  "true", want: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.env != "" {
				t.Setenv("SESH_AGENT_SESSIONS_POPUP_SHOW_EMPTY_SESSIONS", test.env)
			}

			cfg, err := config.Load(
				config.WithExplicitPath("-"),
				config.WithInput(strings.NewReader(test.toml)),
			)
			if err != nil {
				t.Fatal(err)
			}

			if cfg.AgentSessions.PopupShowEmptySessions != test.want {
				t.Fatalf(
					"popup_show_empty_sessions = %t, want %t",
					cfg.AgentSessions.PopupShowEmptySessions,
					test.want,
				)
			}
		})
	}
}
