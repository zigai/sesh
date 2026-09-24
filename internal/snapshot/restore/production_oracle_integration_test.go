//go:build (linux || darwin) && integration

package restore

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func createScratchSessions(t *testing.T, ctx context.Context, server *tmux.Server, count int) {
	t.Helper()

	for index := range count {
		if _, err := server.NewSession(ctx, tmux.NewSessionOptions{
			Name:    "scratch-" + strconv.Itoa(index),
			Program: tmux.Exec("sleep", "300"),
			Start:   tmux.ExistingOnly,
		}); err != nil {
			t.Fatalf("creating scratch session failed: %v", err)
		}
	}
}

func TestProductionExecutorSourceDestructionParity(t *testing.T) {
	sourceServer := tmuxtest.NewServer(t)
	targetServer := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	unrelated, unrelatedLinks, unrelatedPane := setupTargetWithUnrelatedSession(t, ctx, targetServer)

	sessionNames := []string{"source-lead", "source-foll", "src-repeat"}
	repoDir := filepath.Join(t.TempDir(), "repo")
	generation, manifest, expected := captureAndDestroySource(t, ctx, sourceServer, repoDir, sessionNames)

	plan := buildPlanForNames(t, manifest, sessionNames)

	executeAndVerifyOnTarget(t, ctx, targetServer, repoDir, generation, manifest, plan)

	verifyUnrelatedTargetIntact(t, ctx, targetServer, unrelated.ID(), unrelatedLinks, unrelatedPane)
	verifyTargetStructuralParity(t, ctx, targetServer, expected)
}

func TestProductionExecutorResumeAfterPartialFailure(t *testing.T) {
	sourceServer := tmuxtest.NewServer(t)
	targetServer := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	createScratchSessions(t, ctx, targetServer, 2)

	leadSess, follSess, repeatSess := createActualSourceFixture(t, ctx, sourceServer)

	sessionNames := []string{"source-lead", "source-foll", "src-repeat"}
	expected := readIndependentSourceState(t, ctx, sourceServer, sessionNames)

	observation, err := capture.Observe(ctx, sourceServer, []tmux.SessionID{leadSess.ID(), follSess.ID(), repeatSess.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitObservationToRepo(t, ctx, repoDir, observation)

	selected := make([]string, 0, len(sessionNames))
	for _, name := range sessionNames {
		selected = append(selected, sessionIDByName(t, observation.Manifest, name))
	}

	plan, err := BuildPlan(observation.Manifest, selected)
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	if err := sourceServer.Kill(ctx); err != nil {
		t.Fatalf("killing the source server failed: %v", err)
	}

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	defer func() { _ = repo.Close() }()

	operation := registerPlanOperation(t, ctx, repo, "production-resume", generation, targetServer.Endpoint().SocketPath, plan)

	interrupted := plan.Steps[len(plan.Steps)/2]

	journal := newFaultJournal(newPlanJournal(t, ctx, repo, operation))
	journal.failIntent[interrupted.ID()] = errInjectedIntentFault

	resumable := DefaultExecuteConfig()

	if _, err := NewExecutor(targetServer, observation.Manifest, resumable).Execute(ctx, plan, journal); err == nil {
		t.Fatal("expected the interrupted attempt to fail")
	}

	resumed, err := NewExecutor(targetServer, observation.Manifest, DefaultExecuteConfig()).
		Execute(ctx, plan, newPlanJournal(t, ctx, repo, operation))
	if err != nil {
		t.Fatalf("resumed execution failed: %v", err)
	}

	if len(resumed.Adopted) == 0 {
		t.Fatal("expected the resumed attempt to adopt confirmed steps")
	}

	if _, err := ImportScopedState(ctx, targetServer, plan, resumed.Receipts); err != nil {
		t.Fatalf("ImportScopedState failed: %v", err)
	}

	verification, err := Verify(ctx, targetServer, plan, resumed.Receipts)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !verification.Verified {
		t.Fatalf("resumed target failed independent verification: %+v", verification.Mismatches)
	}

	verifyTargetStructuralParity(t, ctx, targetServer, expected)
}

func setupTargetWithUnrelatedSession(t *testing.T, ctx context.Context, targetServer *tmux.Server) (tmux.Session, []tmux.WindowLinkInfo, tmux.PaneInfo) {
	t.Helper()

	if err := targetServer.GlobalSessionOptions().SetBaseIndex(ctx, 2); err != nil {
		t.Fatal(err)
	}

	if err := targetServer.GlobalSessionOptions().SetStatus(ctx, "off"); err != nil {
		t.Fatal(err)
	}

	createScratchSessions(t, ctx, targetServer, 2)

	unrelated, err := targetServer.NewSession(ctx, tmux.NewSessionOptions{
		Name:    "unrelated-target-sess",
		Program: tmux.Exec("sleep", "300"),
		Start:   tmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, unrelatedLinks, err := unrelated.WindowInfos(ctx)
	if err != nil || len(unrelatedLinks) != 1 {
		t.Fatalf("unrelated links: %v, %v", unrelatedLinks, err)
	}

	unrelatedPane := getFirstPaneInfo(t, ctx, unrelatedLinks[0].Handle().Window())

	return unrelated, unrelatedLinks, unrelatedPane
}

func captureAndDestroySource(
	t *testing.T,
	ctx context.Context,
	sourceServer *tmux.Server,
	repoDir string,
	sessionNames []string,
) (string, model.Manifest, *independentSourceState) {
	t.Helper()

	leadSess, follSess, repeatSess := createActualSourceFixture(t, ctx, sourceServer)
	expected := readIndependentSourceState(t, ctx, sourceServer, sessionNames)

	observation, err := capture.Observe(ctx, sourceServer, []tmux.SessionID{leadSess.ID(), follSess.ID(), repeatSess.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	generation := commitObservationToRepo(t, ctx, repoDir, observation)

	if err := sourceServer.Kill(ctx); err != nil {
		t.Fatalf("killing source server failed: %v", err)
	}

	return generation, observation.Manifest, expected
}

func buildPlanForNames(t *testing.T, manifest model.Manifest, names []string) Plan {
	t.Helper()

	selected := make([]string, 0, len(names))
	for _, name := range names {
		selected = append(selected, sessionIDByName(t, manifest, name))
	}

	plan, err := BuildPlan(manifest, selected)
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	return plan
}

func executeAndVerifyOnTarget(
	t *testing.T,
	ctx context.Context,
	targetServer *tmux.Server,
	repoDir, generation string,
	manifest model.Manifest,
	plan Plan,
) {
	t.Helper()

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer func() { _ = repo.Close() }()

	operation := registerPlanOperation(t, ctx, repo, "production-parity", generation, targetServer.Endpoint().SocketPath, plan)

	report, err := NewExecutor(targetServer, manifest, DefaultExecuteConfig()).
		Execute(ctx, plan, newPlanJournal(t, ctx, repo, operation))
	if err != nil {
		t.Fatalf("production executor failed: %v", err)
	}

	if _, err := ImportScopedState(ctx, targetServer, plan, report.Receipts); err != nil {
		t.Fatalf("ImportScopedState failed: %v", err)
	}

	verification, err := Verify(ctx, targetServer, plan, report.Receipts)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !verification.Verified {
		t.Fatalf("production verifier rejected restored target: %+v", verification.Mismatches)
	}
}
