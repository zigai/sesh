//go:build (linux || darwin) && integration

package restore

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

type observedScopeState struct {
	SessionOptions map[string]string
	WindowOptions  map[string]string
	PaneOptions    map[string]string
	Environment    map[environmentKey]environmentShape
}

func readScopeState(t *testing.T, ctx context.Context, session tmux.Session) observedScopeState {
	t.Helper()

	state := observedScopeState{
		SessionOptions: readLocalOptions(t, ctx, session.Options()),
		WindowOptions:  map[string]string{},
		PaneOptions:    map[string]string{},
		Environment:    map[environmentKey]environmentShape{},
	}

	environment, err := environmentTable(ctx, session.Environment())
	if err != nil {
		t.Fatalf("environmentTable failed: %v", err)
	}

	state.Environment = environment

	_, links, err := session.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("WindowInfos failed: %v", err)
	}

	for _, link := range links {
		window := link.Handle().Window()

		maps.Copy(state.WindowOptions, readLocalOptions(t, ctx, window.Options()))

		panes, paneErr := window.Panes(ctx)
		if paneErr != nil {
			t.Fatalf("Panes failed: %v", paneErr)
		}

		for _, pane := range panes {
			maps.Copy(state.PaneOptions, readLocalOptions(t, ctx, pane.Handle().Options()))
		}
	}

	return state
}

func readLocalOptions(t *testing.T, ctx context.Context, scope optionScope) map[string]string {
	t.Helper()

	entries, err := scope.List(ctx)
	if err != nil {
		t.Fatalf("option listing failed: %v", err)
	}

	values := make(map[string]string, len(entries))

	for _, entry := range entries {
		if value, present := entry.Value.Get(); present {
			values[entry.Name] = value
		}
	}

	return values
}

