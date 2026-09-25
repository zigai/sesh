//go:build linux || darwin

package agents_test

import (
	"testing"

	"github.com/zigai/aht/v2/pkg/aht"

	"github.com/zigai/sesh/internal/agents"
)

func TestNonInteractiveArguments(t *testing.T) {
	t.Parallel()

	const prompt = "hello"

	tests := []struct {
		name    string
		harness aht.Harness
		args    []string
		want    bool
	}{
		{"pi print", aht.HarnessPi, []string{"pi", "-p", prompt}, true},
		{"pi print after prompt", aht.HarnessPi, []string{"pi", prompt, "--print"}, true},
		{"pi continue print", aht.HarnessPi, []string{"pi", "-c", "-p", prompt}, true},
		{"pi node", aht.HarnessPi, []string{"node", "/opt/pi/cli.js", "-p", prompt}, true},
		{"pi json", aht.HarnessPi, []string{"pi", "--mode", "json"}, true},
		{"pi rpc", aht.HarnessPi, []string{"pi", "--mode=rpc"}, true},
		{"pi text", aht.HarnessPi, []string{"pi", "--mode", "text"}, false},
		{"pi interactive prompt", aht.HarnessPi, []string{"pi", "explain -p"}, false},
		{"pi prompt delimiter", aht.HarnessPi, []string{"pi", "--", "-p"}, false},
		{"pi system prompt", aht.HarnessPi, []string{"pi", "--system-prompt", "-p"}, false},
		{"pi resume", aht.HarnessPi, []string{"pi", "--resume"}, false},
		{"claude print", aht.HarnessClaude, []string{"claude", "--print", prompt}, true},
		{"copilot long prompt", aht.HarnessCopilot, []string{"copilot", "--prompt=hello"}, true},
		{"goose interactive run", aht.HarnessGoose, []string{"goose", "run", "--interactive"}, false},
		{"copilot interactive prompt", aht.HarnessCopilot, []string{"copilot", "-i", "--prompt"}, false},
		{"copilot prompt", aht.HarnessCopilot, []string{"copilot", "-p", prompt}, true},
		{"omp print", aht.HarnessOmp, []string{"omp", "-p", prompt}, true},
		{"kimi prompt stays interactive", aht.HarnessKimiCode, []string{"kimi", "-p", prompt}, false},
		{"kimi print", aht.HarnessKimiCode, []string{"kimi", "--print", "-p", prompt}, true},
		{"codex exec", aht.HarnessCodex, []string{"codex", "--model", "example", "exec", prompt}, true},
		{"codex profile", aht.HarnessCodex, []string{"codex", "-p", "work"}, false},
		{"codex config", aht.HarnessCodex, []string{"codex", "-c", "model=x", "e", prompt}, true},
		{"codex prompt", aht.HarnessCodex, []string{"codex", "--", "exec"}, false},
		{"opencode run", aht.HarnessOpenCode, []string{"opencode", "run", prompt}, true},
		{"opencode project", aht.HarnessOpenCode, []string{"opencode", "/project"}, false},
		{"goose run", aht.HarnessGoose, []string{"goose", "run", "--text", prompt}, true},
		{"droid exec", aht.HarnessDroid, []string{"droid", "exec", prompt}, true},
		{"agy print", aht.HarnessAgy, []string{"agy", "-p", prompt}, true},
		{"agy prompt", aht.HarnessAgy, []string{"agy", "--prompt=hello"}, true},
		{"cline json", aht.HarnessCline, []string{"cline", "--json", prompt}, true},
		{"cline plan", aht.HarnessCline, []string{"cline", "-p", prompt}, false},
		{"hermes query", aht.HarnessHermes, []string{"hermes", "chat", "-q", prompt}, true},
		{"hermes query file", aht.HarnessHermes, []string{"hermes", "chat", "--query-file=task.txt"}, true},
		{"hermes continue", aht.HarnessHermes, []string{"hermes", "--continue"}, false},
		{"cursor print", aht.HarnessCursor, []string{"agent", "-p", prompt}, true},
		{"grok print", aht.HarnessGrok, []string{"grok", "--print", prompt}, true},
		{"kilo run", aht.HarnessKilo, []string{"kilo", "run", prompt}, true},
		{"openclaw agent", aht.HarnessOpenClaw, []string{"openclaw", "agent", "--message", prompt}, true},
		{"unknown harness", aht.Harness("custom"), []string{"custom", "-p", prompt}, false},
		{"missing args", aht.HarnessPi, nil, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := agents.NonInteractiveArguments(test.harness, test.args); got != test.want {
				t.Fatalf("NonInteractiveArguments() = %t, want %t", got, test.want)
			}
		})
	}
}
