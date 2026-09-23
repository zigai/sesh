//go:build integration

package cli_test

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func requirePickerTermtrace(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("termtrace"); err != nil {
		if os.Getenv(pickerTerminalIntegrationRequired) == "1" {
			t.Fatalf("termtrace is required by %s: %v", pickerTerminalIntegrationRequired, err)
		}

		t.Skip("termtrace is required for terminal integration evidence")
	}
}

func TestTermtraceRequirementRejectsMissingTool(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, required := range []string{"0", "1"} {
		t.Run("required="+required, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			t.Setenv(pickerTerminalIntegrationRequired, required)
			t.Setenv(terminalAvailabilityProbe, "1")

			command := exec.CommandContext(t.Context(), executable, "-test.run=^TestTermtraceAvailabilityProbe$", "-test.v")

			output, runErr := command.CombinedOutput()
			if required == "0" {
				if runErr != nil || !strings.Contains(string(output), "--- SKIP: TestTermtraceAvailabilityProbe") {
					t.Fatalf("optional missing tool must skip: %v\n%s", runErr, output)
				}

				return
			}

			var exitErr *exec.ExitError
			if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("required missing tool must fail: %v\n%s", runErr, output)
			}

			if !strings.Contains(string(output), "termtrace is required by "+pickerTerminalIntegrationRequired) {
				t.Fatalf("missing required-tool diagnostic:\n%s", output)
			}
		})
	}
}

func TestTermtraceAvailabilityProbe(t *testing.T) {
	if os.Getenv(terminalAvailabilityProbe) != "1" {
		t.Skip("subprocess fixture for terminal availability")
	}

	requirePickerTermtrace(t)
	t.Fatal("empty PATH unexpectedly resolved the terminal-test tool")
}
