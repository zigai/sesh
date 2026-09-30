//go:build (linux || darwin) && integration

package session_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/session"
)

func TestCatalogLiveObservationWithDisposableGotmux(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	sessA, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name: "project-alpha",
	})
	if err != nil {
		t.Fatalf("creating session alpha: %v", err)
	}

	_, err = server.NewSession(ctx, gotmux.NewSessionOptions{
		Name: "project-beta",
	})
	if err != nil {
		t.Fatalf("creating session beta: %v", err)
	}

	panes, err := sessA.Panes(ctx)
	if err != nil {
		t.Fatalf("querying panes: %v", err)
	}

	if len(panes) == 0 {
		t.Fatal("expected at least 1 pane in session alpha")
	}

	activePaneID := string(panes[0].ID)

	result, err := session.ListCatalog(ctx, session.CatalogOptions{
		Server:      server,
		CurrentPane: activePaneID,
	})
	if err != nil {
		t.Fatalf("ListCatalog() error = %v", err)
	}

	if result.Status != session.StatusComplete {
		t.Fatalf("result.Status = %v, want complete", result.Status)
	}

	if len(result.Sessions) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(result.Sessions))
	}

	verifyLiveSessionsResult(t, result.Sessions, activePaneID)
}

func TestCatalogLiveObservationCanceledContext(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result, err := session.ListCatalog(ctx, session.CatalogOptions{
		Server: server,
	})
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}

	if result.Status != session.StatusFailed {
		t.Fatalf("result.Status = %v, want failed", result.Status)
	}
}

func TestCatalogKeepsLiveRowsWhenInvokingPaneDisappears(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "first"}); err != nil {
		t.Fatal(err)
	}

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "second"}); err != nil {
		t.Fatal(err)
	}

	result, err := session.ListCatalog(ctx, session.CatalogOptions{Server: server, CurrentPane: "%999999"})
	if err == nil || result.Status != session.StatusPartial || len(result.Sessions) < 2 || result.LiveErr == nil {
		t.Fatalf("stale pane catalog = status %s, rows %d, live error %v, error %v", result.Status, len(result.Sessions), result.LiveErr, err)
	}

	found := map[string]bool{}
	for _, row := range result.Sessions {
		found[row.Name] = true
		if row.IsCurrent {
			t.Fatalf("stale pane marked %q as current", row.Name)
		}
	}

	if !found["first"] || !found["second"] {
		t.Fatalf("stale pane catalog lost fixture sessions: %#v", found)
	}
}

func TestCatalogUsesInvokingSessionHintWithoutPaneLookup(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	first, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "second"}); err != nil {
		t.Fatal(err)
	}

	result, err := session.ListCatalog(ctx, session.CatalogOptions{
		Server: server, CurrentPane: "%999999", CurrentSessionID: string(first.ID()),
	})
	if err != nil || result.Status != session.StatusComplete {
		t.Fatalf("session hint catalog = status %s, error %v", result.Status, err)
	}

	for _, row := range result.Sessions {
		if row.IsCurrent != (row.ID == string(first.ID())) {
			t.Fatalf("session hint marked %q (%s) current = %t", row.Name, row.ID, row.IsCurrent)
		}
	}
}

func TestCatalogPrefersInvokingPaneForCurrentAgentSelection(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx := t.Context()

	sessions, err := server.Sessions(ctx)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("fixture sessions: %v", err)
	}

	origin := sessions[0]

	panes, err := origin.Handle().Panes(ctx)
	if err != nil || len(panes) == 0 {
		t.Fatalf("origin panes: %v", err)
	}

	first := panes[0].Handle()

	invoking, err := first.Split(ctx, gotmux.SplitOptions{Direction: gotmux.DirectionVertical})
	if err != nil {
		t.Fatal(err)
	}

	if err := first.Select(ctx); err != nil {
		t.Fatal(err)
	}

	result, err := session.ListCatalog(ctx, session.CatalogOptions{
		Server: server, CurrentPane: string(invoking.ID()), CurrentSessionID: string(origin.ID),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range result.Sessions {
		if row.ID == string(origin.ID) {
			if !row.IsCurrent || row.ActivePaneID != string(invoking.ID()) {
				t.Fatalf("current row = %+v, want invoking pane %s", row, invoking.ID())
			}

			return
		}
	}

	t.Fatal("current session missing from catalog")
}

func TestCatalogLiveObservationKilledServer(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "temp"}); err != nil {
		t.Fatal(err)
	}

	if err := server.Kill(ctx); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	result, err := session.ListCatalog(ctx, session.CatalogOptions{
		Server: server,
	})
	if err != nil {
		t.Fatalf("unexpected error on killed server (should treat as ErrNoServer 0 sessions): %v", err)
	}

	if len(result.Sessions) != 0 {
		t.Fatalf("expected 0 sessions after kill, got %d", len(result.Sessions))
	}
}

