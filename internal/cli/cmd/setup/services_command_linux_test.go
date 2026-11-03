//go:build linux

package setup_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/setup"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

func TestSetupServicesPrintsWithoutWriting(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "services",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.Envelope.OK {
		t.Fatalf("setup services failed: %+v\nstderr: %s", exec.Err, exec.Stderr)
	}

	result := clitest.DecodePayload[servicesResultJSON](t, exec.Envelope.Result)
	if result.Applied {
		t.Fatal("preview must not report an applied result")
	}

	if len(result.Services) != 4 {
		t.Fatalf("services = %d, want the autosave service, timer, remote monitor and agent status tracker", len(result.Services))
	}

	service := result.Services[0]
	if !strings.Contains(service.Content, setup.ManagedMarker) {
		t.Fatalf("printed definition is missing the ownership marker:\n%s", service.Content)
	}

	if !strings.Contains(service.Content, "autosave save-if-due") {
		t.Fatalf("printed definition does not use the native command:\n%s", service.Content)
	}

	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("preview wrote into %s: %v", directory, err)
	}
}

func TestSetupServicesApplyWritesOwnedDefinitions(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "services", "--apply",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.Envelope.OK {
		t.Fatalf("setup services --apply failed: %+v\nstderr: %s", exec.Err, exec.Stderr)
	}

	result := clitest.DecodePayload[servicesResultJSON](t, exec.Envelope.Result)
	if !result.Applied || result.Written != 4 {
		t.Fatalf("result = %+v, want four written definitions", result)
	}

	for _, name := range []string{setup.AutosaveServiceName, setup.AutosaveTimerName, setup.RemoteServiceName, setup.AgentStatusServiceName} {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("reading %s failed: %v", name, err)
		}

		if !strings.Contains(string(content), setup.ManagedMarker) {
			t.Fatalf("%s is missing the ownership marker:\n%s", name, content)
		}
	}

	again := executeJSON(
		t, "setup", "services", "--apply",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !again.Envelope.OK {
		t.Fatalf("second apply failed: %s", again.Stderr)
	}

	replanned := clitest.DecodePayload[servicesResultJSON](t, again.Envelope.Result)
	if replanned.Written != 0 {
		t.Fatalf("second apply wrote %d definitions, want 0", replanned.Written)
	}

	for _, service := range replanned.Services {
		if service.State != "current" {
			t.Fatalf("service %s state = %q, want current", service.Name, service.State)
		}
	}
}

func TestSetupServicesPreservesForeignDefinitions(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}

	foreignPath := filepath.Join(directory, setup.AutosaveServiceName)
	foreign := []byte("[Unit]\nDescription=someone else\n")

	if err := os.WriteFile(foreignPath, foreign, 0o600); err != nil {
		t.Fatal(err)
	}

	exec := executeJSON(
		t, "setup", "services", "--apply",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.Err == nil {
		t.Fatal("a foreign definition must be reported as a degraded outcome")
	}

	if clibase.ExitCode(exec.Err) != clibase.ExitCodeGeneral {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.Err), clibase.ExitCodeGeneral)
	}

	result := clitest.DecodePayload[servicesResultJSON](t, exec.Envelope.Result)
	if len(result.Skipped) != 1 || result.Skipped[0] != setup.AutosaveServiceName {
		t.Fatalf("skipped = %v, want the foreign definition", result.Skipped)
	}

	content, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(content) != string(foreign) {
		t.Fatalf("foreign definition was modified:\n%s", content)
	}
}

