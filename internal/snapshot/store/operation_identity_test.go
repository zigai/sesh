//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestRegisterOperationExactRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	opID := newID()
	target := model.Bytes("session-target")
	actions := []Action{
		{ID: "act1", Kind: actionLaunch, State: actionPlanned},
		{ID: "act2", Kind: "configure", State: actionPlanned},
	}

	first, err := r.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatalf("first registration failed: %v", err)
	}

	if first.ID != opID || first.State != "active" {
		t.Fatalf("unexpected first operation: %+v", first)
	}

	second, err := r.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatalf("second registration failed: %v", err)
	}

	if diff := cmp.Diff(first, second, cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("idempotent retry produced different operation (-first +second):\n%s", diff)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Operations) != 1 {
		t.Fatalf("expected 1 operation in catalog, got %d", len(catalog.Operations))
	}

	if catalog.Operations[0].ID != opID {
		t.Fatalf("expected catalog operation ID %s, got %s", opID, catalog.Operations[0].ID)
	}
}

func TestRegisterOperationConflictInEachImmutableField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source1 := commitFixture(t, r)
	source2 := commitFixture(t, r)

	baseID := newID()
	baseTarget := model.Bytes("target-1")
	baseActions := []Action{
		{ID: "act-1", Kind: actionLaunch, State: actionPlanned},
		{ID: "act-2", Kind: "configure", State: actionPlanned},
	}

	initial, err := r.RegisterOperation(t.Context(), baseID, source1.ID, baseTarget, baseActions)
	if err != nil {
		t.Fatalf("initial registration failed: %v", err)
	}

	if initial.ID != baseID {
		t.Fatalf("unexpected operation ID: %s", initial.ID)
	}

	cases := []struct {
		name    string
		source  string
		target  model.Bytes
		actions []Action
	}{
		{
			name:    "different source",
			source:  source2.ID,
			target:  baseTarget,
			actions: baseActions,
		},
		{
			name:    "different target bytes",
			source:  source1.ID,
			target:  model.Bytes("target-different"),
			actions: baseActions,
		},
		{
			name:   "different action ID",
			source: source1.ID,
			target: baseTarget,
			actions: []Action{
				{ID: "act-different", Kind: actionLaunch, State: actionPlanned},
				{ID: "act-2", Kind: "configure", State: actionPlanned},
			},
		},
		{
			name:   "different action Kind",
			source: source1.ID,
			target: baseTarget,
			actions: []Action{
				{ID: "act-1", Kind: "link", State: actionPlanned},
				{ID: "act-2", Kind: "configure", State: actionPlanned},
			},
		},
		{
			name:   "different action order",
			source: source1.ID,
			target: baseTarget,
			actions: []Action{
				{ID: "act-2", Kind: "configure", State: actionPlanned},
				{ID: "act-1", Kind: actionLaunch, State: actionPlanned},
			},
		},
		{
			name:   "fewer actions",
			source: source1.ID,
			target: baseTarget,
			actions: []Action{
				{ID: "act-1", Kind: actionLaunch, State: actionPlanned},
			},
		},
		{
			name:   "more actions",
			source: source1.ID,
			target: baseTarget,
			actions: []Action{
				{ID: "act-1", Kind: actionLaunch, State: actionPlanned},
				{ID: "act-2", Kind: "configure", State: actionPlanned},
				{ID: "act-3", Kind: "verify", State: actionPlanned},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.RegisterOperation(t.Context(), baseID, tc.source, tc.target, tc.actions)
			assertRepositoryDiagnostic(t, err, "register_operation", "repository_conflict", ErrConflict)
		})
	}
}

func TestRegisterOperationProgressedActionsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	opID := newID()
	target := model.Bytes("session-progress")
	plannedActions := []Action{
		{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned},
		{ID: "config", Kind: "configure", State: actionPlanned},
	}

	op, err := r.RegisterOperation(t.Context(), opID, source.ID, target, plannedActions)
	if err != nil {
		t.Fatal(err)
	}

	runtimeLaunch := model.Bytes("tmux-%1")
	if err := r.Transition(t.Context(), op.ID, actionLaunch, actionPlanned, actionIssued, runtimeLaunch); err != nil {
		t.Fatal(err)
	}

	if err := r.Transition(t.Context(), op.ID, actionLaunch, actionIssued, actionUnknown, runtimeLaunch); err != nil {
		t.Fatal(err)
	}

	runtimeConfig := model.Bytes("cfg-%2")
	if err := r.Transition(t.Context(), op.ID, "config", actionPlanned, actionIssued, runtimeConfig); err != nil {
		t.Fatal(err)
	}

	reopened := openRepository(t, path)

	retried, err := reopened.RegisterOperation(t.Context(), opID, source.ID, target, plannedActions)
	if err != nil {
		t.Fatalf("idempotent retry after progression failed: %v", err)
	}

	assertProgressedOperation(t, retried, opID, runtimeLaunch, runtimeConfig)

	catalog, err := reopened.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Operations) != 1 {
		t.Fatalf("expected 1 operation in catalog, got %d", len(catalog.Operations))
	}
}

func assertProgressedOperation(t *testing.T, op Operation, opID string, runtimeLaunch, runtimeConfig []byte) {
	t.Helper()

	if op.ID != opID {
		t.Fatalf("wrong operation ID: %s", op.ID)
	}

	if op.State != "active" {
		t.Fatalf("expected state active, got: %s", op.State)
	}

	if len(op.Actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(op.Actions))
	}

	if op.Actions[0].State != actionUnknown || !bytes.Equal(op.Actions[0].RuntimeID, runtimeLaunch) {
		t.Fatalf("launch action was reset or lost runtime ID: %+v", op.Actions[0])
	}

	if op.Actions[1].State != actionIssued || !bytes.Equal(op.Actions[1].RuntimeID, runtimeConfig) {
		t.Fatalf("config action was reset or lost runtime ID: %+v", op.Actions[1])
	}
}

func TestRegisterOperationCompletedRetryAfterSourcePruned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)
	commitFixture(t, r)

	opID := newID()
	target := model.Bytes("target-completed")
	actions := []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}}

	op, err := r.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatal(err)
	}

	advanceAction(t, r, op.ID, actionVerified)

	if err := r.Finish(t.Context(), op.ID, false); err != nil {
		t.Fatalf("finish completed failed: %v", err)
	}

	if err := r.Delete(t.Context(), source.ID); err != nil {
		t.Fatalf("delete source failed: %v", err)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.generationIndex(source.ID) >= 0 {
		t.Fatal("source generation still present after delete")
	}

	reopened := openRepository(t, path)

	retried, err := reopened.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatalf("idempotent retry of completed operation failed: %v", err)
	}

	if retried.ID != opID || retried.State != "complete" {
		t.Fatalf("expected complete operation, got %+v", retried)
	}

	if len(retried.Actions) != 1 || retried.Actions[0].State != actionVerified {
		t.Fatalf("expected verified action, got %+v", retried.Actions)
	}
}

func TestRegisterOperationAbandonedRetryAfterSourcePruned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)
	commitFixture(t, r)

	opID := newID()
	target := model.Bytes("target-abandoned")
	actions := []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned}}

	op, err := r.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Finish(t.Context(), op.ID, true); err != nil {
		t.Fatalf("finish abandon failed: %v", err)
	}

	if err := r.Delete(t.Context(), source.ID); err != nil {
		t.Fatalf("delete source failed: %v", err)
	}

	reopened := openRepository(t, path)

	retried, err := reopened.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatalf("idempotent retry of abandoned operation failed: %v", err)
	}

	if retried.ID != opID || retried.State != "abandoned" {
		t.Fatalf("expected abandoned operation, got %+v", retried)
	}
}

func TestRegisterOperationConcurrentSameID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	opID := newID()
	target := model.Bytes("concurrent-target")
	actions := []Action{
		{ID: "act1", Kind: actionLaunch, State: actionPlanned},
		{ID: "act2", Kind: "configure", State: actionPlanned},
	}

	const concurrency = 10

	handles := make([]*Repository, concurrency)
	for i := range handles {
		handles[i] = openRepository(t, path)
	}

	var wg sync.WaitGroup

	errs := make([]error, concurrency)
	ops := make([]Operation, concurrency)

	for i := range concurrency {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			op, err := handles[idx].RegisterOperation(t.Context(), opID, source.ID, target, actions)
			ops[idx] = op
			errs[idx] = err
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent registration %d failed: %v", i, err)
		}

		if ops[i].ID != opID {
			t.Fatalf("concurrent registration %d got unexpected ID: %s", i, ops[i].ID)
		}

		if ops[i].State != "active" {
			t.Fatalf("concurrent registration %d got unexpected state: %s", i, ops[i].State)
		}
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Operations) != 1 {
		t.Fatalf("expected exactly 1 operation in catalog, got %d", len(catalog.Operations))
	}
}

