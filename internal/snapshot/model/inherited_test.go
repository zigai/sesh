package model

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func option(scope, name, value string, inherited bool) Option {
	return Option{Scope: scope, Name: Bytes(name), Value: Bytes(value), Present: true, Inherited: inherited}
}

// reportedOptionsFixture mirrors tmux's show-options -A output: every built-in global option is
// listed as inherited at session and window scope, and global user options are not.
func reportedOptionsFixture() Manifest {
	m := fixture()
	m.Source.Options = []Option{
		option("server", "escape-time", "10", false),
		option("global-session", "base-index", "1", false),
		option("global-session", "status", "on", false),
		option("global-session", "@plugin", "tpm", false),
		option("global-window", "pane-border-status", "top", false),
		option("global-window", "mode-keys", "vi", false),
	}

	for index := range m.Sessions {
		m.Sessions[index].Options = []Option{
			option("session", "base-index", "1", true),
			option("session", "status", "off", false),
		}
	}

	m.Windows[0].Options = []Option{
		option("window", "mode-keys", "vi", true),
		option("window", "pane-border-status", "top", true),
	}

	m.Panes[0].Top, m.Panes[0].Height = 1, 23
	m.Panes[0].Options = []Option{option("pane", "remain-on-exit", "off", true), option("pane", "@pane-mark", "1", false)}

	return m
}

func TestCompactInheritedOptionsRoundTripsReportedLists(t *testing.T) {
	reported := reportedOptionsFixture()
	if err := reported.Validate(); err != nil {
		t.Fatalf("reported fixture invalid: %v", err)
	}

	compact := reportedOptionsFixture().CompactInheritedOptions()

	if diff := cmp.Diff([]Option{option("session", "status", "off", false)}, compact.Sessions[0].Options); diff != "" {
		t.Fatalf("compact session options (-want +got):\n%s", diff)
	}

	if len(compact.Windows[0].Options) != 0 {
		t.Fatalf("compact window options = %+v, want every inherited option omitted", compact.Windows[0].Options)
	}

	if diff := cmp.Diff(reported.Panes[0].Options, compact.Panes[0].Options); diff != "" {
		t.Fatalf("pane options must stay complete (-want +got):\n%s", diff)
	}

	if err := compact.Validate(); err != nil {
		t.Fatalf("compact manifest invalid: %v", err)
	}

	decoded, err := Decode(mustEncode(t, compact))
	if err != nil {
		t.Fatal(err)
	}

	expanded := decoded.WithInheritedOptions()
	byName := cmpopts.SortSlices(func(left, right Option) bool { return string(left.Name) < string(right.Name) })

	for index, session := range reported.Sessions {
		if diff := cmp.Diff(session.Options, expanded.Sessions[index].Options, byName); diff != "" {
			t.Fatalf("session %s options (-reported +expanded):\n%s", session.ID, diff)
		}
	}

	if diff := cmp.Diff(reported.Windows[0].Options, expanded.Windows[0].Options, byName); diff != "" {
		t.Fatalf("window options (-reported +expanded):\n%s", diff)
	}
}

func TestCompactInheritedOptionsKeepsInheritedValuesThatDiffer(t *testing.T) {
	m := reportedOptionsFixture()
	m.Windows[0].Options[0].Value = Bytes("emacs")
	m = m.CompactInheritedOptions()

	if diff := cmp.Diff([]Option{option("window", "mode-keys", "emacs", true)}, m.Windows[0].Options); diff != "" {
		t.Fatalf("window options (-want +got):\n%s", diff)
	}

	effective := m.EffectiveWindowOptions(m.Windows[0])
	if len(effective) != 2 || string(effective[0].Value) != "emacs" {
		t.Fatalf("effective window options = %+v, want the recorded value kept over the global one", effective)
	}
}

func TestPaneGeometryHonorsInheritedBorderStatus(t *testing.T) {
	m := reportedOptionsFixture().CompactInheritedOptions()

	if err := m.Validate(); err != nil {
		t.Fatalf("pane below an inherited top border status rejected: %v", err)
	}

	m.Source.Options[4].Value = Bytes("off")
	if err := m.Validate(); err == nil {
		t.Fatal("pane offset for a border status the window does not have was accepted")
	}
}

func mustEncode(t *testing.T, m Manifest) []byte {
	t.Helper()

	data, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}

	return data
}
