//go:build (linux || darwin) && integration

package restore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var (
	errInjectedIntentFault       = errors.New("injected journal write fault")
	errUnreachable               = errors.New("unreachable")
	errUnrecognizedPostDispatch  = errors.New("unrecognized post-dispatch I/O error")
	errRenameFailedAfterCreation = errors.New("rename failed after creation")
)

func commitManifest(t *testing.T, ctx context.Context, repoDir string, manifest model.Manifest) string {
	t.Helper()

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	attempt, err := repo.Begin(ctx)
	if err != nil {
		t.Fatalf("repo.Begin failed: %v", err)
	}

	generation, err := repo.Commit(ctx, attempt.ID, manifest, nil)
	if err != nil {
		t.Fatalf("repo.Commit failed: %v", err)
	}

	if err := repo.Close(); err != nil {
		t.Fatalf("repo.Close failed: %v", err)
	}

	return generation.ID
}

func registerPlanOperation(
	t *testing.T,
	ctx context.Context,
	repo *store.Repository,
	label, generation, target string,
	plan Plan,
) string {
	t.Helper()

	specs, err := PlanActions(plan)
	if err != nil {
		t.Fatalf("PlanActions failed: %v", err)
	}

	op, err := repo.RegisterOperation(ctx, testOperationID(label), generation, model.Bytes(target), testActions(specs))
	if err != nil {
		t.Fatalf("RegisterOperation failed: %v", err)
	}

	return op.ID
}

func newPlanJournal(t *testing.T, ctx context.Context, repo *store.Repository, operation string) *storeJournal {
	t.Helper()

	journal, err := newStoreJournal(ctx, repo, operation)
	if err != nil {
		t.Fatalf("newStoreJournal failed: %v", err)
	}

	return journal
}

func createSessionStep(t *testing.T, plan Plan) Step {
	t.Helper()

	for _, step := range plan.Steps {
		if step.Kind == StepCreateSession {
			return step
		}
	}

	t.Fatal("plan has no session creation step")

	return Step{}
}

const (
	fixtureSessionName = "fixture"

	restoredSessionName = "alpha"
)

func restoredSessionCount(t *testing.T, ctx context.Context, server *tmux.Server) int {
	t.Helper()

	sessions, err := server.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions failed: %v", err)
	}

	count := 0

	for _, session := range sessions {
		if session.Name != fixtureSessionName {
			count++
		}
	}

	return count
}

func requireRestoredSessionAbsent(t *testing.T, ctx context.Context, server *tmux.Server) {
	t.Helper()

	if _, err := server.FindSession(ctx, restoredSessionName); !errors.Is(err, tmux.ErrNotFound) {
		t.Fatalf("expected session %q to be absent, got %v", restoredSessionName, err)
	}
}

func requireFixtureIntact(t *testing.T, ctx context.Context, server *tmux.Server) {
	t.Helper()

	if _, err := server.FindSession(ctx, fixtureSessionName); err != nil {
		t.Fatalf("fixture session must stay intact, got %v", err)
	}
}

func TestExecutorJournalIntentFaultPreventsEffect(t *testing.T) {
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

	operation := registerPlanOperation(t, ctx, repo, "intent-fault", generation, server.Endpoint().SocketPath, plan)

	createStep := createSessionStep(t, plan)

	journal := newFaultJournal(newPlanJournal(t, ctx, repo, operation))
	journal.failIntent[createStep.ID] = errInjectedIntentFault

	cfg := DefaultExecuteConfig()

	executor := NewExecutor(server, manifest, cfg)

	_, err = executor.Execute(ctx, plan, journal)
	if err == nil {
		t.Fatal("expected execution failure on journal intent fault")
	}

	if count := restoredSessionCount(t, ctx, server); count != 0 {
		t.Fatalf("expected no restored session after failed intent, got %d", count)
	}

	requireRestoredSessionAbsent(t, ctx, server)
	requireFixtureIntact(t, ctx, server)

	op, reconcileErr := repo.ReconcileOperation(ctx, operation)
	if reconcileErr != nil {
		t.Fatalf("ReconcileOperation failed: %v", reconcileErr)
	}

	for _, action := range op.Actions {
		if action.ID != ActionID(createStep.ID) {
			continue
		}

		if action.State != StatePlanned {
			t.Fatalf("expected action to stay planned after a failed intent, got %q", action.State)
		}
	}
}