func TestRegisterOperationInvalidIDsAndActionRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)
	validTarget := model.Bytes("valid-target")
	validActions := []Action{{ID: "act1", Kind: actionLaunch, State: actionPlanned}}

	t.Run("invalid operation IDs", func(t *testing.T) {
		invalidIDs := []string{
			"",
			"short",
			"0123456789abcdef0123456789abcdeg",
			strings.ToUpper(newID()),
			newID() + "00",
			"../escape",
		}

		for _, badID := range invalidIDs {
			op, err := r.RegisterOperation(t.Context(), badID, source.ID, validTarget, validActions)
			assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)

			if op.ID != badID {
				t.Fatalf("expected op.ID %s to be preserved on error, got %s", badID, op.ID)
			}
		}
	})

	t.Run("invalid source IDs", func(t *testing.T) {
		invalidSources := []string{
			"",
			"not-hex",
			strings.ToUpper(newID()),
			"0123456789abcdef0123456789abcdef00",
		}

		for _, badSource := range invalidSources {
			_, err := r.RegisterOperation(t.Context(), newID(), badSource, validTarget, validActions)
			assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)
		}
	})

	t.Run("empty target bytes", func(t *testing.T) {
		for _, emptyTarget := range []model.Bytes{nil, {}} {
			_, err := r.RegisterOperation(t.Context(), newID(), source.ID, emptyTarget, validActions)
			assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)
		}
	})

	t.Run("action requests with non-planned state", func(t *testing.T) {
		for _, badState := range []model.ActionState{actionIssued, actionUnknown, actionConfirmed, actionVerified} {
			badActions := []Action{{ID: "act", Kind: actionLaunch, State: badState}}
			_, err := r.RegisterOperation(t.Context(), newID(), source.ID, validTarget, badActions)
			assertRepositoryDiagnostic(t, err, "register_operation", "repository_conflict", ErrConflict)
		}
	})

	t.Run("action requests with non-empty runtime ID", func(t *testing.T) {
		badActions := []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned, RuntimeID: model.Bytes("pre-assigned")}}
		_, err := r.RegisterOperation(t.Context(), newID(), source.ID, validTarget, badActions)
		assertRepositoryDiagnostic(t, err, "register_operation", "repository_conflict", ErrConflict)
	})

	t.Run("duplicate action IDs", func(t *testing.T) {
		badActions := []Action{
			{ID: "same-id", Kind: actionLaunch, State: actionPlanned},
			{ID: "same-id", Kind: "configure", State: actionPlanned},
		}
		_, err := r.RegisterOperation(t.Context(), newID(), source.ID, validTarget, badActions)
		assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)
	})

	t.Run("invalid action ID characters", func(t *testing.T) {
		badActions := []Action{{ID: "not/valid/id", Kind: actionLaunch, State: actionPlanned}}
		_, err := r.RegisterOperation(t.Context(), newID(), source.ID, validTarget, badActions)
		assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)
	})

	t.Run("invalid action kind", func(t *testing.T) {
		badActions := []Action{{ID: "act", Kind: "invalid-kind", State: actionPlanned}}
		_, err := r.RegisterOperation(t.Context(), newID(), source.ID, validTarget, badActions)
		assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)
	})

	t.Run("too many actions", func(t *testing.T) {
		badActions := make([]Action, model.MaxEntities+1)
		for i := range badActions {
			badActions[i] = Action{ID: newID(), Kind: actionLaunch, State: actionPlanned}
		}

		_, err := r.RegisterOperation(t.Context(), newID(), source.ID, validTarget, badActions)
		assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)
	})

	t.Run("oversized target bytes", func(t *testing.T) {
		hugeTarget := make(model.Bytes, maxTargetBytes+1)
		_, err := r.RegisterOperation(t.Context(), newID(), source.ID, hugeTarget, validActions)
		assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)
	})

	t.Run("invalid request returns caller ID without unbounded cloning", func(t *testing.T) {
		badID := "not-valid-id"
		op, err := r.RegisterOperation(t.Context(), badID, source.ID, validTarget, validActions)
		assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)

		if op.ID != badID {
			t.Fatalf("expected caller ID preserved, got: %s", op.ID)
		}

		if len(op.Target) != 0 || len(op.Actions) != 0 {
			t.Fatalf("expected empty/nil target and actions on validation error, got: %+v", op)
		}
	})
}

func TestRegisterOperationCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	opID := newID()
	target := model.Bytes("cancel-target")
	actions := []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned}}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	op, err := r.RegisterOperation(ctx, opID, source.ID, target, actions)
	assertRepositoryDiagnostic(t, err, "register_operation", "operation_canceled", context.Canceled)

	if op.ID != opID {
		t.Fatalf("expected preserved op.ID %s, got %s", opID, op.ID)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Operations) != 0 {
		t.Fatalf("expected 0 operations, got %d", len(catalog.Operations))
	}
}

func TestRegisterOperationNoOpNoPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	opID := newID()
	target := model.Bytes("noop-target")
	actions := []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned}}

	_, err := r.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatal(err)
	}

	catalogPath := filepath.Join(path, "catalog.json")

	beforeBytes, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}

	r.fault = func(phase string) error {
		if phase == faultCatalogSynced || phase == faultCatalogReplaced {
			return errInjected
		}

		return nil
	}

	reopened := openRepository(t, path)
	reopened.fault = r.fault

	retried, err := reopened.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatalf("idempotent registration failed despite being a no-op: %v", err)
	}

	if retried.ID != opID {
		t.Fatalf("wrong operation ID: %s", retried.ID)
	}

	afterBytes, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(beforeBytes, afterBytes); diff != "" {
		t.Fatalf("no-op idempotent registration modified catalog file: %s", diff)
	}
}

func TestRegisterOperationMutationDoesNotChangeJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	opID := newID()
	inputTarget := model.Bytes("original-target-bytes")
	inputActions := []Action{{ID: "act1", Kind: actionLaunch, State: actionPlanned}}

	op, err := r.RegisterOperation(t.Context(), opID, source.ID, inputTarget, inputActions)
	if err != nil {
		t.Fatal(err)
	}

	inputTarget[0] = 'Z'
	inputActions[0].ID = "tampered-id"
	inputActions[0].Kind = "destroy"

	op.Target[0] = 'X'
	op.Actions[0].ID = "mutated-id"
	op.Actions[0].Kind = "verify"

	reconciled, err := r.ReconcileOperation(t.Context(), opID)
	if err != nil {
		t.Fatal(err)
	}

	if string(reconciled.Target) != "original-target-bytes" {
		t.Fatalf("journal target was mutated: %s", reconciled.Target)
	}

	if len(reconciled.Actions) != 1 || reconciled.Actions[0].ID != "act1" || reconciled.Actions[0].Kind != actionLaunch {
		t.Fatalf("journal action was mutated: %+v", reconciled.Actions)
	}

	r.fault = func(phase string) error {
		if phase == faultCatalogSynced {
			return errInjected
		}

		return nil
	}

	errTarget := model.Bytes("err-target")
	errActions := []Action{{ID: "act-err", Kind: actionLaunch, State: actionPlanned}}

	failedOp, err := r.RegisterOperation(t.Context(), newID(), source.ID, errTarget, errActions)
	if !errors.Is(err, errInjected) {
		t.Fatalf("expected errInjected, got %v", err)
	}

	failedOp.Target[0] = 'Q'
	failedOp.Actions[0].ID = "changed"

	if string(errTarget) != "err-target" || errActions[0].ID != "act-err" {
		t.Fatal("failed registration mutated caller input slices")
	}
}

func TestRegisterOperationCatalogTamper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	writeCatalogCorruption(t, path, []byte(`{"version":999}`))

	opID := newID()
	target := model.Bytes("tamper-target")
	actions := []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned}}

	_, err := r.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)
}

