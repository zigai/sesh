//go:build linux || darwin

package agents

import (
	"github.com/zigai/aht/pkg/aht"
)

func nonInteractiveAgent(agent aht.Session) bool {
	if agent.Harness == aht.HarnessPi {
		native := agent.Observations.Native

		return native == nil || native.Attributes["aht_interaction_mode"] != "tui" ||
			agent.Process == nil || !agent.Process.Complete() || !native.Process.Equal(*agent.Process)
	}

	if native := agent.Observations.Native; native != nil {
		switch native.Attributes["aht_interaction_mode"] {
		case "print", "json", "rpc", "headless", "batch", "acp":
			return true
		case "tui":
			return false
		}
	}

	if agent.Process == nil || agent.Process.PID <= 0 {
		return false
	}

	return NonInteractiveArguments(agent.Harness, agentProcessArguments(agent.Process.PID))
}