func TestExecutorRefusesPriorUncertainEffect(t *testing.T) {
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

	operation := registerPlanOperation(t, ctx, repo, "prior-uncertain", generation, server.Endpoint().SocketPath, plan)

	createStep := createSessionStep(t, plan)

	if err := repo.Transition(ctx, operation, ActionID(createStep.ID), StatePlanned, StateIssued, nil); err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	journal := newPlanJournal(t, ctx, repo, operation)
	cfg := DefaultExecuteConfig()
	cfg.CompensateOnFailure = true

	executor := NewExecutor(server, manifest, cfg)

	_, err = executor.Execute(ctx, plan, journal)
	if err == nil {
		t.Fatal("expected refusal for a prior uncertain effect")
	}

	if !errors.Is(err, ErrPriorUncertainty) {
		t.Fatalf("expected ErrPriorUncertainty, got %v", err)
	}

	if count := restoredSessionCount(t, ctx, server); count != 0 {
		t.Fatalf("refused attempt must not create sessions, got %d", count)
	}

	requireRestoredSessionAbsent(t, ctx, server)
}

func TestExecutorResumeAdoptsConfirmedStepsWithoutDuplicates(t *testing.T) {
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

	operation := registerPlanOperation(t, ctx, repo, "resume-adoption", generation, server.Endpoint().SocketPath, plan)

	cfg := DefaultExecuteConfig()

	panesBefore, linksBefore := runFirstAttemptAndCountPanes(t, ctx, server, manifest, cfg, plan, repo, operation)
	assertResumedAttemptAdoptsAndPreservesPanes(t, ctx, server, manifest, cfg, plan, repo, operation, panesBefore, linksBefore)
}

func windowPaneCount(t *testing.T, ctx context.Context, links []tmux.WindowLinkInfo) int {
	t.Helper()

	total := 0

	for _, link := range links {
		panes, err := link.Handle().Window().Panes(ctx)
		if err != nil {
			t.Fatalf("Panes failed: %v", err)
		}

		total += len(panes)
	}

	return total
}

func assertCanceledReceiptOutcome(
	t *testing.T,
	ctx context.Context,
	server *tmux.Server,
	repo *store.Repository,
	operation string,
	createStep Step,
	err error,
) {
	t.Helper()

	if err == nil {
		t.Fatal("expected uncertain execution failure")
	}

	if !errors.Is(err, ErrExecutionUncertain) {
		t.Fatalf("expected ErrExecutionUncertain, got %v", err)
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected joined cancellation, got %v", err)
	}

	if !errors.Is(err, ErrCompensationUnavailable) {
		t.Fatalf("expected unsafe cleanup request to be reported, got %v", err)
	}

	op, reconcileErr := repo.ReconcileOperation(ctx, operation)
	if reconcileErr != nil {
		t.Fatalf("ReconcileOperation failed: %v", reconcileErr)
	}

	states := actionStates(op)
	if states[ActionID(createStep.ID)] != StateUnknown {
		t.Fatalf("expected the unconfirmed effect to be recorded as %q, got %q", StateUnknown, states[ActionID(createStep.ID)])
	}

	if count := restoredSessionCount(t, ctx, server); count != 1 {
		t.Fatalf("unsafe compensation must retain the created session, got %d restored sessions", count)
	}

	if _, findErr := server.FindSession(ctx, restoredSessionName); findErr != nil {
		t.Fatalf("created session must survive unsafe compensation: %v", findErr)
	}

	requireFixtureIntact(t, ctx, server)
}

