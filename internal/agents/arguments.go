//go:build linux || darwin

package agents

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/zigai/aht/v2/pkg/aht"
)

const minAgentCommandArguments = 2

func NonInteractiveArguments(harness aht.Harness, argv []string) bool {
	args := agentCommandArguments(argv)

	positionalSeen := false
	nonInteractive := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}

		flag, value, assigned := strings.Cut(arg, "=")
		if interactiveAgentFlag(harness, flag) {
			return false
		}

		if nonInteractiveFlag(harness, flag) {
			return true
		}

		if !assigned && agentOptionTakesValue(harness, flag) && i+1 < len(args) {
			i++
			value = args[i]
		}

		if nonInteractiveMode(harness, flag, value) {
			return true
		}

		if strings.HasPrefix(arg, "-") {
			continue
		}

		if !positionalSeen {
			nonInteractive = nonInteractiveSubcommand(harness, arg)
		}

		positionalSeen = true
	}

	return nonInteractive
}

func nonInteractiveFlag(harness aht.Harness, flag string) bool {
	switch harness {
	case aht.HarnessPi, aht.HarnessOmp, aht.HarnessClaude, aht.HarnessCursor, aht.HarnessGrok:
		return flag == "-p" || flag == "--print"
	case aht.HarnessCopilot:
		return flag == "-p" || flag == "--prompt"
	case aht.HarnessAgy:
		return slices.Contains([]string{"-p", "--print", "--prompt"}, flag)
	case aht.HarnessCline:
		return flag == "--json"
	case aht.HarnessKimiCode:
		return flag == "--print" || flag == "--acp"
	case aht.HarnessHermes:
		return slices.Contains([]string{"-z", "-q", "--query", "--query-file"}, flag)
	default:
		return false
	}
}

func nonInteractiveSubcommand(harness aht.Harness, command string) bool {
	switch harness {
	case aht.HarnessCodex:
		return slices.Contains([]string{"exec", "e", "review", "app-server", "mcp-server"}, command)
	case aht.HarnessDroid:
		return command == "exec"
	case aht.HarnessOpenCode, aht.HarnessKilo:
		return slices.Contains([]string{"run", "serve", "web", "acp"}, command)
	case aht.HarnessGoose:
		return command == "run"
	case aht.HarnessOpenClaw:
		return command == "agent" || command == "gateway"
	default:
		return false
	}
}

func agentOptionTakesValue(harness aht.Harness, flag string) bool {
	if flag == "-p" {
		return harness == aht.HarnessCodex || harness == aht.HarnessKimiCode
	}

	if flag == "-c" {
		return harness == aht.HarnessCodex
	}

	switch flag {
	case "--mode", "--model", "-m", "--provider", "--api-key", "--system-prompt", "--append-system-prompt",
		"--session", "--session-id", "--session-dir", "--fork", "--name", "-n", "--models",
		"--tools", "-t", "--exclude-tools", "-xt", "--thinking", "--extension", "-e",
		"--skill", "--prompt-template", "--theme", "--use-theme", "--tui-mode",
		"--config", "--profile", "--cd", "-C", "--cwd", "--dir", "--agent",
		"--interactive", "-i", "--permission-mode", "--output-format", "--input-format", "--prompt", "--message":
		return true
	default:
		return false
	}
}

func nonInteractiveMode(harness aht.Harness, flag, value string) bool {
	return (harness == aht.HarnessPi || harness == aht.HarnessOmp) && flag == "--mode" && (value == "json" || value == "rpc")
}

func agentCommandArguments(argv []string) []string {
	if len(argv) < minAgentCommandArguments {
		return nil
	}

	args := argv[1:]
	if isRuntimeLauncher(filepath.Base(argv[0])) {
		args = args[1:]
	}

	return args
}

func interactiveAgentFlag(harness aht.Harness, flag string) bool {
	return harness == aht.HarnessGoose && (flag == "--interactive" || flag == "-s")
}

func isRuntimeLauncher(name string) bool {
	switch name {
	case "node", "bun", "deno", "python", "python3", "uv", "uvx", "npx", "pnpm", "yarn", "env", "sh", "bash", "zsh":
		return true
	default:
		return false
	}
}
