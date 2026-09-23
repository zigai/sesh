package cmdsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/setup"
)

var errSetupTestManager = errors.New("private-service-manager-canary")

func TestSetupRefreshApplyReloadsAndPreservesActiveServices(t *testing.T) {
	setupRefreshHome(t)
	seedSetupRefreshServices(t)
	configPath, logPath := runtimeConfig(t, "info")
	manager, calls := setupRefreshManager(t, "")

	execution := executeSetupRefresh(t, manager, "--apply", testConfigFlag, configPath)
	if execution.err != nil || !execution.envelope.OK || execution.stderr != "" {
		t.Fatalf("refresh = %+v", execution)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, execution.envelope.Result)
	if !result.Applied || result.Written != 3 || result.Manager == nil || !result.Manager.Reloaded {
		t.Fatalf("refresh result = %+v", result)
	}

	if !slices.Equal(result.Manager.Restarted, []string{setup.AutosaveTimerName, setup.RemoteServiceName}) {
		t.Fatalf("restart result = %+v", result.Manager)
	}

	if !slices.Equal(*calls, []string{"daemon-reload", "try-restart " + setup.AutosaveTimerName, "try-restart " + setup.RemoteServiceName}) {
		t.Fatalf("manager commands = %v", *calls)
	}

	assertRuntimeLog(t, logPath, "cli", "refresh", "info", "ok", configPath, "private-program-canary", "systemctl")
}

func setupRefreshHome(t *testing.T) {
	t.Helper()

	if runtime.GOOS != setup.PlatformLinux {
		t.Skip("Linux service-manager refresh")
	}

	isolatedSetupHome(t)
}

func seedSetupRefreshServices(t *testing.T) string {
	t.Helper()

	directory, err := setup.ServiceDirectory(setup.PlatformLinux)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{setup.AutosaveTimerName, setup.RemoteServiceName} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("# "+setup.ManagedMarker+"\nold private definition\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return directory
}

func setupRefreshManager(t *testing.T, failing string) (setup.ServiceManager, *[]string) {
	t.Helper()

	var calls []string

	manager := setup.ServiceManager{
		Inspect: func(_ context.Context, executable string, args ...string) (string, error) {
			if executable != "systemctl" || !slices.Equal(args[:len(args)-1], []string{"--user", "show", "--property=ActiveState", "--value"}) {
				t.Fatalf("unexpected inspection: %s %v", executable, args)
			}

			if failing == "inspect" {
				return "", errSetupTestManager
			}

			return "active\n", nil
		},
		Run: func(_ context.Context, executable string, args ...string) error {
			if executable != "systemctl" || args[0] != "--user" {
				t.Fatalf("unexpected manager: %s %v", executable, args)
			}

			call := strings.Join(args[1:], " ")

			calls = append(calls, call)
			if call == failing {
				return errSetupTestManager
			}

			return nil
		},
	}

	return manager, &calls
}

func TestSetupRefreshManagerFailureRetainsPartialJSONAndOneErrorRecord(t *testing.T) {
	for _, phase := range []string{"inspect", "daemon-reload", "try-restart " + setup.RemoteServiceName} {
		t.Run(phase, func(t *testing.T) {
			setupRefreshHome(t)
			directory := seedSetupRefreshServices(t)
			configPath, logPath := runtimeConfig(t, "info")
			manager, _ := setupRefreshManager(t, phase)

			execution := executeSetupRefresh(t, manager, "--apply", testConfigFlag, configPath)
			if !errors.Is(execution.err, errSetupTestManager) || execution.envelope.OK || execution.envelope.Error == nil || execution.stderr != "" {
				t.Fatalf("failed refresh = %+v", execution)
			}

			result := decodeJSONPayload[setupServicesResultJSON](t, execution.envelope.Result)
			if result.Manager == nil {
				t.Fatal("missing partial manager result")
			}

			if phase == "inspect" {
				assertRefreshInspectionDidNotWrite(t, directory, result)
			} else if result.Written != 3 {
				t.Fatalf("lost completed definition writes: %+v", result)
			}

			assertRefreshPartialManagerResult(t, phase, result.Manager)

			if strings.Contains(execution.stdout+execution.stderr, errSetupTestManager.Error()) {
				t.Fatal("private manager diagnostics in JSON")
			}

			assertRuntimeLog(t, logPath, "cli", "refresh", "error", "failed", directory, configPath, "private-program-canary", errSetupTestManager.Error())
		})
	}
}

func assertRefreshPartialManagerResult(t *testing.T, phase string, manager *setup.ServiceRefreshResult) {
	t.Helper()

	if phase == "try-restart "+setup.RemoteServiceName && (!manager.Reloaded || !slices.Equal(manager.Restarted, []string{setup.AutosaveTimerName})) {
		t.Fatalf("lost completed manager operations: %+v", manager)
	}
}

func TestSetupRefreshDisabledLoggingDoesNotCreateSink(t *testing.T) {
	setupRefreshHome(t)
	seedSetupRefreshServices(t)
	configPath, logPath := runtimeConfig(t, "disabled")
	manager, _ := setupRefreshManager(t, "daemon-reload")

	execution := executeSetupRefresh(t, manager, "--apply", testConfigFlag, configPath)
	if execution.err == nil || execution.envelope.OK || execution.stderr != "" {
		t.Fatalf("refresh with logging disabled = %+v", execution)
	}

	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled logging created a sink: %v", err)
	}
}

func assertRefreshInspectionDidNotWrite(t *testing.T, directory string, result setupServicesResultJSON) {
	t.Helper()

	if result.Written != 0 {
		t.Fatalf("failed inspection wrote definitions: %+v", result)
	}

	if _, err := os.Stat(filepath.Join(directory, setup.AutosaveServiceName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed inspection created autosave definition: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(directory, setup.RemoteServiceName))
	if err != nil || !strings.Contains(string(content), "old private definition") {
		t.Fatalf("failed inspection replaced monitor: %s, %v", content, err)
	}
}

func TestSetupRefreshPreviewAndExplicitDirectoryNeverContactManager(t *testing.T) {
	for _, mode := range []string{"preview", "custom-directory", "explicit-default-directory"} {
		t.Run(mode, func(t *testing.T) {
			setupRefreshHome(t)
			directory := seedSetupRefreshServices(t)
			manager := setup.ServiceManager{
				Inspect: func(context.Context, string, ...string) (string, error) {
					t.Fatal("contacted manager from preview or explicit directory")
					return "", errSetupTestManager
				},
				Run: func(context.Context, string, ...string) error {
					t.Fatal("mutated manager from preview or explicit directory")
					return errSetupTestManager
				},
			}
			args := []string{testNoConfigFlag}

			if mode != "preview" {
				if mode == "custom-directory" {
					directory = filepath.Join(t.TempDir(), "custom-units")
				}

				args = append(args, "--apply", "--dir", directory)
			}

			execution := executeSetupRefresh(t, manager, args...)
			if execution.err != nil || !execution.envelope.OK || execution.stderr != "" {
				t.Fatalf("definition-only refresh = %+v", execution)
			}

			result := decodeJSONPayload[setupServicesResultJSON](t, execution.envelope.Result)
			if result.Manager != nil {
				t.Fatalf("definition-only result claims manager operation: %+v", result.Manager)
			}

			if mode == "preview" {
				assertRefreshInspectionDidNotWrite(t, directory, result)
			} else if !result.Applied || result.Written != 3 {
				t.Fatalf("definition-only apply = %+v", result)
			}
		})
	}
}
