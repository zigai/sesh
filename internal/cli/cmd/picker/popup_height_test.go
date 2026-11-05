//go:build linux || darwin

package picker

import (
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func TestAgentsPopupGrowthRespectsConfiguredBaselineAndTerminalLimit(t *testing.T) {
	for _, test := range []struct {
		name      string
		fitting   int
		available int
		want      string
	}{
		{name: "no agent rows preserves configured height", fitting: 4, available: 60, want: "25"},
		{name: "agent rows grow popup", fitting: 45, available: 60, want: "45"},
		{name: "agent rows respect growth cap", fitting: 1000, available: 60, want: "48"},
		{name: "short terminal retains margin", fitting: 1000, available: 9, want: "7"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Settings.DisplayModes = []string{"agents"}
			cfg.Popup.AgentsWidth = "128"
			cfg.Popup.AgentsHeight = "25"
			state := &session.State{PopupDisplayMode: "agents"}
			state.SetPopupHeight("agents", test.fitting)
			state.SetPopupHeight("sesh", 1000)

			_, height := popupDimensions(
				cfg,
				state,
				gotmux.Size{Width: 200, Height: test.available},
			)
			if height != test.want {
				t.Fatalf("height = %s, want %s", height, test.want)
			}
		})
	}
}