func TestConcurrentUpdatesUnderLock(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "history", "state.json")

	svc := session.NewService(session.ServiceDeps{})
	ctx := t.Context()

	if err := svc.SaveState(ctx, statePath, &session.State{Version: session.CurrentHistoryVersion}); err != nil {
		t.Fatalf("initial save failed: %v", err)
	}

	const (
		workerCount = 8
		iterations  = 10
	)

	var wg sync.WaitGroup

	errs := make(chan error, workerCount*iterations)

	for w := range workerCount {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for i := range iterations {
				sessName := "sess-" + strconv.Itoa(workerID) + "-" + strconv.Itoa(i)

				_, err := svc.UpdateState(ctx, statePath, func(current *session.State) {
					current.RecordSwitch(session.Identity{Name: sessName})
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent UpdateState failed: %v", err)
		}
	}

	final, err := svc.LoadState(statePath)
	if err != nil {
		t.Fatalf("loading final state: %v", err)
	}

	for w := range workerCount {
		for i := range iterations {
			sessName := "sess-" + strconv.Itoa(w) + "-" + strconv.Itoa(i)
			if final.GetLastSwitch(session.Identity{Name: sessName}) == 0 {
				t.Fatalf("missing record for %s", sessName)
			}
		}
	}
}

func TestLockCancellationPrompt(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "history", "state.json")
	svc := session.NewService(session.ServiceDeps{})

	blockLock := make(chan struct{})
	lockAcquired := make(chan struct{})

	holderDone := make(chan error, 1)

	go func() {
		_, err := svc.UpdateState(t.Context(), statePath, func(*session.State) {
			close(lockAcquired)
			<-blockLock
		})
		holderDone <- err
	}()

	select {
	case <-lockAcquired:
	case err := <-holderDone:
		t.Fatalf("holder did not acquire lock: %v", err)
	}

	cancelCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := svc.UpdateState(cancelCtx, statePath, func(current *session.State) {
		current.RecordSwitch(session.Identity{Name: "should-not-run"})
	})
	elapsed := time.Since(start)

	close(blockLock)

	if holderErr := <-holderDone; holderErr != nil {
		t.Fatal(holderErr)
	}

	if err == nil {
		t.Fatal("expected error due to lock acquisition timeout, got nil")
	}

	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context timeout/canceled error, got %v", err)
	}

	if elapsed > 2*time.Second {
		t.Fatalf("lock acquisition did not cancel promptly, took %v", elapsed)
	}
}

func verifyLiveSessionsResult(t *testing.T, sessions []session.Session, activePaneID string) {
	t.Helper()

	foundAlpha := false
	foundBeta := false

	for _, s := range sessions {
		if s.Name == "project-alpha" {
			foundAlpha = true

			if !s.IsCurrent {
				t.Error("project-alpha should be marked current")
			}

			if s.Kind != session.KindLive {
				t.Errorf("expected KindLive, got %v", s.Kind)
			}

			if s.ActivePaneID != activePaneID {
				t.Errorf("ActivePaneID = %q, want %q", s.ActivePaneID, activePaneID)
			}
		}

		if s.Name == "project-beta" {
			foundBeta = true
		}
	}

	if !foundAlpha || !foundBeta {
		t.Fatalf("did not find both expected sessions: alpha=%v, beta=%v", foundAlpha, foundBeta)
	}
}

const (
	envLockWorkerFlag        = "SESH_TEST_LOCK_WORKER"
	envWorkerID              = "SESH_WORKER_ID"
	envStatePath             = "SESH_STATE_PATH"
	historyProcessIterations = 5
)

