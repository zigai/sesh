package setup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/setup"
)

func definitionsIn(t *testing.T, root string) []setup.ServiceDefinition {
	t.Helper()

	definitions, err := setup.Definitions(linuxInputs(t))
	if err != nil {
		t.Fatalf("Definitions failed: %v", err)
	}

	return setup.Relocate(definitions, root)
}

func TestPlanReportsMissingThenCurrent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	definitions := definitionsIn(t, root)

	plans, err := setup.PlanDefinitions(definitions)
	if err != nil {
		t.Fatalf("PlanDefinitions failed: %v", err)
	}

	for _, plan := range plans {
		assertPlanState(t, plan, setup.ArtifactMissing, true)
	}

	result, err := setup.ApplyDefinitions(definitions, plans, false)
	if err != nil {
		t.Fatalf("ApplyDefinitions failed: %v", err)
	}

	if result.Written != len(definitions) {
		t.Fatalf("written = %d, want %d", result.Written, len(definitions))
	}

	info, err := os.Stat(filepath.Join(root, setup.AutosaveServiceName))
	if err != nil {
		t.Fatalf("stat written definition failed: %v", err)
	}

	if info.Mode().Perm() != 0o644 {
		t.Fatalf("definition mode = %v, want 0644", info.Mode().Perm())
	}

	replanned, err := setup.PlanDefinitions(definitions)
	if err != nil {
		t.Fatalf("PlanDefinitions failed: %v", err)
	}

	for _, plan := range replanned {
		assertPlanState(t, plan, setup.ArtifactCurrent, false)
	}
}

func TestPlanDetectsStaleOwnedDefinition(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	definitions := definitionsIn(t, root)

	plans, err := setup.PlanDefinitions(definitions)
	if err != nil {
		t.Fatalf("PlanDefinitions failed: %v", err)
	}

	if _, err := setup.ApplyDefinitions(definitions, plans, false); err != nil {
		t.Fatalf("ApplyDefinitions failed: %v", err)
	}

	path := filepath.Join(root, setup.AutosaveServiceName)

	writeOwnedDefinition(t, path, "[Unit]\nDescription=old\n")

	replanned, err := setup.PlanDefinitions(definitions)
	if err != nil {
		t.Fatalf("PlanDefinitions failed: %v", err)
	}

	assertPlanState(t, replanned[0], setup.ArtifactStale, true)

	if _, err := setup.ApplyDefinitions(definitions, replanned, false); err != nil {
		t.Fatalf("ApplyDefinitions failed: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(content), "ExecStart=") {
		t.Fatalf("stale definition was not refreshed:\n%s", content)
	}
}

func TestForeignDefinitionIsReportedAndPreserved(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	definitions := definitionsIn(t, root)

	path := filepath.Join(root, setup.AutosaveServiceName)
	foreign := []byte("[Unit]\nDescription=someone else\n")

	if err := os.WriteFile(path, foreign, 0o600); err != nil {
		t.Fatal(err)
	}

	plans, err := setup.PlanDefinitions(definitions)
	if err != nil {
		t.Fatalf("PlanDefinitions failed: %v", err)
	}

	assertPlanState(t, plans[0], setup.ArtifactForeign, false)

	result, err := setup.ApplyDefinitions(definitions, plans, false)
	if err != nil {
		t.Fatalf("ApplyDefinitions failed: %v", err)
	}

	if len(result.SkippedNames) != 1 || result.SkippedNames[0] != setup.AutosaveServiceName {
		t.Fatalf("skipped = %v, want the foreign definition", result.SkippedNames)
	}

	assertFileContent(t, path, foreign)
}

func TestForcedApplyReplacesAForeignDefinition(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	definitions := definitionsIn(t, root)

	path := filepath.Join(root, setup.AutosaveServiceName)

	if err := os.WriteFile(path, []byte("[Unit]\nDescription=someone else\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	forcedPlans, err := setup.PlanDefinitions(definitions)
	if err != nil {
		t.Fatalf("PlanDefinitions failed: %v", err)
	}

	forced, err := setup.ApplyDefinitions(definitions, forcedPlans, true)
	if err != nil {
		t.Fatalf("forced ApplyDefinitions failed: %v", err)
	}

	if forced.Written != 2 {
		t.Fatalf("forced written = %d, want the replaced foreign and missing definitions", forced.Written)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(content), setup.ManagedMarker) {
		t.Fatalf("forced apply did not write the owned definition:\n%s", content)
	}
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(content) != string(want) {
		t.Fatalf("file %s was modified:\n%s", path, content)
	}
}

func TestSymlinkedTargetIsTreatedAsForeign(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	definitions := definitionsIn(t, root)

	target := filepath.Join(root, "outside.conf")
	if err := os.WriteFile(target, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, setup.AutosaveServiceName)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	plans, err := setup.PlanDefinitions(definitions)
	if err != nil {
		t.Fatalf("PlanDefinitions failed: %v", err)
	}

	assertPlanState(t, plans[0], setup.ArtifactForeign, false)

	if _, err := setup.ApplyDefinitions(definitions, plans, false); err != nil {
		t.Fatalf("ApplyDefinitions failed: %v", err)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	if string(content) != "untouched\n" {
		t.Fatalf("symlink target was modified: %q", content)
	}

	if _, err := setup.ApplyDefinitions(definitions, plans, true); err != nil {
		t.Fatalf("forced ApplyDefinitions failed: %v", err)
	}

	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("forced apply did not replace the symlink: %v (%v)", err, info.Mode())
	}
}

func TestBlockedDefinitionIsNeverWrittenEvenWhenForced(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	inputs := linuxInputs(t)
	inputs.WithTmuxService = true
	inputs.RequiresTmuxForeground = true

	definitions, err := setup.Definitions(inputs)
	if err != nil {
		t.Fatalf("Definitions failed: %v", err)
	}

	definitions = setup.Relocate(definitions, root)

	plans, err := setup.PlanDefinitions(definitions)
	if err != nil {
		t.Fatalf("PlanDefinitions failed: %v", err)
	}

	result, err := setup.ApplyDefinitions(definitions, plans, true)
	if err != nil {
		t.Fatalf("ApplyDefinitions failed: %v", err)
	}

	if len(result.BlockedNames) != 2 {
		t.Fatalf("blocked = %v, want the tmux service and the restore service that follows it", result.BlockedNames)
	}

	if _, err := os.Stat(filepath.Join(root, setup.TmuxServiceName)); err == nil {
		t.Fatal("blocked definition must not be written")
	}
}

func TestApplyDefinitionsRejectsMismatchedInputs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	definitions := definitionsIn(t, root)

	if _, err := setup.ApplyDefinitions(definitions, nil, false); err == nil {
		t.Fatal("ApplyDefinitions accepted a mismatched plan list")
	}
}

func assertPlanState(t *testing.T, plan setup.ArtifactPlan, state setup.ArtifactState, changed bool) {
	t.Helper()

	if plan.State != state || plan.Changed != changed {
		t.Fatalf("plan = %+v, want state %q (changed: %t)", plan, state, changed)
	}
}

func writeOwnedDefinition(t *testing.T, path, body string) {
	t.Helper()

	content := "# " + setup.ManagedMarker + "\n" + body
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
