package cmdsetup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/setup"
)

func TestSetupCommandHelpListsOwnedSubcommands(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommand(newRootCommand(t.Context()), "setup", "--help")
	if err != nil {
		t.Fatalf("setup --help failed: %v\nstderr: %s", err, stderr)
	}

	for _, subcommand := range []string{"tmux", "agents", "autosave", "coordinator", "remote", "refresh"} {
		if !strings.Contains(stdout, subcommand) {
			t.Fatalf("setup help is missing %q:\n%s", subcommand, stdout)
		}
	}
}

func TestSetupAutosavePrintsWithoutWriting(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "autosave",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.envelope.OK {
		t.Fatalf("setup autosave failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
	if result.Applied {
		t.Fatal("preview must not report an applied result")
	}

	if len(result.Services) != 2 {
		t.Fatalf("services = %d, want the autosave service and timer", len(result.Services))
	}

	service := result.Services[0]
	if !strings.Contains(service.Content, setup.ManagedMarker) {
		t.Fatalf("printed definition is missing the ownership marker:\n%s", service.Content)
	}

	if !strings.Contains(service.Content, "action autosave-save-if-due") {
		t.Fatalf("printed definition does not use the native command:\n%s", service.Content)
	}

	if strings.Contains(service.Content, "continuum") || strings.Contains(service.Content, "resurrect") {
		t.Fatalf("printed definition references legacy names:\n%s", service.Content)
	}

	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("preview wrote into %s: %v", directory, err)
	}
}

func TestSetupAutosaveApplyWritesOwnedDefinitions(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "autosave", "--apply",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.envelope.OK {
		t.Fatalf("setup autosave --apply failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
	if !result.Applied || result.Written != 2 {
		t.Fatalf("result = %+v, want two written definitions", result)
	}

	for _, name := range []string{setup.AutosaveServiceName, setup.AutosaveTimerName} {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("reading %s failed: %v", name, err)
		}

		if !strings.Contains(string(content), setup.ManagedMarker) {
			t.Fatalf("%s is missing the ownership marker:\n%s", name, content)
		}
	}

	again := executeJSON(
		t, "setup", "autosave", "--apply",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !again.envelope.OK {
		t.Fatalf("second apply failed: %s", again.stderr)
	}

	replanned := decodeJSONPayload[setupServicesResultJSON](t, again.envelope.Result)
	if replanned.Written != 0 {
		t.Fatalf("second apply wrote %d definitions, want 0", replanned.Written)
	}

	for _, service := range replanned.Services {
		if service.State != "current" {
			t.Fatalf("service %s state = %q, want current", service.Name, service.State)
		}
	}
}

func TestSetupAutosavePreservesForeignDefinitions(t *testing.T) {
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
		t, "setup", "autosave", "--apply",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.err == nil {
		t.Fatal("a foreign definition must be reported as a degraded outcome")
	}

	if clibase.ExitCode(exec.err) != clibase.ExitCodeGeneral {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.err), clibase.ExitCodeGeneral)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
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

func TestSetupAutosaveDefaultsToUserTmuxServer(t *testing.T) {
	isolatedSetupHome(t)

	exec := executeJSON(
		t, "setup", "autosave", "--with-coordinator",
		"--dir", filepath.Join(t.TempDir(), "units"),
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.envelope.OK {
		t.Fatalf("setup autosave failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	want := "--socket " + clibase.DefaultTmuxSocket() + " "

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
	for _, service := range result.Services {
		if service.Kind == string(setup.KindSystemdTimer) {
			continue
		}

		if !strings.Contains(service.Content, want) {
			t.Fatalf("%s does not target tmux's default server %q:\n%s", service.Name, want, service.Content)
		}

		if strings.Contains(service.Content, setup.TmuxServiceName) {
			t.Fatalf("%s references the uninstalled managed tmux server:\n%s", service.Name, service.Content)
		}
	}
}

func TestSetupAutosaveWithTmuxServiceDefaultsToManagedSocket(t *testing.T) {
	isolatedSetupHome(t)

	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	exec := executeJSON(
		t, "setup", "autosave", "--with-tmux-service", "--tmux", "/usr/bin/tmux",
		"--dir", filepath.Join(t.TempDir(), "units"),
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.envelope.OK {
		t.Fatalf("setup autosave failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	want := "--socket " + filepath.Join(runtimeDir, "sesh", "tmux.sock") + " "

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
	for _, service := range result.Services {
		if service.Kind == string(setup.KindSystemdService) && !strings.Contains(service.Content, want) {
			t.Fatalf("%s does not target the managed server %q:\n%s", service.Name, want, service.Content)
		}
	}
}

func TestSetupAutosaveTmuxServiceRequiresExplicitTmuxPath(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "autosave",
		"--with-tmux-service",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.err == nil {
		t.Fatal("the managed tmux service requires an explicit tmux path")
	}

	if clibase.ExitCode(exec.err) != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.err), clibase.ExitCodeUsage)
	}
}

func TestSetupRemoteWritesRunnableMonitorDefinition(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "remote", "--apply",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.err != nil {
		t.Fatalf("setup remote failed: %v", exec.err)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
	if len(result.Blocked) != 0 {
		t.Fatalf("blocked = %v, want runnable remote monitor definition", result.Blocked)
	}

	if len(result.Services) != 1 || len(result.Services[0].Requires) != 0 {
		t.Fatalf("services = %+v, want no unmet prerequisite", result.Services)
	}

	if !strings.Contains(result.Services[0].Content, "remote monitor run") {
		t.Fatalf("remote definition does not name the native entry point:\n%s", result.Services[0].Content)
	}

	if _, err := os.Stat(filepath.Join(directory, setup.RemoteServiceName)); err != nil {
		t.Fatalf("runnable definition was not written: %v", err)
	}
}

func TestSetupRefreshPlansOwnedDefinitions(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "refresh",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.err != nil {
		t.Fatalf("setup refresh failed: %v", exec.err)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
	if result.Applied {
		t.Fatal("refresh without --apply must not write")
	}

	if len(result.Services) < 3 {
		t.Fatalf("services = %d, want autosave, timer and remote definitions", len(result.Services))
	}
}

func TestSetupAgentsRejectsUnknownNames(t *testing.T) {
	isolatedSetupHome(t)

	exec := executeJSON(t, "setup", "agents", "not-a-real-agent", "--dry-run", testNoConfigFlag, testJSONFlag)
	if exec.err == nil {
		t.Fatal("an unknown agent name must fail")
	}

	if clibase.ExitCode(exec.err) != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.err), clibase.ExitCodeUsage)
	}
}

func TestSetupAgentsRejectsUnknownTrackerMode(t *testing.T) {
	isolatedSetupHome(t)

	exec := executeJSON(t, "setup", "agents", "--tracker", "bogus", "--dry-run", testNoConfigFlag, testJSONFlag)
	if exec.err == nil {
		t.Fatal("an unknown tracker operation must fail")
	}

	if clibase.ExitCode(exec.err) != clibase.ExitCodeUsage {
		t.Fatalf("exit code = %d, want %d", clibase.ExitCode(exec.err), clibase.ExitCodeUsage)
	}
}

func TestSetupAutosaveApplyWritesManagedTmuxService(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")
	tmuxPath := createFakeTmuxExecutable(t)

	exec := executeJSON(
		t, "setup", "autosave", "--apply",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.envelope.OK {
		t.Fatalf("setup autosave --with-tmux-service --apply failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
	if !result.Applied || result.Written != 3 {
		t.Fatalf("result = %+v, want 3 written definitions including tmux service", result)
	}

	assertWrittenManagedUnits(t, directory)

	again := executeJSON(
		t, "setup", "autosave", "--apply",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !again.envelope.OK {
		t.Fatalf("second apply failed: %s", again.stderr)
	}

	replanned := decodeJSONPayload[setupServicesResultJSON](t, again.envelope.Result)
	if replanned.Written != 0 {
		t.Fatalf("second apply wrote %d definitions, want 0", replanned.Written)
	}
}

func createFakeTmuxExecutable(t *testing.T) string {
	t.Helper()

	tmuxPath := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(tmuxPath, 0o700); err != nil {
		t.Fatal(err)
	}

	return tmuxPath
}

func assertWrittenManagedUnits(t *testing.T, directory string) {
	t.Helper()

	for _, name := range []string{setup.AutosaveServiceName, setup.AutosaveTimerName, setup.TmuxServiceName} {
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
		"daemon serve",
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

func TestSetupAutosaveTmuxServicePreservesForeignDefinition(t *testing.T) {
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
		t, "setup", "autosave", "--apply",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.err == nil {
		t.Fatal("a foreign tmux service must be reported as a degraded outcome")
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
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
		t, "setup", "autosave", "--apply", "--force",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !forceExec.envelope.OK {
		t.Fatalf("apply with --force failed: %s", forceExec.stderr)
	}

	replaced, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(replaced), setup.ManagedMarker) {
		t.Fatalf("replaced file missing managed marker:\n%s", replaced)
	}
}

func TestSetupAutosaveTmuxServiceSymlinkProtection(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}

	targetPath := filepath.Join(t.TempDir(), "external-target.service")
	targetContent := []byte("[Unit]\nDescription=external\n")

	if err := os.WriteFile(targetPath, targetContent, 0o600); err != nil {
		t.Fatal(err)
	}

	symlinkPath := filepath.Join(directory, setup.TmuxServiceName)
	if err := os.Symlink(targetPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	tmuxPath := createFakeTmuxExecutable(t)

	exec := executeJSON(
		t, "setup", "autosave", "--apply",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.err == nil {
		t.Fatal("symlink target must be reported as foreign/skipped")
	}

	curTarget, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(curTarget) != string(targetContent) {
		t.Fatalf("symlink target was overwritten through symlink:\n%s", curTarget)
	}
}

func TestSetupRefreshWithTmuxService(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")
	tmuxPath := createFakeTmuxExecutable(t)

	exec := executeJSON(
		t, "setup", "refresh",
		"--with-tmux-service",
		"--tmux", tmuxPath,
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.err != nil {
		t.Fatalf("setup refresh failed: %v", exec.err)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
	if result.Applied {
		t.Fatal("refresh without --apply must not write")
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

func TestSetupCoordinatorPrintsWithoutWriting(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "coordinator",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.envelope.OK {
		t.Fatalf("setup coordinator failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
	if result.Applied {
		t.Fatal("preview must not report an applied result")
	}

	if len(result.Services) != 1 {
		t.Fatalf("services = %d, want the coordinator service", len(result.Services))
	}

	service := result.Services[0]
	if service.Name != setup.CoordinatorServiceName {
		t.Fatalf("name = %q, want %q", service.Name, setup.CoordinatorServiceName)
	}

	if !strings.Contains(service.Content, setup.ManagedMarker) {
		t.Fatalf("printed definition is missing the ownership marker:\n%s", service.Content)
	}

	if !strings.Contains(service.Content, "daemon run") {
		t.Fatalf("printed definition does not use daemon run:\n%s", service.Content)
	}

	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("preview wrote into %s: %v", directory, err)
	}
}

func TestSetupCoordinatorApplyWritesOwnedDefinition(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "coordinator", "--apply",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.envelope.OK {
		t.Fatalf("setup coordinator --apply failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
	if !result.Applied || result.Written != 1 {
		t.Fatalf("result = %+v, want 1 written definition", result)
	}

	content, err := os.ReadFile(filepath.Join(directory, setup.CoordinatorServiceName))
	if err != nil {
		t.Fatalf("reading %s failed: %v", setup.CoordinatorServiceName, err)
	}

	if !strings.Contains(string(content), setup.ManagedMarker) {
		t.Fatalf("missing ownership marker:\n%s", string(content))
	}

	if !strings.Contains(string(content), "daemon run") {
		t.Fatalf("missing daemon run:\n%s", string(content))
	}
}

func TestSetupAutosaveWithCoordinatorApply(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "autosave", "--apply",
		"--with-coordinator",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.envelope.OK {
		t.Fatalf("setup autosave --with-coordinator --apply failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)
	if !result.Applied || result.Written != 3 {
		t.Fatalf("result = %+v, want 3 written definitions", result)
	}

	for _, name := range []string{setup.AutosaveServiceName, setup.AutosaveTimerName, setup.CoordinatorServiceName} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
	}
}

func TestSetupRefreshWithCoordinator(t *testing.T) {
	isolatedSetupHome(t)

	directory := filepath.Join(t.TempDir(), "units")

	exec := executeJSON(
		t, "setup", "refresh",
		"--with-coordinator",
		"--dir", directory,
		testNoConfigFlag, testJSONFlag,
	)
	if exec.err != nil {
		t.Fatalf("setup refresh failed: %v", exec.err)
	}

	result := decodeJSONPayload[setupServicesResultJSON](t, exec.envelope.Result)

	var foundCoordinator bool

	for _, svc := range result.Services {
		if svc.Name == setup.CoordinatorServiceName {
			foundCoordinator = true
		}
	}

	if !foundCoordinator {
		t.Fatalf("services = %+v, want %s included", result.Services, setup.CoordinatorServiceName)
	}
}