func TestHistoryProcessWorker(t *testing.T) {
	mode := os.Getenv(envLockWorkerFlag)
	if mode == "" {
		return
	}

	statePath := os.Getenv(envStatePath)
	if statePath == "" {
		t.Fatal("missing isolated state path")
	}

	svc := session.NewService(session.ServiceDeps{})

	if mode == "cancel_waiter" {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()

		called := false

		state, err := svc.UpdateState(ctx, statePath, func(*session.State) { called = true })
		if !errors.Is(err, context.DeadlineExceeded) || called || state != nil {
			t.Fatalf("lock waiter outcome: %v, callback=%v, state=%v", err, called, state)
		}

		return
	}

	if mode != "worker" {
		t.Fatal("invalid worker mode")
	}

	for i := range historyProcessIterations {
		name := fmt.Sprintf("proc-%s-%d", os.Getenv(envWorkerID), i)

		_, err := svc.UpdateState(t.Context(), statePath, func(s *session.State) {
			s.RecordSwitch(session.Identity{Name: name})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestMultiProcessConcurrentUpdatesUnderLock(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "history", "state.json")
	svc := session.NewService(session.ServiceDeps{})

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if err := svc.SaveState(ctx, statePath, &session.State{Version: session.CurrentHistoryVersion}); err != nil {
		t.Fatal(err)
	}

	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	testHistoryLockWaiter(t, ctx, program, statePath)

	const processCount = 4

	var wg sync.WaitGroup

	failures := make(chan error, processCount)
	for p := range processCount {
		wg.Go(func() {
			worker := exec.CommandContext(ctx, program, "-test.run=^TestHistoryProcessWorker$")
			worker.WaitDelay = time.Second

			worker.Env = append(os.Environ(), envLockWorkerFlag+"=worker", envWorkerID+"="+strconv.Itoa(p), envStatePath+"="+statePath)

			output, err := worker.CombinedOutput()
			if err != nil {
				failures <- fmt.Errorf("history worker failed: %w: %s", err, output)
			}
		})
	}

	wg.Wait()
	close(failures)

	for err := range failures {
		t.Error(err)
	}

	if t.Failed() {
		return
	}

	final, err := svc.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}

	if len(final.SwitchHistory) != processCount*historyProcessIterations {
		t.Fatalf("lost or extra updates: %d", len(final.SwitchHistory))
	}

	for p := range processCount {
		for i := range historyProcessIterations {
			name := fmt.Sprintf("proc-%d-%d", p, i)
			if final.GetLastSwitch(session.Identity{Name: name}) == 0 {
				t.Fatalf("missing worker update: %s", name)
			}
		}
	}
}

func TestCatalogSharedPaneDoesNotGuessCurrentSession(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	first, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "group-one"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "group-two", Group: "group-one"}); err != nil {
		t.Fatal(err)
	}

	panes, err := first.Panes(ctx)
	if err != nil || len(panes) != 1 {
		t.Fatalf("fixture panes: %v, %d", err, len(panes))
	}

	result, err := session.ListCatalog(ctx, session.CatalogOptions{Server: server, CurrentPane: string(panes[0].ID)})
	if !errors.Is(err, session.ErrCurrentSessionAmbiguous) || result.Status != session.StatusPartial || len(result.Sessions) < 2 {
		t.Fatalf("shared pane uncertainty hidden: %+v, %v", result, err)
	}

	for _, item := range result.Sessions {
		if item.IsCurrent {
			t.Fatal("guessed current session from shared pane")
		}
	}
}

func testHistoryLockWaiter(t *testing.T, ctx context.Context, program, statePath string) {
	t.Helper()

	dir, err := privatefs.Open(filepath.Dir(statePath), false)
	if err != nil {
		t.Fatal(err)
	}

	lock, lockErr := dir.Lock(ctx, filepath.Base(statePath)+".lock")

	closeErr := dir.Close()
	if err := errors.Join(lockErr, closeErr); err != nil {
		if lock != nil {
			err = errors.Join(err, lock.Close())
		}

		t.Fatal(err)
	}

	cmd := exec.CommandContext(ctx, program, "-test.run=^TestHistoryProcessWorker$")
	cmd.WaitDelay = time.Second

	cmd.Env = append(os.Environ(), envLockWorkerFlag+"=cancel_waiter", envStatePath+"="+statePath)

	output, runErr := cmd.CombinedOutput()
	if err := errors.Join(runErr, lock.Close()); err != nil {
		t.Fatalf("waiter failed: %v, %s", err, output)
	}
}