func TestRegisterOperationRootSyncFailurePreservesUncertainty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	opID := newID()
	target := model.Bytes("sync-target")
	actions := []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned}}

	op, err := r.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatal(err)
	}

	cause := &os.PathError{Op: "sync", Path: "/private/repository", Err: os.ErrPermission}
	r.fault = func(phase string) error {
		if phase == faultOperationReconcileSync {
			return cause
		}

		return nil
	}

	retried, err := r.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	assertRepositoryDiagnostic(t, err, "register_operation", "publication_uncertain", ErrUncertain)

	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected permission error cause preserved, got %v", err)
	}

	if retried.ID != opID {
		t.Fatalf("expected preserved op.ID %s on uncertain failure, got %s", opID, retried.ID)
	}

	if retried.State != op.State {
		t.Fatalf("expected preserved state %s, got %s", op.State, retried.State)
	}
}

func TestRegisterOperationBoundsBeforeLockAndSourceReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)
	validTarget := model.Bytes("valid-target")
	validActions := []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned}}

	lock, err := r.root.Lock(t.Context(), "repository.lock")
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := lock.Close(); err != nil {
			t.Error(err)
		}
	}()

	hugeTarget := make(model.Bytes, maxTargetBytes+1)
	_, err = r.RegisterOperation(t.Context(), newID(), source.ID, hugeTarget, validActions)
	assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)

	_, err = r.RegisterOperation(t.Context(), newID(), "0123456789abcdef0123456789abcdef", hugeTarget, validActions)
	assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)

	excessiveActions := make([]Action, model.MaxEntities+1)
	for i := range excessiveActions {
		excessiveActions[i] = Action{ID: newID(), Kind: actionLaunch, State: actionPlanned}
	}

	_, err = r.RegisterOperation(t.Context(), newID(), source.ID, validTarget, excessiveActions)
	assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)

	runtimeActions := []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned, RuntimeID: model.Bytes("assigned")}}
	_, err = r.RegisterOperation(t.Context(), newID(), source.ID, validTarget, runtimeActions)
	assertRepositoryDiagnostic(t, err, "register_operation", "repository_conflict", ErrConflict)

	_, err = r.RegisterOperation(t.Context(), "invalid-id", source.ID, validTarget, validActions)
	assertRepositoryDiagnostic(t, err, "register_operation", "repository_corrupt", ErrCorrupt)
}

func TestRegisterOperationNoMutableAliasesOnValidFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	r.fault = func(phase string) error {
		if phase == faultCatalogSynced {
			return errInjected
		}

		return nil
	}

	inputTarget := model.Bytes("original-bytes")
	inputActions := []Action{{ID: "act1", Kind: actionLaunch, State: actionPlanned}}

	failedOp, err := r.RegisterOperation(t.Context(), newID(), source.ID, inputTarget, inputActions)
	if !errors.Is(err, errInjected) {
		t.Fatalf("expected errInjected, got: %v", err)
	}

	failedOp.Target[0] = 'X'
	failedOp.Actions[0].ID = "mutated-action"

	if string(inputTarget) != "original-bytes" {
		t.Fatalf("input target was mutated by modifying returned operation: %s", inputTarget)
	}

	if inputActions[0].ID != "act1" {
		t.Fatalf("input action ID was mutated by modifying returned operation: %s", inputActions[0].ID)
	}

	inputTarget[0] = 'Y'
	inputActions[0].ID = "input-mutated"

	if failedOp.Target[0] != 'X' {
		t.Fatalf("failedOp target was mutated by modifying input target: %s", failedOp.Target)
	}

	if failedOp.Actions[0].ID != "mutated-action" {
		t.Fatalf("failedOp action was mutated by modifying input action: %s", failedOp.Actions[0].ID)
	}
}

func TestRegisterOperationNilActionsContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)
	target := model.Bytes("nil-target")

	opNilID := newID()

	opNil, err := r.RegisterOperation(t.Context(), opNilID, source.ID, target, nil)
	if err != nil {
		t.Fatalf("nil actions registration failed: %v", err)
	}

	if opNil.Actions != nil {
		t.Fatalf("expected nil Actions slice in response, got: %+v", opNil.Actions)
	}

	reconciledNil, err := r.ReconcileOperation(t.Context(), opNilID)
	if err != nil {
		t.Fatalf("reconcile nil actions failed: %v", err)
	}

	if len(reconciledNil.Actions) != 0 {
		t.Fatalf("expected empty reconciled actions, got: %d", len(reconciledNil.Actions))
	}

	retriedNilWithEmpty, err := r.RegisterOperation(t.Context(), opNilID, source.ID, target, []Action{})
	if err != nil {
		t.Fatalf("retrying nil operation with empty actions should match intent: %v", err)
	}

	if retriedNilWithEmpty.ID != opNilID {
		t.Fatalf("wrong ID on retried nil with empty: %s", retriedNilWithEmpty.ID)
	}
}

func TestRegisterOperationEmptyActionsContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)
	target := model.Bytes("empty-target")

	opEmptyID := newID()

	opEmpty, err := r.RegisterOperation(t.Context(), opEmptyID, source.ID, target, []Action{})
	if err != nil {
		t.Fatalf("empty actions registration failed: %v", err)
	}

	if opEmpty.Actions == nil || len(opEmpty.Actions) != 0 {
		t.Fatalf("expected non-nil empty Actions slice in response, got: %+v", opEmpty.Actions)
	}

	reconciledEmpty, err := r.ReconcileOperation(t.Context(), opEmptyID)
	if err != nil {
		t.Fatalf("reconcile empty actions failed: %v", err)
	}

	if len(reconciledEmpty.Actions) != 0 {
		t.Fatalf("expected empty reconciled actions, got: %d", len(reconciledEmpty.Actions))
	}

	retriedEmptyWithNil, err := r.RegisterOperation(t.Context(), opEmptyID, source.ID, target, nil)
	if err != nil {
		t.Fatalf("retrying empty operation with nil actions should match intent: %v", err)
	}

	if retriedEmptyWithNil.ID != opEmptyID {
		t.Fatalf("wrong ID on retried empty with nil: %s", retriedEmptyWithNil.ID)
	}
}

func TestRegisterOperationCancellationAfterReconcileBoundaryPreservesUncertainty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	opID := newID()
	target := model.Bytes("cancel-boundary-target")
	actions := []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}}

	_, err := r.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())

	r.fault = func(phase string) error {
		if phase == faultOperationReconcileSync {
			cancel()
			return nil
		}

		return nil
	}

	retried, err := r.RegisterOperation(ctx, opID, source.ID, target, actions)
	assertRepositoryDiagnostic(t, err, "register_operation", "publication_uncertain", ErrUncertain)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled joined with uncertainty, got: %v", err)
	}

	if retried.ID != opID {
		t.Fatalf("expected preserved op.ID %s, got: %s", opID, retried.ID)
	}

	ctxReconcile, cancelReconcile := context.WithCancel(t.Context())
	r.fault = func(phase string) error {
		if phase == faultOperationReconcileSync {
			cancelReconcile()
			return nil
		}

		return nil
	}

	_, err = r.ReconcileOperation(ctxReconcile, opID)
	assertRepositoryDiagnostic(t, err, "reconcile_operation", "publication_uncertain", ErrUncertain)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in ReconcileOperation, got: %v", err)
	}
}

func TestRegisterOperationReconcileAdvancedJournalAfterUnknownEffect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	opID := newID()
	target := model.Bytes("unknown-effect-target")
	actions := []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}}

	op, err := r.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatal(err)
	}

	advanceAction(t, r, op.ID, actionUnknown)

	reconciled, err := r.ReconcileOperation(t.Context(), opID)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if reconciled.Actions[0].State != actionUnknown {
		t.Fatalf("reconciled action was reset to %s instead of unknown", reconciled.Actions[0].State)
	}

	retried, err := r.RegisterOperation(t.Context(), opID, source.ID, target, actions)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}

	if retried.Actions[0].State != actionUnknown {
		t.Fatalf("retried action reported planned success instead of unknown: %s", retried.Actions[0].State)
	}

	if retried.State != "active" {
		t.Fatalf("expected active state, got: %s", retried.State)
	}
}