func TestExecutorImportsScopedStateFaithfully(t *testing.T) {
	sourceServer := tmuxtest.NewServer(t)
	targetServer := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	source, sourceState := setupScopedStateSourceSession(t, ctx, sourceServer)

	observation, err := capture.Observe(ctx, sourceServer, []tmux.SessionID{source.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitObservationToRepo(t, ctx, repoDir, observation)

	plan, err := BuildPlan(observation.Manifest, []string{sessionIDByName(t, observation.Manifest, "state-source")})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	defer func() { _ = repo.Close() }()

	operation := registerPlanOperation(t, ctx, repo, "scoped-state", generation, targetServer.Endpoint().SocketPath, plan)

	report, err := NewExecutor(targetServer, observation.Manifest, DefaultExecuteConfig()).
		Execute(ctx, plan, newPlanJournal(t, ctx, repo, operation))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	importReport, err := ImportScopedState(ctx, targetServer, plan, report.Receipts)
	if err != nil {
		t.Fatalf("ImportScopedState failed: %v", err)
	}

	if importReport.Sessions != 1 || importReport.Options == 0 || importReport.Environment == 0 {
		t.Fatalf("expected scoped state to be applied, got %+v", importReport)
	}

	target, err := targetServer.FindSession(ctx, "state-source")
	if err != nil {
		t.Fatalf("FindSession failed: %v", err)
	}

	targetState := readScopeState(t, ctx, target)

	assertTargetScopedOptionsAndEnv(t, sourceState, targetState)

	verification, err := Verify(ctx, targetServer, plan, report.Receipts)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !verification.Verified {
		t.Fatalf("independent verification failed: %+v", verification.Mismatches)
	}
}

func TestExecutorNeverMutatesGlobalScopes(t *testing.T) {
	sourceServer := tmuxtest.NewServer(t)
	targetServer := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	if err := sourceServer.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatalf("setting source global option failed: %v", err)
	}

	source, err := sourceServer.NewSession(ctx, tmux.NewSessionOptions{Name: "global-source", Program: tmux.Exec("sleep", "300"), Start: tmux.ExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableAutomaticRename(t, ctx, source)

	observation, err := capture.Observe(ctx, sourceServer, []tmux.SessionID{source.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	targetBefore, err := targetServer.GlobalSessionOptions().Get(ctx, "status")
	if err != nil {
		t.Fatalf("reading target global option failed: %v", err)
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitObservationToRepo(t, ctx, repoDir, observation)

	plan, err := BuildPlan(observation.Manifest, []string{sessionIDByName(t, observation.Manifest, "global-source")})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	defer func() { _ = repo.Close() }()

	operation := registerPlanOperation(t, ctx, repo, "global-scope", generation, targetServer.Endpoint().SocketPath, plan)

	report, err := NewExecutor(targetServer, observation.Manifest, DefaultExecuteConfig()).
		Execute(ctx, plan, newPlanJournal(t, ctx, repo, operation))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	importReport, err := ImportScopedState(ctx, targetServer, plan, report.Receipts)
	if err != nil {
		t.Fatalf("ImportScopedState failed: %v", err)
	}

	if !hasPartialRequirement(importReport.Missing, "options") {
		t.Fatalf("expected a partial global option classification, got %+v", importReport.Missing)
	}

	targetAfter, err := targetServer.GlobalSessionOptions().Get(ctx, "status")
	if err != nil {
		t.Fatalf("reading target global option failed: %v", err)
	}

	if targetBefore.Local != targetAfter.Local {
		t.Fatalf("global scope must not be mutated: before %+v after %+v", targetBefore, targetAfter)
	}
}

func TestInheritedDefaultsAreMaterializedOnOwnedScopes(t *testing.T) {
	sourceServer := tmuxtest.NewServer(t)
	targetServer := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	observation := observeInheritedSource(t, ctx, sourceServer)
	unrelated := setupInheritedTarget(t, ctx, targetServer)
	plan, receipts := executeInheritedRestore(t, ctx, targetServer, observation)
	restored := findSession(t, ctx, targetServer, "inherited-source")

	assertInheritedRestoreState(t, ctx, targetServer, restored, unrelated)
	assertChildEnvironment(t, ctx, restored, inheritedChildEnvironment)

	if err := targetServer.GlobalSessionOptions().Set(ctx, "status-left", "target-drift"); err != nil {
		t.Fatalf("drifting target global option failed: %v", err)
	}

	if err := targetServer.Environment().Set(ctx, "SESH_GLOBAL", "target-drift"); err != nil {
		t.Fatalf("drifting target global environment failed: %v", err)
	}

	assertOptionValue(t, ctx, restored.Options(), "status-left", "source", true)
	assertChildEnvironment(t, ctx, restored, inheritedChildEnvironment)

	mismatches, err := ObserveScopedStateConflicts(ctx, targetServer, plan, receipts)
	if err != nil {
		t.Fatalf("ObserveScopedStateConflicts failed: %v", err)
	}

	if len(mismatches) != 0 {
		t.Fatalf("materialized state changed after target global drift: %+v", mismatches)
	}
}

func windowPaneIndexes(t *testing.T, ctx context.Context, window tmux.Window) []int {
	t.Helper()

	panes, err := window.Panes(ctx)
	if err != nil {
		t.Fatalf("Panes failed: %v", err)
	}

	indexes := make([]int, len(panes))
	for index, pane := range panes {
		indexes[index] = pane.Index
	}

	slices.Sort(indexes)

	return indexes
}

func requirePaneIndexes(t *testing.T, got, want []int) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("pane indexes = %v, want %v", got, want)
	}
}

func createTwoPaneSession(
	t *testing.T,
	ctx context.Context,
	server *tmux.Server,
	name string,
) (tmux.Session, tmux.Window) {
	t.Helper()

	session, err := server.NewSession(ctx, tmux.NewSessionOptions{
		Name: name, Program: tmux.Exec("sleep", "300"), Start: tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatalf("creating %s failed: %v", name, err)
	}

	disableAutomaticRename(t, ctx, session)

	_, links, err := session.WindowInfos(ctx)
	if err != nil || len(links) != 1 {
		t.Fatalf("%s WindowInfos failed: %v, links=%d", name, err, len(links))
	}

	window := links[0].Handle().Window()

	pane, err := window.ActivePane(ctx)
	if err != nil {
		t.Fatalf("%s ActivePane failed: %v", name, err)
	}

	if _, err := pane.Split(ctx, tmux.SplitOptions{Program: tmux.Exec("sleep", "300")}); err != nil {
		t.Fatalf("%s Split failed: %v", name, err)
	}

	return session, window
}

func restorePaneIndexPlan(
	t *testing.T,
	ctx context.Context,
	server *tmux.Server,
	observation capture.Observation,
	plan Plan,
) (ExecutionReport, tmux.Window) {
	t.Helper()

	report, err := NewExecutor(server, observation.Manifest, DefaultExecuteConfig()).Execute(ctx, plan, newMemoryJournal())
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if _, err := ImportScopedState(ctx, server, plan, report.Receipts); err != nil {
		t.Fatalf("ImportScopedState failed: %v", err)
	}

	identities, err := ResolveRuntimeIdentities(ctx, server, plan, report.Receipts)
	if err != nil {
		t.Fatalf("ResolveRuntimeIdentities failed: %v", err)
	}

	window, err := server.Window(ctx, identities.Windows[observation.Manifest.Windows[0].ID])
	if err != nil {
		t.Fatalf("resolving restored window failed: %v", err)
	}

	return report, window
}

func assertPaneIndexVerification(t *testing.T, ctx context.Context, server *tmux.Server, plan Plan, receipts map[string]string) {
	t.Helper()

	verified, err := Verify(ctx, server, plan, receipts)
	if err != nil || !verified.Verified {
		t.Fatalf("exact pane indexes did not verify: %+v, %v", verified, err)
	}

	wrongPlan := plan
	wrongPlan.SourceGraph.Panes = slices.Clone(plan.SourceGraph.Panes)
	wrongPlan.SourceGraph.Panes[0].Index += 10

	mismatch, err := Verify(ctx, server, wrongPlan, receipts)
	if err != nil {
		t.Fatalf("Verify wrong pane index failed: %v", err)
	}

	if !slices.ContainsFunc(mismatch.Mismatches, func(entry VerificationMismatch) bool {
		return entry.Domain == MismatchPane && entry.Reason == "pane_index_mismatch"
	}) {
		t.Fatalf("wrong pane index was not reported: %+v", mismatch.Mismatches)
	}
}

func TestPaneIndexesRestoreAcrossTargetDefaults(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	sourceServer := tmuxtest.NewServer(t)
	if err := sourceServer.GlobalWindowOptions().Set(ctx, "pane-base-index", "7"); err != nil {
		t.Fatalf("setting source pane base index failed: %v", err)
	}

	source, sourceWindow := createTwoPaneSession(t, ctx, sourceServer, "pane-index-source")
	requirePaneIndexes(t, windowPaneIndexes(t, ctx, sourceWindow), []int{7, 8})

	observation, err := capture.Observe(ctx, sourceServer, []tmux.SessionID{source.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	plan, err := BuildPlan(observation.Manifest, []string{sessionIDByName(t, observation.Manifest, "pane-index-source")})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	targetServer := tmuxtest.NewServer(t)
	if err := targetServer.GlobalWindowOptions().Set(ctx, "pane-base-index", "2"); err != nil {
		t.Fatalf("setting target pane base index failed: %v", err)
	}

	_, unrelatedWindow := createTwoPaneSession(t, ctx, targetServer, "pane-index-unrelated")
	requirePaneIndexes(t, windowPaneIndexes(t, ctx, unrelatedWindow), []int{2, 3})

	report, restoredWindow := restorePaneIndexPlan(t, ctx, targetServer, observation, plan)
	requirePaneIndexes(t, windowPaneIndexes(t, ctx, restoredWindow), []int{7, 8})
	requirePaneIndexes(t, windowPaneIndexes(t, ctx, unrelatedWindow), []int{2, 3})
	assertOptionValue(t, ctx, targetServer.GlobalWindowOptions(), "pane-base-index", "2", true)

	assertPaneIndexVerification(t, ctx, targetServer, plan, report.Receipts)
}

const inheritedChildEnvironment = "source-global\nsource-local\n\n\n\nunset\nunset\n" //nolint:dupword // reason: both hidden variables must independently report unset in child output

func observeInheritedSource(t *testing.T, ctx context.Context, server *tmux.Server) capture.Observation {
	t.Helper()
	setInheritedFixture(t, ctx, server, "source")

	source, err := server.NewSession(ctx, tmux.NewSessionOptions{
		Name: "inherited-source", Program: tmux.Exec("sleep", "300"), Start: tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableAutomaticRename(t, ctx, source)

	if err := source.Environment().Set(ctx, "SESH_OVERRIDE", "source-local"); err != nil {
		t.Fatalf("setting source session environment failed: %v", err)
	}

	if err := source.Environment().Remove(ctx, "SESH_REMOVED"); err != nil {
		t.Fatalf("removing source session environment failed: %v", err)
	}

	observation, err := capture.Observe(ctx, server, []tmux.SessionID{source.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	return observation
}

func setupInheritedTarget(t *testing.T, ctx context.Context, server *tmux.Server) tmux.Session {
	t.Helper()
	setInheritedFixture(t, ctx, server, "target")

	if err := server.Environment().Set(ctx, "SESH_TARGET_ONLY", "target-only"); err != nil {
		t.Fatalf("setting target-only environment failed: %v", err)
	}

	if err := server.Environment().SetHidden(ctx, "SESH_TARGET_ONLY_HIDDEN", "target-hidden"); err != nil {
		t.Fatalf("setting hidden target-only environment failed: %v", err)
	}

	unrelated, err := server.NewSession(ctx, tmux.NewSessionOptions{
		Name: "unrelated-inherited", Program: tmux.Exec("sleep", "300"), Start: tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatalf("creating unrelated session failed: %v", err)
	}

	return unrelated
}

func executeInheritedRestore(
	t *testing.T,
	ctx context.Context,
	server *tmux.Server,
	observation capture.Observation,
) (Plan, map[string]string) {
	t.Helper()

	plan, err := BuildPlan(observation.Manifest, []string{sessionIDByName(t, observation.Manifest, "inherited-source")})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitObservationToRepo(t, ctx, repoDir, observation)

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	t.Cleanup(func() { _ = repo.Close() })

	operation := registerPlanOperation(t, ctx, repo, "inherited-defaults", generation, server.Endpoint().SocketPath, plan)

	report, err := NewExecutor(server, observation.Manifest, DefaultExecuteConfig()).
		Execute(ctx, plan, newPlanJournal(t, ctx, repo, operation))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	importReport, err := ImportScopedState(ctx, server, plan, report.Receipts)
	if err != nil {
		t.Fatalf("ImportScopedState failed: %v", err)
	}

	if !hasPartialRequirement(importReport.Missing, "options") ||
		!hasPartialRequirement(importReport.Missing, "environment") {
		t.Fatalf("materialized inheritance must remain explicitly partial: %+v", importReport.Missing)
	}

	return plan, report.Receipts
}

func findSession(t *testing.T, ctx context.Context, server *tmux.Server, name string) tmux.Session {
	t.Helper()

	session, err := server.FindSession(ctx, name)
	if err != nil {
		t.Fatalf("FindSession failed: %v", err)
	}

	return session
}

func assertInheritedRestoreState(
	t *testing.T,
	ctx context.Context,
	server *tmux.Server,
	restored,
	unrelated tmux.Session,
) {
	t.Helper()
	assertOptionValue(t, ctx, restored.Options(), "status-left", "source", true)
	assertOptionValue(t, ctx, restored.Options(), "status-right", "", true)
	assertOptionValue(t, ctx, restored.Options(), "status-format[9]", "source-array", true)

	_, restoredLinks, err := restored.WindowInfos(ctx)
	if err != nil || len(restoredLinks) != 1 {
		t.Fatalf("WindowInfos failed: %v %v", restoredLinks, err)
	}

	assertOptionValue(t, ctx, restoredLinks[0].Handle().Window().Options(), "window-style", "fg=red", true)
	assertOptionValue(t, ctx, unrelated.Options(), "status-left", "target", false)

	targetGlobal, err := server.GlobalSessionOptions().Get(ctx, "status-left")

	targetGlobalValue, targetGlobalPresent := targetGlobal.Effective.Get()
	if err != nil || !targetGlobalPresent || targetGlobalValue != "target" {
		t.Fatalf("target global option changed: %+v %v", targetGlobal, err)
	}

	targetOnly, err := restored.Environment().Get(ctx, "SESH_TARGET_ONLY", false)
	if err != nil || !targetOnly.Unset {
		t.Fatalf("target-only global environment was not locally masked: %+v %v", targetOnly, err)
	}

	hidden, err := restored.Environment().Get(ctx, "SESH_HIDDEN", true)

	hiddenValue, hiddenPresent := hidden.Value.Get()
	if err != nil || !hiddenPresent || hiddenValue != "source-hidden" || hidden.Unset {
		t.Fatalf("hidden source environment mismatch: %+v %v", hidden, err)
	}
}

func TestEnvironmentReinspectionPreservesMultilineValues(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	t.Run("global", func(t *testing.T) {
		server := tmuxtest.NewServer(t)
		if err := server.Environment().Set(ctx, "SESH_MULTILINE", "first\nsecond"); err != nil {
			t.Fatalf("setting multiline global environment failed: %v", err)
		}

		table, err := environmentTable(ctx, server.Environment())
		if err != nil {
			t.Fatalf("reading multiline global environment failed: %v", err)
		}

		if got := table[environmentKey{name: "SESH_MULTILINE", hidden: false}]; got.value != "first\nsecond" || got.unset {
			t.Fatalf("multiline global environment was truncated: %+v", got)
		}

		if _, fabricated := table[environmentKey{name: "second", hidden: false}]; fabricated {
			t.Fatal("multiline global environment fabricated a continuation entry")
		}
	})

	t.Run("session", func(t *testing.T) {
		server := tmuxtest.NewServer(t)

		session, err := server.NewSession(ctx, tmux.NewSessionOptions{
			Name: "multiline-session", Program: tmux.Exec("sleep", "300"), Start: tmux.ExistingOnly,
		})
		if err != nil {
			t.Fatalf("NewSession failed: %v", err)
		}

		if err := session.Environment().Set(ctx, "SESH_MULTILINE", "first\nsecond"); err != nil {
			t.Fatalf("setting multiline session environment failed: %v", err)
		}

		table, err := environmentTable(ctx, session.Environment())
		if err != nil {
			t.Fatalf("reading multiline session environment failed: %v", err)
		}

		if got := table[environmentKey{name: "SESH_MULTILINE", hidden: false}]; got.value != "first\nsecond" || got.unset {
			t.Fatalf("multiline session environment was truncated: %+v", got)
		}

		if _, fabricated := table[environmentKey{name: "second", hidden: false}]; fabricated {
			t.Fatal("multiline session environment fabricated a continuation entry")
		}
	})
}

func setInheritedFixture(t *testing.T, ctx context.Context, server *tmux.Server, value string) {
	t.Helper()

	fixture := inheritedFixtureValuesFor(value)
	setInheritedOptions(t, ctx, server, fixture)
	setInheritedEnvironment(t, ctx, server, fixture)
}

type inheritedFixtureValues struct {
	statusLeft  string
	statusRight string
	array       string
	windowStyle string
	global      string
	override    string
	removed     string
	empty       string
	hidden      string
}

func inheritedFixtureValuesFor(value string) inheritedFixtureValues {
	if value == "source" {
		return inheritedFixtureValues{
			statusLeft: value, statusRight: "", array: "source-array", windowStyle: "fg=red",
			global: "source-global", override: "source-global", removed: value, empty: "", hidden: "source-hidden",
		}
	}

	return inheritedFixtureValues{
		statusLeft: value, statusRight: "target-right", array: "target-array", windowStyle: "fg=blue",
		global: value + "-global", override: value + "-global", removed: value, empty: "target-nonempty", hidden: "target-hidden",
	}
}

func setInheritedOptions(t *testing.T, ctx context.Context, server *tmux.Server, fixture inheritedFixtureValues) {
	t.Helper()

	if err := server.GlobalSessionOptions().Set(ctx, "status-left", fixture.statusLeft); err != nil {
		t.Fatalf("setting global session option failed: %v", err)
	}

	if err := server.GlobalSessionOptions().Set(ctx, "status-right", fixture.statusRight); err != nil {
		t.Fatalf("setting empty-capable global option failed: %v", err)
	}

	if err := server.GlobalSessionOptions().Set(ctx, "status-format[9]", fixture.array); err != nil {
		t.Fatalf("setting global array option failed: %v", err)
	}

	if err := server.GlobalWindowOptions().Set(ctx, "window-style", fixture.windowStyle); err != nil {
		t.Fatalf("setting global window option failed: %v", err)
	}
}

func setInheritedEnvironment(t *testing.T, ctx context.Context, server *tmux.Server, fixture inheritedFixtureValues) {
	t.Helper()

	for name, content := range map[string]string{
		"SESH_GLOBAL": fixture.global, "SESH_OVERRIDE": fixture.override, "SESH_REMOVED": fixture.removed,
		"SESH_EMPTY": fixture.empty,
	} {
		if err := server.Environment().Set(ctx, name, content); err != nil {
			t.Fatalf("setting global environment failed: %v", err)
		}
	}

	if err := server.Environment().SetHidden(ctx, "SESH_HIDDEN", fixture.hidden); err != nil {
		t.Fatalf("setting hidden global environment failed: %v", err)
	}
}

func assertOptionValue(t *testing.T, ctx context.Context, scope optionScope, name, want string, local bool) {
	t.Helper()

	entries, err := scope.ListWith(ctx, tmux.ListOptionOptions{Inherited: true})
	if err != nil {
		t.Fatalf("listing options failed: %v", err)
	}

	for _, entry := range entries {
		value, present := entry.Value.Get()
		if entry.Name == name && present {
			if value != want || entry.Inherited == local {
				t.Fatalf("option %q mismatch: value %q inherited %v", name, value, entry.Inherited)
			}

			return
		}
	}

	t.Fatalf("option %q missing", name)
}

func assertChildEnvironment(t *testing.T, ctx context.Context, session tmux.Session, want string) {
	t.Helper()
	output := filepath.Join(t.TempDir(), "child-env")

	_, err := session.NewWindow(ctx, tmux.NewWindowOptions{
		Name:    "environment-probe",
		Program: tmux.Exec("sh", "-c", `printf '%s\n%s\n%s\n%s\n%s\n%s\n%s\n' "$SESH_GLOBAL" "$SESH_OVERRIDE" "${SESH_REMOVED-}" "$SESH_EMPTY" "${SESH_TARGET_ONLY-}" "${SESH_HIDDEN-unset}" "${SESH_TARGET_ONLY_HIDDEN-unset}" > "$1"`, "sh", output),
	})
	if err != nil {
		t.Fatalf("creating environment probe failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)

	var last []byte

	for {
		content, readErr := os.ReadFile(output)
		if readErr == nil {
			last = content
			if string(content) == want {
				break
			}
		}

		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			t.Fatalf("reading child environment failed: %v", readErr)
		}

		if time.Now().After(deadline) {
			t.Fatalf("child environment mismatch: got %q want %q", last, want)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

func sessionIDByName(t *testing.T, manifest model.Manifest, name string) string {
	t.Helper()

	for _, session := range manifest.Sessions {
		if string(session.Name) == name {
			return session.ID
		}
	}

	t.Fatalf("session %q not found in manifest", name)

	return ""
}

func disableAutomaticRename(t *testing.T, ctx context.Context, session tmux.Session) {
	t.Helper()

	_, links, err := session.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("WindowInfos failed: %v", err)
	}

	for _, link := range links {
		options := link.Handle().Window().Options()

		if err := options.SetAutomaticRename(ctx, false); err != nil {
			t.Fatalf("disabling automatic rename failed: %v", err)
		}

		if err := options.SetAllowRename(ctx, false); err != nil {
			t.Fatalf("disabling window rename failed: %v", err)
		}
	}
}

func hasPartialRequirement(requirements []MissingRequirement, domain string) bool {
	for _, requirement := range requirements {
		if requirement.Domain == domain && requirement.Status == "partial" {
			return true
		}
	}

	return false
}

func TestExecutorLaunchesApprovedProgramOnceWithModeledEnvironment(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	marker := filepath.Join(t.TempDir(), "launched.txt")

	if err := server.Environment().Set(ctx, "SESH_LAUNCH_REMOVE", "target-value"); err != nil {
		t.Fatalf("set target launch environment: %v", err)
	}

	manifest := newIntegrationManifest()
	manifest.Source.Environment = []model.Variable{
		{Name: model.Bytes("SESH_LAUNCH_GLOBAL"), State: "set", Value: model.Bytes("global")},
		{Name: model.Bytes("SESH_LAUNCH_OVERRIDE"), State: "set", Value: model.Bytes("global")},
	}
	manifest.Sessions[0].Environment = []model.Variable{
		{Name: model.Bytes("SESH_LAUNCH_SESSION"), State: "set", Value: model.Bytes("session")},
		{Name: model.Bytes("SESH_LAUNCH_OVERRIDE"), State: "set", Value: model.Bytes("session")},
		{Name: model.Bytes("SESH_LAUNCH_REMOVE"), State: "removed"},
	}
	manifest.Panes[0].Launch = model.Launch{
		Executable: model.Bytes("/bin/sh"),
		Arguments: []model.Bytes{
			model.Bytes("sh"),
			model.Bytes("-c"),
			model.Bytes(`printf '%s|%s|%s|%s|%s|%s|%s\n' "$SESH_LAUNCH_GLOBAL" "$SESH_LAUNCH_SESSION" "$SESH_LAUNCH_OVERRIDE" "$SESH_LAUNCH_SET" "${SESH_LAUNCH_REMOVE-unset}" "$TMUX" "$TMUX_PANE" >> "$1"; exec sleep 300`),
			model.Bytes("sh"),
			model.Bytes(marker),
		},
		Directory: model.Bytes("/tmp"),
		Environment: []model.Variable{
			{Name: model.Bytes("SESH_LAUNCH_SET"), State: "set", Value: model.Bytes("approved")},
			{Name: model.Bytes("SESH_LAUNCH_OVERRIDE"), State: "set", Value: model.Bytes("process")},
			{Name: model.Bytes("TMUX"), State: "set", Value: model.Bytes("source-tmux")},
			{Name: model.Bytes("TMUX_PANE"), State: "set", Value: model.Bytes("source-pane")},
		},
		Birth:    nil,
		Recovery: "restart",
	}

	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest validation failed: %v", err)
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitManifest(t, ctx, repoDir, manifest)

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	defer func() { _ = repo.Close() }()

	operation := registerPlanOperation(t, ctx, repo, "approved-launch", generation, server.Endpoint().SocketPath, plan)

	cfg := DefaultExecuteConfig()
	cfg.LaunchProcesses = true

	journal := newPlanJournal(t, ctx, repo, operation)

	report, err := NewExecutor(server, manifest, cfg).Execute(ctx, plan, journal)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if hasPartialRequirement(report.Missing, "launch") {
		t.Fatalf("approved launch unexpectedly degraded: %+v", report.Missing)
	}

	first := waitForLaunchMarker(t, ctx, marker)
	assertLaunchMarker(t, first)

	adopted, err := NewExecutor(server, manifest, cfg).Execute(ctx, plan, journal)
	if err != nil {
		t.Fatalf("adopt confirmed execution: %v", err)
	}

	if len(adopted.Dispatched) != 0 || len(adopted.Adopted) != len(plan.Steps) {
		t.Fatalf("confirmed execution was not fully adopted: %+v", adopted)
	}

	if after := waitForLaunchMarker(t, ctx, marker); after != first {
		t.Fatalf("confirmed adoption relaunched program: before %q after %q", first, after)
	}
}

func TestExecutorUnavailableApprovedLaunchPreservesPaneAsPartial(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	manifest := newIntegrationManifest()
	manifest.Panes[0].Launch = model.Launch{
		Executable: model.Bytes(filepath.Join(t.TempDir(), "missing")),
		Arguments:  []model.Bytes{model.Bytes("missing")},
		Directory:  model.Bytes("/tmp"),
		Recovery:   "restart",
	}

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	report, err := NewExecutor(server, manifest, ExecuteConfig{LaunchProcesses: true}).Execute(ctx, plan, newMemoryJournal())
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !hasPartialRequirement(report.Missing, "launch") {
		t.Fatalf("unavailable approved launch was not reported partial: %+v", report.Missing)
	}

	if !slices.Contains(report.Missing, MissingRequirement{Domain: "launch", Status: "partial", Reason: "approved_launch_unavailable"}) {
		t.Fatalf("unavailable approved launch used an unexpected reason: %+v", report.Missing)
	}

	if len(report.SessionsCreated) != 1 {
		t.Fatalf("structural session was not preserved: %+v", report)
	}

	if _, err := server.FindSession(ctx, restoredSessionName); err != nil {
		t.Fatalf("structural session is not live after skipped launch: %v", err)
	}
}

func assertLaunchMarker(t *testing.T, marker string) {
	t.Helper()

	fields := strings.Split(strings.TrimSpace(marker), "|")
	if len(fields) != 7 {
		t.Fatalf("unexpected launch marker shape: %q", marker)
	}

	if fields[0] != "global" || fields[1] != "session" || fields[2] != "process" || fields[3] != "approved" || fields[4] != "unset" {
		t.Fatalf("modeled launch environment not applied: %q", marker)
	}

	if fields[5] == "" || fields[5] == "source-tmux" || fields[6] == "" || fields[6] == "source-pane" {
		t.Fatalf("source tmux identities leaked into launched process: %q", marker)
	}
}

func TestExecutorReportsUnsupportedCapturedApplication(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	manifest := newIntegrationManifest()
	manifest.Panes[0].Launch = model.Launch{
		Executable: model.Bytes("/usr/bin/unsupported-application"),
		Arguments:  []model.Bytes{model.Bytes("unsupported-application")},
		Directory:  model.Bytes("/tmp"),
		Recovery:   "unverified",
	}

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	report, err := NewExecutor(server, manifest, ExecuteConfig{LaunchProcesses: true}).Execute(ctx, plan, newMemoryJournal())
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	want := MissingRequirement{Domain: "launch", Status: "partial", Reason: "application_restart_unsupported"}
	if !slices.Contains(report.Missing, want) {
		t.Fatalf("unsupported captured application was silent: %+v", report.Missing)
	}

	if _, err := server.FindSession(ctx, restoredSessionName); err != nil {
		t.Fatalf("unsupported application damaged structural restore: %v", err)
	}
}

func TestExecutorLaunchesApprovedProgramInSplitPane(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	marker := filepath.Join(t.TempDir(), "split-launched.txt")
	manifest := nestedLayoutFixture()
	manifest.Panes[1].Launch = model.Launch{
		Executable: model.Bytes("/bin/sh"),
		Arguments: []model.Bytes{
			model.Bytes("sh"), model.Bytes("-c"),
			model.Bytes(`printf split > "$1"; exec sleep 300`), model.Bytes("sh"), model.Bytes(marker),
		},
		Directory: model.Bytes("/tmp"),
		Recovery:  "resume",
	}

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	report, err := NewExecutor(server, manifest, ExecuteConfig{LaunchProcesses: true}).Execute(ctx, plan, newMemoryJournal())
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if hasPartialRequirement(report.Missing, "launch") {
		t.Fatalf("approved split-pane launch unexpectedly degraded: %+v", report.Missing)
	}

	if markerContent := waitForLaunchMarker(t, ctx, marker); markerContent != "split" {
		t.Fatalf("split-pane launch marker: %q", markerContent)
	}
}

func waitForLaunchMarker(t *testing.T, ctx context.Context, path string) string {
	t.Helper()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data)
		}

		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read launch marker: %v", err)
		}

		select {
		case <-ctx.Done():
			t.Fatalf("wait for launch marker: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestExecutorCompensationRequestRetainsCreatedAndUnrelatedSessions(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	unrelated, unrelatedLinks, unrelatedPaneInfo := setupUnrelatedLiveSession(t, ctx, server)

	manifest := newIntegrationManifest()
	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitManifest(t, ctx, repoDir, manifest)

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	defer func() { _ = repo.Close() }()

	operation := registerPlanOperation(t, ctx, repo, "compensation-unrelated", generation, server.Endpoint().SocketPath, plan)

	failing := plan.Steps[len(plan.Steps)-1]

	journal := newFaultJournal(newPlanJournal(t, ctx, repo, operation))
	journal.failIntent[failing.ID] = errInjectedIntentFault

	cfg := DefaultExecuteConfig()
	cfg.CompensateOnFailure = true

	report, err := NewExecutor(server, manifest, cfg).Execute(ctx, plan, journal)
	if err == nil {
		t.Fatal("expected execution failure")
	}

	if !errors.Is(err, ErrCompensationUnavailable) {
		t.Fatalf("expected ErrCompensationUnavailable, got %v", err)
	}

	if len(report.Compensation) != 1 || report.Compensation[0].Status != CompensationRetained {
		t.Fatalf("expected one retained compensation entry, got %+v", report.Compensation)
	}

	if _, err := server.FindSession(ctx, restoredSessionName); err != nil {
		t.Fatalf("created session must be retained when cleanup is unsafe: %v", err)
	}

	assertUnrelatedLiveSessionIntact(t, ctx, server, unrelated, unrelatedLinks, unrelatedPaneInfo)
}

func TestExecutorCompensationDisabledKeepsConfirmedObjects(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	manifest := newIntegrationManifest()
	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitManifest(t, ctx, repoDir, manifest)

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	defer func() { _ = repo.Close() }()

	operation := registerPlanOperation(t, ctx, repo, "compensation-disabled", generation, server.Endpoint().SocketPath, plan)

	failing := plan.Steps[len(plan.Steps)-1]

	journal := newFaultJournal(newPlanJournal(t, ctx, repo, operation))
	journal.failIntent[failing.ID] = errInjectedIntentFault

	cfg := DefaultExecuteConfig()
	cfg.CompensateOnFailure = false

	report, err := NewExecutor(server, manifest, cfg).Execute(ctx, plan, journal)
	if err == nil {
		t.Fatal("expected execution failure")
	}

	if len(report.Compensation) != 1 || report.Compensation[0].Status != CompensationNotAttempted {
		t.Fatalf("expected an unattempted compensation entry, got %+v", report.Compensation)
	}

	if _, err := server.FindSession(ctx, restoredSessionName); err != nil {
		t.Fatalf("confirmed session must stay without compensation: %v", err)
	}
}

func TestExecutorWorkingDirFallbackIsExplicitlyPartial(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	manifest := newIntegrationManifest()
	manifest.Panes[0].Directory = model.Bytes(filepath.Join(t.TempDir(), "missing-dir"))

	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest validation failed: %v", err)
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitManifest(t, ctx, repoDir, manifest)

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	defer func() { _ = repo.Close() }()

	operation := registerPlanOperation(t, ctx, repo, "directory-fallback", generation, server.Endpoint().SocketPath, plan)

	cfg := DefaultExecuteConfig()
	cfg.AllowMissingWorkingDir = true

	report, err := NewExecutor(server, manifest, cfg).Execute(ctx, plan, newPlanJournal(t, ctx, repo, operation))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !hasPartialRequirement(report.Missing, "directory") {
		t.Fatalf("expected an explicit partial directory classification, got %+v", report.Missing)
	}
}

func setupScopedStateSourceSession(t *testing.T, ctx context.Context, server *tmux.Server) (tmux.Session, observedScopeState) {
	t.Helper()

	source, err := server.NewSession(ctx, tmux.NewSessionOptions{Name: "state-source", Program: tmux.Exec("sleep", "300"), Start: tmux.ExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableAutomaticRename(t, ctx, source)

	if err := source.Options().Set(ctx, "@sesh-flag", "session-value"); err != nil {
		t.Fatalf("setting session option failed: %v", err)
	}

	if err := source.Options().Set(ctx, "status-style", "fg=red"); err != nil {
		t.Fatalf("setting session style failed: %v", err)
	}

	if err := source.Environment().Set(ctx, "SESH_SCOPED_VAR", "scoped-value"); err != nil {
		t.Fatalf("setting session environment failed: %v", err)
	}

	_, links, err := source.WindowInfos(ctx)
	if err != nil || len(links) != 1 {
		t.Fatalf("WindowInfos failed: %v %v", links, err)
	}

	window := links[0].Handle().Window()
	if err := window.Options().Set(ctx, "@sesh-window", "window-value"); err != nil {
		t.Fatalf("setting window option failed: %v", err)
	}

	pane, err := window.ActivePane(ctx)
	if err != nil {
		t.Fatalf("ActivePane failed: %v", err)
	}

	if err := pane.Options().Set(ctx, "@sesh-pane", "pane-value"); err != nil {
		t.Fatalf("setting pane option failed: %v", err)
	}

	return source, readScopeState(t, ctx, source)
}

func assertTargetScopedOptionsAndEnv(t *testing.T, sourceState, targetState observedScopeState) {
	t.Helper()

	scopes := map[string][2]map[string]string{
		"session": {sourceState.SessionOptions, targetState.SessionOptions},
		"window":  {sourceState.WindowOptions, targetState.WindowOptions},
		"pane":    {sourceState.PaneOptions, targetState.PaneOptions},
	}

	for name, pair := range scopes {
		want, got := pair[0], pair[1]

		for key, value := range want {
			if got[key] != value {
				t.Fatalf("%s scope option %q mismatch: got %q want %q", name, key, got[key], value)
			}
		}
	}

	if got := targetState.Environment[environmentKey{name: "SESH_SCOPED_VAR"}]; got.value != "scoped-value" || got.unset {
		t.Fatalf("session environment mismatch: got %+v", got)
	}
}

func setupUnrelatedLiveSession(t *testing.T, ctx context.Context, server *tmux.Server) (tmux.Session, []tmux.WindowLinkInfo, tmux.PaneInfo) {
	t.Helper()

	unrelated, err := server.NewSession(ctx, tmux.NewSessionOptions{Name: "unrelated-live", Start: tmux.ExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	_, unrelatedLinks, err := unrelated.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("WindowInfos failed: %v", err)
	}

	unrelatedPane, err := unrelatedLinks[0].Handle().Window().ActivePane(ctx)
	if err != nil {
		t.Fatalf("ActivePane failed: %v", err)
	}

	unrelatedPaneInfo, err := unrelatedPane.Info(ctx)
	if err != nil {
		t.Fatalf("Pane Info failed: %v", err)
	}

	return unrelated, unrelatedLinks, unrelatedPaneInfo
}

func assertUnrelatedLiveSessionIntact(
	t *testing.T,
	ctx context.Context,
	server *tmux.Server,
	unrelated tmux.Session,
	unrelatedLinks []tmux.WindowLinkInfo,
	unrelatedPaneInfo tmux.PaneInfo,
) {
	t.Helper()

	found, err := server.FindSession(ctx, "unrelated-live")
	if err != nil {
		t.Fatalf("unrelated session must survive compensation: %v", err)
	}

	if found.ID() != unrelated.ID() {
		t.Fatalf("unrelated session identity changed: %v vs %v", found.ID(), unrelated.ID())
	}

	_, unrelatedLinksAfter, err := found.WindowInfos(ctx)
	if err != nil || len(unrelatedLinksAfter) != len(unrelatedLinks) {
		t.Fatalf("unrelated session links changed: %v %v", unrelatedLinksAfter, err)
	}

	unrelatedPaneAfter, err := unrelatedLinksAfter[0].Handle().Window().ActivePane(ctx)
	if err != nil {
		t.Fatalf("ActivePane failed: %v", err)
	}

	unrelatedPaneInfoAfter, err := unrelatedPaneAfter.Info(ctx)
	if err != nil {
		t.Fatalf("Pane Info failed: %v", err)
	}

	if unrelatedPaneInfoAfter.PID != unrelatedPaneInfo.PID || unrelatedPaneInfoAfter.ID != unrelatedPaneInfo.ID {
		t.Fatalf("unrelated pane changed: %+v vs %+v", unrelatedPaneInfoAfter, unrelatedPaneInfo)
	}
}

func TestExecutorCompensationRetainsSessionWithForeignWork(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	manifest := newIntegrationManifest()
	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitManifest(t, ctx, repoDir, manifest)

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer func() { _ = repo.Close() }()

	operation := registerPlanOperation(t, ctx, repo, "foreign-work", generation, server.Endpoint().SocketPath, plan)

	failing := plan.Steps[len(plan.Steps)-1]

	journal := newFaultJournal(newPlanJournal(t, ctx, repo, operation))
	journal.beforeIntent = func(step Step) error {
		if step.ID == failing.ID {
			sess, findErr := server.FindSession(ctx, restoredSessionName)
			if findErr != nil {
				return fmt.Errorf("find session: %w", findErr)
			}

			idx := 42

			if _, winErr := sess.NewWindow(ctx, tmux.NewWindowOptions{Name: "foreign-window", Index: &idx}); winErr != nil {
				return fmt.Errorf("new window: %w", winErr)
			}

			return errInjectedIntentFault
		}

		return nil
	}

	cfg := DefaultExecuteConfig()
	cfg.CompensateOnFailure = true

	report, err := NewExecutor(server, manifest, cfg).Execute(ctx, plan, journal)
	if err == nil {
		t.Fatal("expected execution failure")
	}

	found, err := server.FindSession(ctx, restoredSessionName)
	if err != nil {
		t.Fatalf("session containing foreign work must survive compensation, got %v", err)
	}

	if len(report.Compensation) != 1 || report.Compensation[0].Status != CompensationRetained {
		t.Fatalf("expected compensation status %s, got %+v", CompensationRetained, report.Compensation)
	}

	_, links, err := found.WindowInfos(ctx)
	if err != nil || len(links) < 2 {
		t.Fatalf("foreign window must be intact, got links: %v", links)
	}
}

type compensationMutation func(context.Context, *testing.T, tmux.Session)

func soleWindowLink(t *testing.T, ctx context.Context, session tmux.Session) tmux.WindowLinkInfo {
	t.Helper()

	_, links, err := session.WindowInfos(ctx)
	if err != nil || len(links) != 1 {
		t.Fatalf("WindowInfos failed: %v, links=%d", err, len(links))
	}

	return links[0]
}

func replaceSolePane(ctx context.Context, t *testing.T, session tmux.Session) {
	t.Helper()

	pane, err := soleWindowLink(t, ctx, session).Handle().Window().ActivePane(ctx)
	if err != nil {
		t.Fatalf("ActivePane failed: %v", err)
	}

	if _, err := pane.Split(ctx, tmux.SplitOptions{Program: tmux.Exec("sleep", "300")}); err != nil {
		t.Fatalf("Split failed: %v", err)
	}

	if err := pane.Kill(ctx); err != nil {
		t.Fatalf("Kill original pane failed: %v", err)
	}
}

func replaceSoleWindow(ctx context.Context, t *testing.T, session tmux.Session) {
	t.Helper()

	original := soleWindowLink(t, ctx, session)
	if _, err := session.NewWindow(ctx, tmux.NewWindowOptions{Name: "replacement", Program: tmux.Exec("sleep", "300")}); err != nil {
		t.Fatalf("NewWindow failed: %v", err)
	}

	if err := original.Handle().Window().Kill(ctx); err != nil {
		t.Fatalf("Kill original window failed: %v", err)
	}
}

func changeLiveProcess(ctx context.Context, t *testing.T, session tmux.Session) {
	t.Helper()

	pane, err := soleWindowLink(t, ctx, session).Handle().Window().ActivePane(ctx)
	if err != nil {
		t.Fatalf("ActivePane failed: %v", err)
	}

	if err := pane.Respawn(ctx, tmux.RespawnOptions{Program: tmux.Exec("sleep", "300"), KillRunning: true}); err != nil {
		t.Fatalf("Respawn failed: %v", err)
	}
}

func runCompensationOwnershipCase(t *testing.T, mutate compensationMutation) {
	t.Helper()

	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	manifest := newIntegrationManifest()

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	failing := plan.Steps[len(plan.Steps)-1]
	journal := newMemoryJournal()
	journal.beforeIntent = func(step Step) error {
		if step.ID != failing.ID {
			return nil
		}

		session, findErr := server.FindSession(ctx, restoredSessionName)
		if findErr != nil {
			return fmt.Errorf("find restored session before external mutation: %w", findErr)
		}

		mutate(ctx, t, session)

		return errInjectedIntentFault
	}

	cfg := DefaultExecuteConfig()
	cfg.CompensateOnFailure = true

	report, err := NewExecutor(server, manifest, cfg).Execute(ctx, plan, journal)
	if !errors.Is(err, ErrCompensationUnavailable) {
		t.Fatalf("expected ErrCompensationUnavailable, got %v", err)
	}

	if len(report.Compensation) != 1 || report.Compensation[0].Status != CompensationRetained {
		t.Fatalf("expected retained compensation, got %+v", report.Compensation)
	}

	if _, err := server.FindSession(ctx, restoredSessionName); err != nil {
		t.Fatalf("externally changed session must survive: %v", err)
	}
}

func TestExecutorCompensationNeverInfersOwnershipFromCurrentShape(t *testing.T) {
	mutations := []struct {
		name   string
		mutate compensationMutation
	}{
		{name: "sole pane replaced", mutate: replaceSolePane},
		{name: "sole window replaced", mutate: replaceSoleWindow},
		{name: "live process changed", mutate: changeLiveProcess},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			runCompensationOwnershipCase(t, mutation.mutate)
		})
	}
}

func TestResumedExternallyChangedScopedStateSurfacesConflict(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	source, sourceState := setupScopedStateSourceSession(t, ctx, server)
	_ = sourceState

	obs, err := capture.Observe(ctx, server, []tmux.SessionID{source.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitObservationToRepo(t, ctx, repoDir, obs)

	targetServer := tmuxtest.NewServer(t)

	plan, err := BuildPlan(obs.Manifest, []string{sessionIDByName(t, obs.Manifest, "state-source")})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer func() { _ = repo.Close() }()

	operation := registerPlanOperation(t, ctx, repo, "resume-conflict", generation, targetServer.Endpoint().SocketPath, plan)

	report, err := NewExecutor(targetServer, obs.Manifest, DefaultExecuteConfig()).
		Execute(ctx, plan, newPlanJournal(t, ctx, repo, operation))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if _, err := ImportScopedState(ctx, targetServer, plan, report.Receipts); err != nil {
		t.Fatalf("ImportScopedState failed: %v", err)
	}

	tamperAndAssertScopedConflict(t, ctx, targetServer, plan, report.Receipts)
}

func tamperAndAssertScopedConflict(
	t *testing.T,
	ctx context.Context,
	targetServer *tmux.Server,
	plan Plan,
	receipts map[string]string,
) {
	t.Helper()

	targetSess, err := targetServer.FindSession(ctx, "state-source")
	if err != nil {
		t.Fatalf("FindSession failed: %v", err)
	}

	if err := targetSess.Options().Set(ctx, "@sesh-flag", "external-tamper"); err != nil {
		t.Fatalf("tampering option failed: %v", err)
	}

	mismatches, err := ObserveScopedStateConflicts(ctx, targetServer, plan, receipts)
	if err != nil {
		t.Fatalf("ObserveScopedStateConflicts failed: %v", err)
	}

	if len(mismatches) == 0 {
		t.Fatal("expected scoped state conflict to be detected, got 0 mismatches")
	}

	found := false

	for _, m := range mismatches {
		if m.Domain == MismatchOption && m.Reason == "local_option_mismatch" {
			found = true

			break
		}
	}

	if !found {
		t.Fatalf("expected local_option_mismatch, got %+v", mismatches)
	}
}
