package config_test

import (
	"errors"
	"fmt"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/config"
)

func TestSessionOrderDefaults(t *testing.T) {
	if got := config.DefaultConfig().AgentSessions.SessionOrder; got != nil {
		t.Fatalf("built-in session_order = %#v, want nil", got)
	}

	for _, input := range []string{"", "[agent_sessions]\nsession_order = []\n"} {
		cfg, err := config.Load(
			config.WithExplicitPath("-"),
			config.WithInput(strings.NewReader(input)),
		)
		if err != nil {
			t.Fatal(err)
		}

		if len(cfg.AgentSessions.SessionOrder) != 0 {
			t.Fatalf("session_order = %#v, want no rules", cfg.AgentSessions.SessionOrder)
		}

		assertSessionOrderUnrelatedDefaults(t, cfg)
	}
}

func TestLoadSessionOrderRuleTables(t *testing.T) {
	input := `[[agent_sessions.session_order]]
match = "work-*"
position = "first"

[[agent_sessions.session_order]]
match = "Work?"
position = "normal"

[[agent_sessions.session_order]]
match = "agents"
position = "last"
`

	cfg, err := config.Load(
		config.WithExplicitPath("-"),
		config.WithInput(strings.NewReader(input)),
	)
	if err != nil {
		t.Fatal(err)
	}

	want := []config.SessionOrderRule{
		{Match: "work-*", Position: config.SessionOrderFirst},
		{Match: "Work?", Position: config.SessionOrderNormal},
		{Match: "agents", Position: config.SessionOrderLast},
	}
	if !slices.Equal(cfg.AgentSessions.SessionOrder, want) {
		t.Fatalf("session_order = %#v, want %#v", cfg.AgentSessions.SessionOrder, want)
	}

	assertSessionOrderUnrelatedDefaults(t, cfg)
}

func TestValidateSessionOrderRules(t *testing.T) {
	for _, rules := range [][]config.SessionOrderRule{
		nil,
		{},
		{{Match: "*", Position: config.SessionOrderFirst}},
		{{Match: "work-?", Position: config.SessionOrderNormal}},
		{{Match: "[a-z]*", Position: config.SessionOrderLast}},
		{{Match: `literal\*`, Position: config.SessionOrderLast}},
		{{Match: " Case Sensitive ", Position: config.SessionOrderFirst}},
		{
			{Match: "*", Position: config.SessionOrderFirst},
			{Match: "agents", Position: config.SessionOrderLast},
		},
	} {
		if err := config.ValidateAgentSessionsConfig(config.AgentSessionsConfig{
			SessionOrder: rules,
		}); err != nil {
			t.Fatalf("valid session_order %#v rejected: %v", rules, err)
		}
	}
}

func TestSessionOrderRejectsInvalidRules(t *testing.T) {
	for _, test := range []struct {
		name       string
		rule       config.SessionOrderRule
		field      string
		badPattern bool
	}{
		{name: "empty match", rule: config.SessionOrderRule{Position: "first"}, field: "match"},
		{name: "blank match", rule: config.SessionOrderRule{Match: " \t\n", Position: "first"}, field: "match"},
		{
			name: "unclosed class", rule: config.SessionOrderRule{Match: "[", Position: "first"},
			field: "match", badPattern: true,
		},
		{
			name: "trailing escape", rule: config.SessionOrderRule{Match: `agents\`, Position: "last"},
			field: "match", badPattern: true,
		},
		{
			name: "malformed suffix", rule: config.SessionOrderRule{Match: "prefix*[", Position: "normal"},
			field: "match", badPattern: true,
		},
		{name: "missing position", rule: config.SessionOrderRule{Match: "agents"}, field: "position"},
		{
			name: "unknown position", rule: config.SessionOrderRule{Match: "agents", Position: "middle"},
			field: "position",
		},
		{
			name: "uppercase position", rule: config.SessionOrderRule{Match: "agents", Position: "LAST"},
			field: "position",
		},
		{
			name: "padded position", rule: config.SessionOrderRule{Match: "agents", Position: " last "},
			field: "position",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			// An earlier catch-all must not bypass validation of later rules.
			rules := []config.SessionOrderRule{
				{Match: "*", Position: config.SessionOrderFirst},
				test.rule,
			}
			err := config.ValidateAgentSessionsConfig(
				config.AgentSessionsConfig{SessionOrder: rules},
			)

			wantContext := "agent_sessions.session_order[1]." + test.field
			if err == nil || !strings.Contains(err.Error(), wantContext) {
				t.Fatalf("validation error = %v, want %q", err, wantContext)
			}

			if test.badPattern && !errors.Is(err, path.ErrBadPattern) {
				t.Fatalf("validation error = %v, want path.ErrBadPattern cause", err)
			}

			input := fmt.Sprintf(`[[agent_sessions.session_order]]
match = "*"
position = "first"
[[agent_sessions.session_order]]
match = %q
position = %q
`, test.rule.Match, test.rule.Position)

			_, err = config.Load(
				config.WithExplicitPath("-"),
				config.WithInput(strings.NewReader(input)),
			)
			if err == nil || !strings.Contains(err.Error(), wantContext) {
				t.Fatalf("Load() error = %v, want %q", err, wantContext)
			}
		})
	}
}

func TestLoadSessionOrderRequiresBothFields(t *testing.T) {
	for _, input := range []string{
		"[[agent_sessions.session_order]]\nposition = 'first'\n",
		"[[agent_sessions.session_order]]\nmatch = 'agents'\n",
	} {
		_, err := config.Load(
			config.WithExplicitPath("-"),
			config.WithInput(strings.NewReader(input)),
		)
		if err == nil || !strings.Contains(err.Error(), "agent_sessions.session_order[0]") {
			t.Fatalf("Load(%q) error = %v, want missing rule field error", input, err)
		}
	}
}

func TestCloneSessionOrderIndependence(t *testing.T) {
	cfg := config.DefaultConfig()
	if got := cfg.Clone().AgentSessions.SessionOrder; got != nil {
		t.Fatalf("clone of default session_order = %#v, want nil", got)
	}

	cfg.AgentSessions.SessionOrder = []config.SessionOrderRule{
		{Match: "agents", Position: config.SessionOrderLast},
	}

	cloned := cfg.Clone()
	if !slices.Equal(cloned.AgentSessions.SessionOrder, cfg.AgentSessions.SessionOrder) {
		t.Fatal("clone did not preserve session order rules")
	}

	cloned.AgentSessions.SessionOrder[0].Match = "work-*"

	cfg.AgentSessions.SessionOrder[0].Position = config.SessionOrderFirst
	if cfg.AgentSessions.SessionOrder[0].Match != "agents" {
		t.Fatal("clone shares rule match with original")
	}

	if cloned.AgentSessions.SessionOrder[0].Position != config.SessionOrderLast {
		t.Fatal("original shares rule position with clone")
	}
}

func assertSessionOrderUnrelatedDefaults(t *testing.T, cfg *config.Config) {
	t.Helper()

	want := config.DefaultConfig()
	agentSessions := cfg.AgentSessions

	agentSessions.SessionOrder = nil
	if !reflect.DeepEqual(agentSessions, want.AgentSessions) {
		t.Fatalf("unrelated agent_sessions defaults changed: %#v", agentSessions)
	}

	if !reflect.DeepEqual(cfg.Settings, want.Settings) ||
		!reflect.DeepEqual(cfg.Keys, want.Keys) ||
		!reflect.DeepEqual(cfg.Popup, want.Popup) {
		t.Fatal("session_order changed unrelated settings, keys, or popup defaults")
	}
}
