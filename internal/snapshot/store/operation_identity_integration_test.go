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
	"testing"
	"time"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/testsupport/testenv"
)

func TestProcessDeathAtOperationRegistrationBoundaries(t *testing.T) {
	for _, phase := range []string{faultCatalogSynced, faultCatalogReplaced} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			source := commitFixture(t, r)

			opID := newID()

			cmd := operationWorker(t, "register", path, opID, source.ID, phase)
			killWorkerAtBoundary(t, cmd)

			verifyCmd := operationWorker(t, "verify", path, opID, source.ID, phase)
			if output, err := verifyCmd.CombinedOutput(); err != nil {
				t.Fatalf("fresh process recovery failed: %v\n%s", err, output)
			}
		})
	}
}

func operationWorker(t *testing.T, mode, path, id, source, phase string) *exec.Cmd {
	t.Helper()

	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, program, "-test.run=^TestOperationIdentityWorker$")
	cmd.Env = []string{
		testenv.IsolatedMarker + "=1",
		"SESH_TEST_OP_WORKER=" + mode,
		"SESH_TEST_OP_ROOT=" + path,
		"SESH_TEST_OP_ID=" + id,
		"SESH_TEST_OP_SOURCE=" + source,
		"SESH_TEST_OP_PHASE=" + phase,
	}

	return cmd
}

func TestOperationIdentityWorker(t *testing.T) {
	mode := os.Getenv("SESH_TEST_OP_WORKER")
	if mode == "" {
		t.Skip("subprocess entry point")
	}

	path := os.Getenv("SESH_TEST_OP_ROOT")
	opID := os.Getenv("SESH_TEST_OP_ID")
	sourceID := os.Getenv("SESH_TEST_OP_SOURCE")
	phase := os.Getenv("SESH_TEST_OP_PHASE")

	if mode == "verify" {
		verifyOperationIdentity(t, path, opID, sourceID, phase)
		return
	}

	if mode != "register" {
		t.Fatal("invalid operation worker mode")
	}

	r := openRepository(t, path)

	ready := os.NewFile(3, "operation-boundary")

	t.Cleanup(func() {
		if err := ready.Close(); err != nil {
			t.Error(err)
		}
	})

	r.fault = func(current string) error {
		if current != phase {
			return nil
		}

		if _, err := ready.Write([]byte{1}); err != nil {
			return fmt.Errorf("signal operation boundary: %w", err)
		}

		<-t.Context().Done()

		return errInjected
	}

	target := model.Bytes("integration-target")
	actions := []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}}

	if _, err := r.RegisterOperation(t.Context(), opID, sourceID, target, actions); err != nil {
		t.Fatal(err)
	}

	t.Fatal("operation worker escaped interruption")
}

func verifyOperationIdentity(t *testing.T, path, opID, sourceID, phase string) {
	t.Helper()

	r := openRepository(t, path)

	target := model.Bytes("integration-target")
	actions := []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}}

	switch phase {
	case faultCatalogSynced:
		verifyPreReplacement(t, r, opID, sourceID, target, actions)
	case faultCatalogReplaced:
		verifyPostReplacement(t, r, opID, sourceID, target, actions)
	}

	verifyJournalAndPinRetained(t, r, opID, sourceID, target)
}

func verifyPreReplacement(t *testing.T, r *Repository, opID, sourceID string, target model.Bytes, actions []Action) {
	t.Helper()

	_, err := r.ReconcileOperation(t.Context(), opID)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable before replacement, got: %v", err)
	}

	registered, err := r.RegisterOperation(t.Context(), opID, sourceID, target, actions)
	if err != nil {
		t.Fatalf("retry registration after kill failed: %v", err)
	}

	if registered.ID != opID {
		t.Fatalf("wrong operation ID: %s", registered.ID)
	}
}

func verifyPostReplacement(t *testing.T, r *Repository, opID, sourceID string, target model.Bytes, actions []Action) {
	t.Helper()

	reconciled, err := r.ReconcileOperation(t.Context(), opID)
	if err != nil {
		t.Fatalf("reconciliation after catalog replacement failed: %v", err)
	}

	if reconciled.ID != opID {
		t.Fatalf("wrong reconciled ID: %s", reconciled.ID)
	}

	retried, err := r.RegisterOperation(t.Context(), opID, sourceID, target, actions)
	if err != nil {
		t.Fatalf("idempotent retry after kill failed: %v", err)
	}

	if retried.ID != opID {
		t.Fatalf("wrong retried ID: %s", retried.ID)
	}
}

func verifyJournalAndPinRetained(t *testing.T, r *Repository, opID, sourceID string, target model.Bytes) {
	t.Helper()

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	matchingOps := 0

	for _, op := range catalog.Operations {
		if op.ID == opID {
			matchingOps++

			if op.State != "active" {
				t.Fatalf("unexpected state: %s", op.State)
			}

			if len(op.Actions) != 1 || op.Actions[0].State != actionPlanned {
				t.Fatalf("actions replayed or corrupted: %+v", op.Actions)
			}

			if !bytes.Equal(op.Target, target) {
				t.Fatalf("target corrupted: %s", op.Target)
			}
		}
	}

	if matchingOps != 1 {
		t.Fatalf("expected exactly 1 journal entry for %s, got %d", opID, matchingOps)
	}

	commitFixture(t, r)

	if err := r.Delete(t.Context(), sourceID); !errors.Is(err, ErrConflict) {
		t.Fatalf("source pin was not retained: %v", err)
	}
}
