package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
)

func TestSessionCommandsHelp(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"new", "rename", "kill", "delete", "switch"} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, err := executeCommand(t, "session", name, "--help")
			if err != nil {
				t.Fatalf("session %s --help failed: %v\nstderr: %s", name, err, stderr)
			}

			if !strings.Contains(stdout, "sesh session "+name) {
				t.Fatalf("stdout missing session %s in help: %s", name, stdout)
			}
		})
	}
}

func TestSessionCommandsMissingArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{
			name:     "new missing name",
			args:     []string{"session", "new"},
			wantCode: clibase.ExitCodeUsage,
		},
		{
			name:     "rename missing name",
			args:     []string{"session", "rename"},
			wantCode: clibase.ExitCodeUsage,
		},
		{
			name:     "switch missing target",
			args:     []string{"session", "switch"},
			wantCode: clibase.ExitCodeUsage,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := executeCommand(t, tc.args...)
			if err == nil {
				t.Fatalf("%s expected error, got nil", tc.name)
			}

			code := clibase.ExitCode(err)
			if code != tc.wantCode {
				t.Fatalf("%s exit code = %d, want %d", tc.name, code, tc.wantCode)
			}
		})
	}
}

func TestSessionCommandsHonorConfigurationFailures(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("unknown_section = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	exec := executeJSON(
		t,
		"session", "switch", "dev",
		testSocketFlag, "/tmp/sesh-cli-unit-socket",
		testConfigFlag, configPath,
		testJSONFlag,
	)

	if exec.Err == nil {
		t.Fatal("expected a configuration failure")
	}

	if exec.Envelope.OK {
		t.Fatalf("envelope reported success with invalid configuration: %+v", exec.Envelope)
	}

	if exec.Envelope.Error.Code != "configurationFailed" {
		t.Fatalf("error code = %q, want configurationFailed", exec.Envelope.Error.Code)
	}
}
