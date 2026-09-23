package cmdsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
)

func TestSetupTmuxHelp(t *testing.T) {
	t.Parallel()

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "setup", "tmux", "--help")
	if err != nil {
		t.Fatalf("setup tmux --help failed: %v\nstderr: %s", err, stderr)
	}

	for _, flag := range []string{"--apply", "--force", testSocketFlag, "--table", "--next-key", "--back-key", "--picker-key"} {
		if !strings.Contains(stdout, flag) {
			t.Fatalf("help missing %s: %s", flag, stdout)
		}
	}
}

func TestSetupTmuxPrintsBindingsWithoutSocket(t *testing.T) {
	t.Setenv("TMUX", "")

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "setup", "tmux", testNoConfigFlag)
	if err != nil {
		t.Fatalf("setup tmux failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "bind-key -T root S-F12 run-shell -b \"sesh action agent-next\"") {
		t.Fatalf("stdout missing next-agent binding:\n%s", stdout)
	}

	if !strings.Contains(stdout, "bind-key -T root S-F1 run-shell -b \"sesh action agent-back\"") {
		t.Fatalf("stdout missing back binding:\n%s", stdout)
	}

	if !strings.Contains(stdout, "bind-key -T root C-o run-shell -b \"sesh action picker-open -c '#{client_name}' -t '#{pane_id}' --current-session-id '#{session_id}' -w '#{client_width}' --client-height '#{client_height}'\"") {
		t.Fatalf("stdout missing picker binding:\n%s", stdout)
	}

	if strings.Contains(stdout, "resurrect") || strings.Contains(stdout, "continuum") {
		t.Fatalf("rendered block mentions a retired command name:\n%s", stdout)
	}
}

func TestSetupTmuxJSONPlansBindings(t *testing.T) {
	t.Setenv("TMUX", "")

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "setup", "tmux", testNoConfigFlag, testJSONFlag)
	if err != nil {
		t.Fatalf("setup tmux --json failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, `"table": "root"`) {
		t.Fatalf("stdout missing table: %s", stdout)
	}

	if !strings.Contains(stdout, `"status": "planned"`) {
		t.Fatalf("stdout missing planned bindings: %s", stdout)
	}
}

func TestSetupTmuxCustomKeysAndTable(t *testing.T) {
	t.Setenv("TMUX", "")

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(
		root, "setup", "tmux",
		"--table", "prefix",
		"--next-key", "C-n",
		"--local-next-key", "C-l",
		"--back-key", "C-b",
		"--picker-key", "C-p",
		testNoConfigFlag,
	)
	if err != nil {
		t.Fatalf("setup tmux custom keys failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "bind-key -T prefix C-n ") {
		t.Fatalf("stdout missing custom table/key binding:\n%s", stdout)
	}

	if !strings.Contains(stdout, "bind-key -T prefix C-p ") {
		t.Fatalf("stdout missing custom picker binding:\n%s", stdout)
	}
}

func TestSetupTmuxRejectsInvalidKeyAndTable(t *testing.T) {
	t.Setenv("TMUX", "")

	cases := []struct {
		name string
		args []string
	}{
		{name: "invalid key", args: []string{"setup", "tmux", "--next-key", "not a key", testNoConfigFlag}},
		{name: "invalid table", args: []string{"setup", "tmux", "--table", "bad table", testNoConfigFlag}},
		{name: "duplicate key", args: []string{"setup", "tmux", "--back-key", setupDefaultPickerKey, testNoConfigFlag}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newRootCommand(t.Context())

			_, _, err := executeCommand(root, tc.args...)
			if err == nil {
				t.Fatal("expected usage failure, got nil")
			}

			if clibase.ExitCode(err) != clibase.ExitCodeUsage {
				t.Fatalf("exit code = %d, want %d", clibase.ExitCode(err), clibase.ExitCodeUsage)
			}
		})
	}
}

func TestSetupTmuxApplyRequiresSocket(t *testing.T) {
	t.Setenv("TMUX", "/tmp/ambient-tmux.sock,123,0")

	root := newRootCommand(t.Context())

	_, _, err := executeCommand(root, "setup", "tmux", "--apply", testNoConfigFlag)
	if err == nil {
		t.Fatal("expected usage failure without a socket, got nil")
	}

	if clibase.ExitCode(err) != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(err), clibase.ExitCodeUsage)
	}
}

func TestSetupTmuxUsesConfiguredKeysUnlessFlagsOverrideThem(t *testing.T) {
	t.Setenv("TMUX", "")

	configPath := filepath.Join(t.TempDir(), "config.toml")

	configText := `[agent_sessions]
next_idle_key = "F20"
next_idle_local_key = "M-F21"
navigation_back_key = "F22"
`
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, "setup", "tmux", testConfigFlag, configPath)
	if err != nil {
		t.Fatalf("setup tmux with configured keys failed: %v\nstderr: %s", err, stderr)
	}

	for _, key := range []string{"S-F8", "M-S-F9", "S-F10"} {
		if !strings.Contains(stdout, "bind-key -T root "+key+" ") {
			t.Fatalf("stdout missing configured key %q:\n%s", key, stdout)
		}
	}

	root = newRootCommand(t.Context())

	stdout, stderr, err = executeCommand(
		root, "setup", "tmux", testConfigFlag, configPath,
		"--next-key", "C-n",
	)
	if err != nil {
		t.Fatalf("setup tmux with flag override failed: %v\nstderr: %s", err, stderr)
	}

	if !strings.Contains(stdout, "bind-key -T root C-n ") {
		t.Fatalf("stdout missing explicit next-key override:\n%s", stdout)
	}

	for _, key := range []string{"M-S-F9", "S-F10"} {
		if !strings.Contains(stdout, "bind-key -T root "+key+" ") {
			t.Fatalf("stdout missing non-overridden configured key %q:\n%s", key, stdout)
		}
	}
}

const setupDefaultPickerKey = "C-o"