func TestExecutorCancellationDuringReceiptKeepsUncertainty(t *testing.T) {
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

	operation := registerPlanOperation(t, ctx, repo, "cancel-uncertain", generation, server.Endpoint().SocketPath, plan)

	createStep := createSessionStep(t, plan)

	canceled, cancelCtx := context.WithCancel(ctx)

	journal := newFaultJournal(newPlanJournal(t, ctx, repo, operation))
	journal.beforeConfirmed = func(step Step) error {
		if step.ID != createStep.ID {
			return nil
		}

		cancelCtx()

		return context.Canceled
	}

	cfg := DefaultExecuteConfig()
	cfg.CompensateOnFailure = true

	executor := NewExecutor(server, manifest, cfg)

	_, err = executor.Execute(canceled, plan, journal)
	assertCanceledReceiptOutcome(t, ctx, server, repo, operation, createStep, err)

	_, err = NewExecutor(server, manifest, DefaultExecuteConfig()).Execute(ctx, plan, newPlanJournal(t, ctx, repo, operation))
	if !errors.Is(err, ErrPriorUncertainty) {
		t.Fatalf("expected refusal on prior uncertainty, got %v", err)
	}

	if count := restoredSessionCount(t, ctx, server); count != 1 {
		t.Fatalf("refused attempt must preserve the retained session, got %d", count)
	}
}

func actionStates(op store.Operation) map[string]string {
	states := make(map[string]string, len(op.Actions))

	for _, action := range op.Actions {
		states[action.ID] = action.State
	}

	return states
}

func TestExecutorCrashAfterEffectNeverDuplicates(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	manifest := newIntegrationManifest()
	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitManifest(t, ctx, repoDir, manifest)

	newer := commitManifest(t, ctx, repoDir, manifest)

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}

	if newer == generation {
		t.Fatalf("expected two distinct generations, got %q twice", generation)
	}

	operation := registerPlanOperation(t, ctx, repo, "crash-after-effect", generation, server.Endpoint().SocketPath, plan)

	createStep := createSessionStep(t, plan)

	runCrashWorker(t, ctx, repoDir, generation, operation, server.Endpoint().SocketPath, createStep.ID)

	requireCrashedCreationSurvived(t, ctx, server, repo, operation, createStep)
	requireRefusedRetryWithoutDuplicates(t, ctx, server, repo, operation, plan, manifest)
	requirePinReleasedOnlyByAbandonment(t, ctx, repo, generation, operation)

	if err := repo.Close(); err != nil {
		t.Fatalf("repo.Close failed: %v", err)
	}
}

func requireCrashedCreationSurvived(
	t *testing.T,
	ctx context.Context,
	server *tmux.Server,
	repo *store.Repository,
	operation string,
	createStep Step,
) {
	t.Helper()

	if count := restoredSessionCount(t, ctx, server); count != 1 {
		t.Fatalf("crash worker should have applied exactly one creation, got %d restored sessions", count)
	}

	if _, err := server.FindSession(ctx, restoredSessionName); err != nil {
		t.Fatalf("crashed attempt should have created the session exactly once: %v", err)
	}

	op, err := repo.ReconcileOperation(ctx, operation)
	if err != nil {
		t.Fatalf("ReconcileOperation failed: %v", err)
	}

	if states := actionStates(op); states[ActionID(createStep.ID)] != StateIssued {
		t.Fatalf("expected the interrupted effect to stay %q, got %q", StateIssued, states[ActionID(createStep.ID)])
	}
}

func requireRefusedRetryWithoutDuplicates(
	t *testing.T,
	ctx context.Context,
	server *tmux.Server,
	repo *store.Repository,
	operation string,
	plan Plan,
	manifest model.Manifest,
) {
	t.Helper()

	_, err := NewExecutor(server, manifest, DefaultExecuteConfig()).Execute(ctx, plan, newPlanJournal(t, ctx, repo, operation))
	if !errors.Is(err, ErrPriorUncertainty) {
		t.Fatalf("expected refusal after crash, got %v", err)
	}

	if count := restoredSessionCount(t, ctx, server); count != 1 {
		t.Fatalf("refused attempt must not duplicate the crashed creation, got %d restored sessions", count)
	}
}

func requirePinReleasedOnlyByAbandonment(
	t *testing.T,
	ctx context.Context,
	repo *store.Repository,
	generation, operation string,
) {
	t.Helper()

	if err := repo.Delete(ctx, generation); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected the active operation to protect its source, got %v", err)
	}

	if err := repo.Finish(ctx, operation, true); err != nil {
		t.Fatalf("Finish(abandon) failed: %v", err)
	}

	if err := repo.Delete(ctx, generation); err != nil {
		t.Fatalf("abandonment should release the source pin, got %v", err)
	}
}

