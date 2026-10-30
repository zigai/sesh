//go:build integration && (linux || darwin)

package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/testsupport/testenv"
)

func TestProcessDeathAtPublicationBoundaries(t *testing.T) {
	for _, phase := range []string{faultStageSynced, faultGenerationPublished, faultCatalogSynced, faultCatalogReplaced} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			prior := commitFixture(t, r)

			attempt, err := r.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			killPublicationWorker(t, path, attempt.ID, phase)
			cmd := repositoryWorker(t, "verify", path, attempt.ID, phase)

			cmd.Env = append(cmd.Env, "SESH_TEST_PRIOR="+prior.ID)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fresh recovery failed: %v\n%s", err, output)
			}
		})
	}
}

func repositoryWorker(t *testing.T, mode, path, attempt, phase string) *exec.Cmd {
	t.Helper()

	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, program, "-test.run=^TestRepositoryWorker$")
	cmd.Env = []string{testenv.IsolatedMarker + "=1", "SESH_TEST_WORKER=" + mode, "SESH_TEST_ROOT=" + path, "SESH_TEST_ATTEMPT=" + attempt, "SESH_TEST_PHASE=" + phase}

	return cmd
}

func killPublicationWorker(t *testing.T, path, attempt, phase string) {
	t.Helper()
	killWorkerAtBoundary(t, repositoryWorker(t, "commit", path, attempt, phase))
}

func killWorkerAtBoundary(t *testing.T, cmd *exec.Cmd) {
	t.Helper()

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	var readerDone chan struct{}

	t.Cleanup(func() {
		if err := read.Close(); err != nil {
			t.Error(err)
		}

		if readerDone != nil {
			<-readerDone
		}
	})

	cmd.ExtraFiles = []*os.File{write}

	var output bytes.Buffer

	cmd.Stdout, cmd.Stderr = &output, &output

	startErr := cmd.Start()
	if startErr == nil {
		reapWorker(t, cmd)
	}

	if err := errors.Join(startErr, write.Close()); err != nil {
		t.Fatal(err)
	}

	ready := make(chan error, 1)
	readerDone = make(chan struct{})

	go func() {
		defer close(readerDone)

		var value [1]byte

		_, err := read.Read(value[:])
		ready <- err
	}()

	select {
	case err := <-ready:
		if err != nil {
			t.Fatal("worker failed before publication boundary", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not reach publication boundary")
	}

	killAndWaitWorker(t, cmd, &output)
}

func killAndWaitWorker(t *testing.T, cmd *exec.Cmd, output *bytes.Buffer) {
	t.Helper()

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) {
		t.Fatalf("worker was not killed: %v\n%s", err, output.String())
	}

	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("worker did not exit by SIGKILL: %v\n%s", exit, output.String())
	}
}

func TestRepositoryWorker(t *testing.T) {
	mode := os.Getenv("SESH_TEST_WORKER")
	if mode == "" {
		t.Skip("subprocess entry point")
	}

	path, attempt, phase := os.Getenv("SESH_TEST_ROOT"), os.Getenv("SESH_TEST_ATTEMPT"), os.Getenv("SESH_TEST_PHASE")
	if mode == "append" {
		r := openRepository(t, path)
		for range 20 {
			if _, err := r.Begin(t.Context()); err != nil {
				t.Fatal(err)
			}
		}

		return
	}

	if mode == "verify" {
		verifyPublication(t, path, os.Getenv("SESH_TEST_PRIOR"), attempt, phase == faultCatalogReplaced)
		return
	}

	if mode != "commit" {
		t.Fatal("invalid worker mode")
	}

	r := openRepository(t, path)

	ready := os.NewFile(3, "publication-boundary")
	defer func() {
		if err := ready.Close(); err != nil {
			t.Error(err)
		}
	}()

	r.fault = func(current string) error {
		if current != phase {
			return nil
		}

		if _, err := ready.Write([]byte{1}); err != nil {
			return fmt.Errorf("signal publication boundary: %w", err)
		}

		<-t.Context().Done()

		return errInjected
	}

	m, artifacts := snapshotFixture()
	if _, err := r.Commit(t.Context(), attempt, m, artifacts); err != nil {
		t.Fatal(err)
	}

	t.Fatal("worker returned without interruption")
}

func TestConcurrentRepositoryProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	var workers sync.WaitGroup

	for range 3 {
		cmd := repositoryWorker(t, "append", path, "", "")

		workers.Go(func() {
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("writer: %v\n%s", err, output)
			}
		})
	}

	workers.Wait()

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Attempts) != 60 || catalog.Sequence != 60 {
		t.Fatal("concurrent process update lost")
	}
}

func reapWorker(t *testing.T, cmd *exec.Cmd) {
	t.Helper()

	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Error(err)
			}

			var exit *exec.ExitError
			if err := cmd.Wait(); err != nil && !errors.As(err, &exit) {
				t.Error(err)
			}
		}
	})
}

func TestDispatchProcessDeathKeepsRecoveryPin(t *testing.T) {
	for _, phase := range []string{faultCatalogSynced, faultCatalogReplaced} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)

			op := pinnedLaunch(t, r)
			for range 6 {
				commitFixture(t, r)
			}

			cmd := repositoryWorker(t, "issue", path, op.ID, phase)
			cmd.Args[1] = "-test.run=^TestJournalWorker$"
			killWorkerAtBoundary(t, cmd)
			verify := repositoryWorker(t, "verify", path, op.ID, phase)

			verify.Args[1] = "-test.run=^TestJournalWorker$"
			if output, err := verify.CombinedOutput(); err != nil {
				t.Fatalf("fresh journal recovery: %v\n%s", err, output)
			}
		})
	}
}

func TestJournalWorker(t *testing.T) {
	mode := os.Getenv("SESH_TEST_WORKER")
	if mode == "" {
		t.Skip("subprocess entry point")
	}

	r := openRepository(t, os.Getenv("SESH_TEST_ROOT"))

	id := os.Getenv("SESH_TEST_ATTEMPT")

	if mode == "verify" {
		want := model.ActionStateIssued
		if os.Getenv("SESH_TEST_PHASE") == faultCatalogSynced {
			want = model.ActionStatePlanned
		}

		verifyJournal(t, r, id, want)

		return
	}

	if mode != "issue" {
		t.Fatal("invalid journal worker mode")
	}

	ready := os.NewFile(3, "issued-boundary")

	t.Cleanup(func() {
		if err := ready.Close(); err != nil {
			t.Error(err)
		}
	})

	r.fault = func(phase string) error {
		if phase != os.Getenv("SESH_TEST_PHASE") {
			return nil
		}

		if _, err := ready.Write([]byte{1}); err != nil {
			return fmt.Errorf("signal issued boundary: %w", err)
		}

		<-t.Context().Done()

		return errInjected
	}
	if err := r.Transition(t.Context(), id, "launch", actionPlanned, actionIssued, nil); err != nil {
		t.Fatal(err)
	}

	t.Fatal("journal worker escaped interruption")
}

func verifyJournal(t *testing.T, r *Repository, id string, want model.ActionState) {
	t.Helper()

	op, err := r.ReconcileOperation(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	if op.ID != id || op.State != "active" || len(op.Actions) != 1 || op.Actions[0].State != want {
		t.Fatal("wrong reconciled journal after process death")
	}

	if want == actionIssued {
		if err := r.Transition(t.Context(), id, "launch", actionPlanned, actionIssued, nil); !errors.Is(err, ErrConflict) {
			t.Fatal("duplicate dispatch admitted", err)
		}
	}

	if err := r.Finish(t.Context(), id, false); !errors.Is(err, ErrConflict) {
		t.Fatal("unfinished operation reported complete", err)
	}

	if _, err := r.Retain(t.Context(), minimumRetained); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Read(t.Context(), op.Source); err != nil {
		t.Fatal("interrupted operation lost its source", err)
	}
}