func TestSetupServicesDefaultsToUserTmuxServer(t *testing.T) {
	isolatedSetupHome(t)

	exec := executeJSON(
		t, "setup", "services", "--with-coordinator",
		"--dir", filepath.Join(t.TempDir(), "units"),
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.Envelope.OK {
		t.Fatalf("setup services failed: %+v\nstderr: %s", exec.Err, exec.Stderr)
	}

	want := clibase.DefaultTmuxSocket()

	result := clitest.DecodePayload[servicesResultJSON](t, exec.Envelope.Result)
	for _, service := range result.Services {
		if service.Kind == string(setup.KindSystemdTimer) {
			continue
		}

		if !targetsSocket(service.Content, want) {
			t.Fatalf("%s does not target tmux's default server %q:\n%s", service.Name, want, service.Content)
		}

		if strings.Contains(service.Content, setup.TmuxServiceName) {
			t.Fatalf("%s references the uninstalled managed tmux server:\n%s", service.Name, service.Content)
		}
	}
}

func TestSetupServicesWithTmuxServiceDefaultsToManagedSocket(t *testing.T) {
	isolatedSetupHome(t)

	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	exec := executeJSON(
		t, "setup", "services", "--with-tmux-service", "--tmux", "/usr/bin/tmux",
		"--dir", filepath.Join(t.TempDir(), "units"),
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.Envelope.OK {
		t.Fatalf("setup services failed: %+v\nstderr: %s", exec.Err, exec.Stderr)
	}

	want := filepath.Join(runtimeDir, "sesh", "tmux.sock")

	result := clitest.DecodePayload[servicesResultJSON](t, exec.Envelope.Result)
	for _, service := range result.Services {
		if service.Kind == string(setup.KindSystemdService) && !targetsSocket(service.Content, want) {
			t.Fatalf("%s does not target the managed server %q:\n%s", service.Name, want, service.Content)
		}
	}
}

func TestSetupServicesWritesRunnableRemoteMonitorDefinition(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "services", "--apply",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.Err != nil {
		t.Fatalf("setup services failed: %v", exec.Err)
	}

	result := clitest.DecodePayload[servicesResultJSON](t, exec.Envelope.Result)
	if len(result.Blocked) != 0 {
		t.Fatalf("blocked = %v, want runnable remote monitor definition", result.Blocked)
	}

	index := slices.IndexFunc(result.Services, func(service serviceJSON) bool { return service.Name == setup.RemoteServiceName })
	if index < 0 || len(result.Services[index].Requires) != 0 {
		t.Fatalf("services = %+v, want a remote monitor with no unmet prerequisite", result.Services)
	}

	if !strings.Contains(result.Services[index].Content, "daemon remote-monitor") {
		t.Fatalf("remote definition does not name the native entry point:\n%s", result.Services[index].Content)
	}

	if _, err := os.Stat(filepath.Join(directory, setup.RemoteServiceName)); err != nil {
		t.Fatalf("runnable definition was not written: %v", err)
	}
}

func TestSetupServicesApplyWritesManagedTmuxService(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")
	tmuxPath := createFakeTmuxExecutable(t)

	exec := executeJSON(
		t, "setup", "services", "--apply",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.Envelope.OK {
		t.Fatalf("setup services --with-tmux-service --apply failed: %+v\nstderr: %s", exec.Err, exec.Stderr)
	}

	result := clitest.DecodePayload[servicesResultJSON](t, exec.Envelope.Result)
	if !result.Applied || result.Written != 6 {
		t.Fatalf("result = %+v, want 6 written definitions including the tmux and restore services", result)
	}

	assertWrittenManagedUnits(t, directory)

	again := executeJSON(
		t, "setup", "services", "--apply",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !again.Envelope.OK {
		t.Fatalf("second apply failed: %s", again.Stderr)
	}

	replanned := clitest.DecodePayload[servicesResultJSON](t, again.Envelope.Result)
	if replanned.Written != 0 {
		t.Fatalf("second apply wrote %d definitions, want 0", replanned.Written)
	}
}

func targetsSocket(content, socket string) bool {
	return strings.Contains(content, "--socket "+socket+" ") || strings.Contains(content, "--socket "+socket+"\n")
}

func assertWrittenManagedUnits(t *testing.T, directory string) {
	t.Helper()

	for _, name := range []string{setup.AutosaveServiceName, setup.AutosaveTimerName, setup.TmuxServiceName, setup.RestoreServiceName} {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("reading %s failed: %v", name, err)
		}

		if !strings.Contains(string(content), setup.ManagedMarker) {
			t.Fatalf("%s is missing the ownership marker:\n%s", name, content)
		}
	}

	tmuxContent, err := os.ReadFile(filepath.Join(directory, setup.TmuxServiceName))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"daemon tmux",
		"daemon wait-ready",
		"autosave final-save",
		"TimeoutStopSec=25s",
	} {
		if !strings.Contains(string(tmuxContent), want) {
			t.Fatalf("tmux service missing %q:\n%s", want, tmuxContent)
		}
	}

	if strings.Contains(string(tmuxContent), "kill-server") {
		t.Fatalf("tmux service must not request kill-server:\n%s", tmuxContent)
	}
}

func TestSetupServicesTmuxServicePreservesForeignDefinition(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}

	foreignPath := filepath.Join(directory, setup.TmuxServiceName)
	foreignContent := []byte("[Unit]\nDescription=foreign tmux\n")

	if err := os.WriteFile(foreignPath, foreignContent, 0o600); err != nil {
		t.Fatal(err)
	}

	tmuxPath := createFakeTmuxExecutable(t)

	exec := executeJSON(
		t, "setup", "services", "--apply",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.Err == nil {
		t.Fatal("a foreign tmux service must be reported as a degraded outcome")
	}

	result := clitest.DecodePayload[servicesResultJSON](t, exec.Envelope.Result)
	if !slices.Contains(result.Skipped, setup.TmuxServiceName) {
		t.Fatalf("skipped = %v, want foreign %s", result.Skipped, setup.TmuxServiceName)
	}

	content, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(content) != string(foreignContent) {
		t.Fatalf("foreign definition was overwritten: %s", content)
	}

	forceExec := executeJSON(
		t, "setup", "services", "--apply", "--force",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !forceExec.Envelope.OK {
		t.Fatalf("apply with --force failed: %s", forceExec.Stderr)
	}

	replaced, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(replaced), setup.ManagedMarker) {
		t.Fatalf("replaced file missing managed marker:\n%s", replaced)
	}
}

func TestSetupServicesPlansTmuxServiceWithoutUnmetPrerequisites(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")
	tmuxPath := createFakeTmuxExecutable(t)

	exec := executeJSON(
		t, "setup", "services",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.Err != nil {
		t.Fatalf("setup services failed: %v", exec.Err)
	}

	result := clitest.DecodePayload[servicesResultJSON](t, exec.Envelope.Result)
	if result.Applied {
		t.Fatal("preview must not write")
	}

	var foundTmux bool

	for _, svc := range result.Services {
		if svc.Name == setup.TmuxServiceName {
			foundTmux = true

			if len(svc.Requires) != 0 {
				t.Fatalf("tmux service requires = %v, want no unmet prerequisites", svc.Requires)
			}
		}
	}

	if !foundTmux {
		t.Fatalf("services = %+v, want %s included", result.Services, setup.TmuxServiceName)
	}
}

func TestSetupServicesCoordinatorPrintsWithoutWriting(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "services", "--with-coordinator",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.Envelope.OK {
		t.Fatalf("setup services --with-coordinator failed: %+v\nstderr: %s", exec.Err, exec.Stderr)
	}

	result := clitest.DecodePayload[servicesResultJSON](t, exec.Envelope.Result)
	if result.Applied {
		t.Fatal("preview must not report an applied result")
	}

	index := slices.IndexFunc(result.Services, func(service serviceJSON) bool { return service.Name == setup.CoordinatorServiceName })
	if index < 0 {
		t.Fatalf("services = %+v, want %s included", result.Services, setup.CoordinatorServiceName)
	}

	service := result.Services[index]

	if !strings.Contains(service.Content, setup.ManagedMarker) {
		t.Fatalf("printed definition is missing the ownership marker:\n%s", service.Content)
	}

	if !strings.Contains(service.Content, "daemon coordinator") {
		t.Fatalf("printed definition does not use daemon coordinator:\n%s", service.Content)
	}

	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("preview wrote into %s: %v", directory, err)
	}
}

func TestSetupServicesWithCoordinatorApplyWritesAllDefinitions(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "services", "--apply",
		"--with-coordinator",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.Envelope.OK {
		t.Fatalf("setup services --with-coordinator --apply failed: %+v\nstderr: %s", exec.Err, exec.Stderr)
	}

	result := clitest.DecodePayload[servicesResultJSON](t, exec.Envelope.Result)
	if !result.Applied || result.Written != 5 {
		t.Fatalf("result = %+v, want 5 written definitions", result)
	}

	for _, name := range []string{setup.AutosaveServiceName, setup.AutosaveTimerName, setup.RemoteServiceName, setup.AgentStatusServiceName, setup.CoordinatorServiceName} {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}

		if !strings.Contains(string(content), setup.ManagedMarker) {
			t.Fatalf("%s is missing the ownership marker:\n%s", name, content)
		}
	}

	coordinator, err := os.ReadFile(filepath.Join(directory, setup.CoordinatorServiceName))
	if err != nil || !strings.Contains(string(coordinator), "daemon coordinator") {
		t.Fatalf("coordinator definition does not run daemon coordinator: %v\n%s", err, coordinator)
	}
}