const (
	crashWorkerFlag       = "SESH_RESTORE_CRASH_WORKER"
	crashWorkerRepo       = "SESH_RESTORE_REPO"
	crashWorkerGeneration = "SESH_RESTORE_GENERATION"
	crashWorkerOperation  = "SESH_RESTORE_OPERATION"
	crashWorkerSocket     = "SESH_RESTORE_SOCKET"
	crashWorkerStep       = "SESH_RESTORE_CRASH_STEP"
)

func runCrashWorker(t *testing.T, ctx context.Context, repoDir, generation, operation, socket, stepID string) {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(ctx, self, "-test.run=^TestRestoreCrashWorker$")
	cmd.Env = []string{
		crashWorkerFlag + "=1",
		crashWorkerRepo + "=" + repoDir,
		crashWorkerGeneration + "=" + generation,
		crashWorkerOperation + "=" + operation,
		crashWorkerSocket + "=" + socket,
		crashWorkerStep + "=" + stepID,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
	}

	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("crash worker was expected to die, output:\n%s", string(output))
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("crash worker failed without an exit status: %v\n%s", err, string(output))
	}

	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("crash worker exit status %v, output:\n%s", status, string(output))
	}
}

func TestRestoreCrashWorker(t *testing.T) {
	if os.Getenv(crashWorkerFlag) == "" {
		t.Skip("subprocess entry point")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	repoDir := os.Getenv(crashWorkerRepo)
	generation := os.Getenv(crashWorkerGeneration)
	operation := os.Getenv(crashWorkerOperation)
	socket := os.Getenv(crashWorkerSocket)
	stepID := os.Getenv(crashWorkerStep)

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("worker: store.Open failed: %v", err)
	}

	manifest, err := repo.Read(ctx, generation)
	if err != nil {
		t.Fatalf("worker: repo.Read failed: %v", err)
	}

	selected := make([]string, 0, len(manifest.Sessions))
	for _, session := range manifest.Sessions {
		selected = append(selected, session.ID)
	}

	plan, err := BuildPlan(manifest, selected)
	if err != nil {
		t.Fatalf("worker: BuildPlan failed: %v", err)
	}

	server, err := tmux.New(tmux.Config{SocketPath: socket, ConfigFile: "/dev/null"})
	if err != nil {
		t.Fatalf("worker: tmux.New failed: %v", err)
	}

	journal := newFaultJournal(newPlanJournal(t, ctx, repo, operation))
	journal.beforeConfirmed = func(step Step) error {
		if step.ID != stepID {
			return nil
		}

		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)

		return errUnreachable
	}

	_, _ = NewExecutor(server, manifest, DefaultExecuteConfig()).Execute(ctx, plan, journal)
}

func refusalSessions(t *testing.T, ctx context.Context, server *tmux.Server) (tmux.Session, tmux.Session) {
	t.Helper()

	first, err := server.NewSession(ctx, tmux.NewSessionOptions{Name: "refusal-first", Start: tmux.ExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	second, err := server.NewSession(ctx, tmux.NewSessionOptions{Name: "refusal-second", Start: tmux.ExistingOnly})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	return first, second
}

func TestRefusedCommandWithStderrPreservesUnknown(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	first, second := refusalSessions(t, ctx, server)

	_, dupErr := server.NewSession(ctx, tmux.NewSessionOptions{Name: "refusal-first", Start: tmux.ExistingOnly})
	if dupErr == nil {
		t.Fatal("expected duplicate session creation to fail")
	}

	if outcome := classifyMutation(dupErr); outcome.Effect != tmux.Unknown {
		t.Fatalf("duplicate session must preserve library unknown, got %s", outcome.Effect)
	}

	_, links, err := second.WindowInfos(ctx)
	if err != nil || len(links) != 1 {
		t.Fatalf("WindowInfos failed: %v %v", links, err)
	}

	window := links[0].Handle().Window()
	index := links[0].Index

	_, linkErr := window.Link(ctx, first, tmux.LinkOptions{Index: &index})
	if linkErr == nil {
		t.Fatal("expected linking onto an occupied slot to fail")
	}

	if outcome := classifyMutation(linkErr); outcome.Effect != tmux.Unknown {
		t.Fatalf("occupied slot link must preserve library unknown, got %s", outcome.Effect)
	}
}

func TestPreDispatchTypedRefusalIsClassifiedNotSent(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"working_dir_not_found", ErrWorkingDirNotFound},
		{"session_collision", ErrSessionCollision},
		{"group_collision", ErrGroupCollision},
		{"invalid_handle", tmux.ErrInvalidHandle},
		{"invalid_argument", tmux.ErrInvalidArgument},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := classifyMutation(tc.err)
			if outcome.Effect != tmux.NotSent {
				t.Fatalf("expected pre-dispatch error to be not-sent, got %s", outcome.Effect)
			}
		})
	}
}

