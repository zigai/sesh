package setup_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli/clibase"
)

func writeSetupConfig(t *testing.T, text string) string {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	return configPath
}

func TestSetupTmuxHelp(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(t, "setup", "tmux", "--help")
	if err != nil {
		t.Fatalf("setup tmux --help failed: %v\nstderr: %s", err, stderr)
	}

	for _, flag := range []string{"--apply", "--force", testSocketFlag} {
		if !strings.Contains(stdout, flag) {
			t.Fatalf("help missing %s: %s", flag, stdout)
		}
	}
}

func TestSetupTmuxPrintsBindingsWithoutSocket(t *testing.T) {
	t.Setenv("TMUX", "")

	stdout, stderr, err := executeCommand(t, "setup", "tmux", testNoConfigFlag)
	if err != nil {
		t.Fatalf("setup tmux failed: %v\nstderr: %s", err, stderr)
	}

	caller := " --client '#{client_name}' --pane '#{pane_id}' --client-session '#{session_id}' --client-width '#{client_width}' --client-height '#{client_height}'"

	for _, want := range []string{
		rootBinding("S-F12", "sesh agent next"+caller) + `\""`,
		rootBinding("M-S-F12", "sesh agent next --local=true"+caller) + `\""`,
		rootBinding("S-F1", "sesh agent back"+caller) + `\""`,
		rootBinding("C-o", "sesh pick popup"+caller) + `\""`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestSetupTmuxJSONPlansBindings(t *testing.T) {
	t.Setenv("TMUX", "")

	stdout, stderr, err := executeCommand(t, "setup", "tmux", testNoConfigFlag, testJSONFlag)
	if err != nil {
		t.Fatalf("setup tmux --json failed: %v\nstderr: %s", err, stderr)
	}

	for _, want := range []string{`"table": "root"`, `"action": "pick.popup"`, `"status": "planned"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %s: %s", want, stdout)
		}
	}
}

func TestSetupTmuxRendersConfiguredBindings(t *testing.T) {
	t.Setenv("TMUX", "")

	configPath := writeSetupConfig(t, `[[keys.bind]]
key = "F20"
action = "agent.latest"
in = "tmux"

[[keys.bind]]
key = "C-o"
in = "tmux"

[[keys.bind]]
key = "M-s"
action = "snapshot.save"
in = "tmux"
table = "prefix"
`)

	stdout, stderr, err := executeCommand(t, "setup", "tmux", testConfigFlag, configPath)
	if err != nil {
		t.Fatalf("setup tmux with configured bindings failed: %v\nstderr: %s", err, stderr)
	}

	for _, want := range []string{
		rootBinding("S-F8", "sesh agent latest "),
		"bind-key -T prefix M-s run-shell -b \"sesh snapshot save '#{session_id}'\"",
		"bind-key -T root S-F12 ",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}

	if strings.Contains(stdout, " C-o ") {
		t.Fatalf("unbound C-o is still rendered:\n%s", stdout)
	}
}

func TestSetupTmuxRejectsBindingsTmuxCannotRun(t *testing.T) {
	t.Setenv("TMUX", "")

	for name, text := range map[string]string{
		"invalid key":    "[[keys.bind]]\nkey = \"not a key\"\naction = \"agent.back\"\nin = \"tmux\"\n",
		"invalid table":  "[[keys.bind]]\nkey = \"M-b\"\naction = \"agent.back\"\nin = \"tmux\"\ntable = \"bad table\"\n",
		"missing params": "[[keys.bind]]\nkey = \"M-c\"\naction = \"session.create\"\nin = \"tmux\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := executeCommand(t, "setup", "tmux", testConfigFlag, writeSetupConfig(t, text))
			if clibase.ExitCode(err) != clibase.ExitCodeGeneral {
				t.Fatalf("exit code = %d (%v), want the configuration error status %d", clibase.ExitCode(err), err, clibase.ExitCodeGeneral)
			}
		})
	}
}

func TestSetupTmuxApplyTargetsAmbientServerWithoutSocket(t *testing.T) {
	t.Setenv("TMUX", filepath.Join(t.TempDir(), "ambient-tmux.sock")+",123,0")

	_, _, err := executeCommand(t, "setup", "tmux", "--apply", testNoConfigFlag)
	if !errors.Is(err, gotmux.ErrNoServer) {
		t.Fatalf("error = %v, want the stopped ambient server to report no tmux server", err)
	}
}

// rootBinding is the start of a rendered root-table binding: a key pressed in a remote attachment
// goes on to the remote tmux, otherwise the command runs.
func rootBinding(key, command string) string {
	return `bind-key -T root ` + key + ` if-shell -F "#{@sesh_remote}" "send-keys ` + key + `" "run-shell -b \"` + command
}