func TestJoinedCancellationPreservesUncertainty(t *testing.T) {
	notSentErr := ErrWorkingDirNotFound
	uncertainErr := errUnrecognizedPostDispatch

	joined := errors.Join(notSentErr, uncertainErr)

	outcome := classifyMutation(joined)
	if outcome.Effect != tmux.Unknown {
		t.Fatalf("joined error with uncertain branch must be Unknown, got %s", outcome.Effect)
	}

	joinedWithCancel := errors.Join(notSentErr, context.Canceled)

	outcomeCancel := classifyMutation(joinedWithCancel)
	if outcomeCancel.Effect != tmux.Unknown {
		t.Fatalf("joined cancellation after dispatch must be Unknown, got %s", outcomeCancel.Effect)
	}
}

func TestCompositePartiallyAppliedMutationPreservesReceipt(t *testing.T) {
	applied := &appliedError{
		runtimeID: "$42",
		err:       errRenameFailedAfterCreation,
	}

	outcome := classifyMutation(applied)
	if outcome.Effect != tmux.Unknown {
		t.Fatalf("partially applied composite mutation must be Unknown, got %s", outcome.Effect)
	}

	if outcome.RuntimeID != "$42" {
		t.Fatalf("partially applied mutation must preserve runtime ID, got %q", outcome.RuntimeID)
	}
}

func runFirstAttemptAndCountPanes(
	t *testing.T,
	ctx context.Context,
	server *tmux.Server,
	manifest model.Manifest,
	cfg ExecuteConfig,
	plan Plan,
	repo *store.Repository,
	operation string,
) (int, []tmux.WindowLinkInfo) {
	t.Helper()

	first, err := NewExecutor(server, manifest, cfg).Execute(ctx, plan, newPlanJournal(t, ctx, repo, operation))
	if err != nil {
		t.Fatalf("first execution failed: %v", err)
	}

	if len(first.Dispatched) != len(plan.Steps) {
		t.Fatalf("expected every step dispatched on first attempt, got %d of %d", len(first.Dispatched), len(plan.Steps))
	}

	session, err := server.FindSession(ctx, restoredSessionName)
	if err != nil {
		t.Fatalf("FindSession failed: %v", err)
	}

	_, linksBefore, err := session.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("WindowInfos failed: %v", err)
	}

	return windowPaneCount(t, ctx, linksBefore), linksBefore
}

func assertResumedAttemptAdoptsAndPreservesPanes(
	t *testing.T,
	ctx context.Context,
	server *tmux.Server,
	manifest model.Manifest,
	cfg ExecuteConfig,
	plan Plan,
	repo *store.Repository,
	operation string,
	panesBefore int,
	linksBefore []tmux.WindowLinkInfo,
) {
	t.Helper()

	second, err := NewExecutor(server, manifest, cfg).Execute(ctx, plan, newPlanJournal(t, ctx, repo, operation))
	if err != nil {
		t.Fatalf("resumed execution failed: %v", err)
	}

	if len(second.Dispatched) != 0 {
		t.Fatalf("resumed execution must not dispatch confirmed steps, dispatched %v", second.Dispatched)
	}

	if len(second.Adopted) != len(plan.Steps) {
		t.Fatalf("expected every step adopted, got %d of %d", len(second.Adopted), len(plan.Steps))
	}

	if count := restoredSessionCount(t, ctx, server); count != 1 {
		t.Fatalf("resumed execution must not create duplicate session, got %d restored sessions", count)
	}

	if _, err := server.FindSession(ctx, restoredSessionName); err != nil {
		t.Fatalf("restored session must stay available: %v", err)
	}

	if panesAfter := windowPaneCount(t, ctx, linksBefore); panesAfter != panesBefore {
		t.Fatalf("resumed execution changed pane count: %d -> %d", panesBefore, panesAfter)
	}
}
