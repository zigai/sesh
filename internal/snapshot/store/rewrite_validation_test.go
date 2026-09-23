//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRewriteDoesNotOverrideLatestSaveOutcome(t *testing.T) {
	for _, state := range []string{"pending", "failed", "incomplete"} {
		t.Run(state, func(t *testing.T) {
			r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
			source := commitMultiSessionFixture(t, r)
			applySaveOutcome(t, r, state)

			if _, err := r.RemoveSessions(t.Context(), newID(), source.ID, []string{"s0"}); err != nil {
				t.Fatal(err)
			}

			if _, err := r.Current(t.Context()); !errors.Is(err, ErrRecoveryRequired) {
				t.Fatalf("rewrite bypassed latest %s save: %v", state, err)
			}
		})
	}
}

func TestIncompleteNonCurrentRewriteDoesNotBlockCurrent(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	source := commitFixture(t, r)
	current := commitMultiSessionFixture(t, r)

	if _, err := r.RemoveSessions(t.Context(), newID(), source.ID, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Current(t.Context()); err != nil {
		t.Fatalf("historical rewrite blocked current %s: %v", current.ID, err)
	}
}

func TestRewriteReconciliationConfirmsEveryLookup(t *testing.T) {
	for _, outcome := range []string{"absent", "pending", "completed"} {
		t.Run(outcome, func(t *testing.T) {
			r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
			source := commitMultiSessionFixture(t, r)
			id := newID()
			registerRewriteOutcome(t, r, source.ID, id, outcome)

			r.fault = func(phase string) error {
				if phase == "rewrite_reconcile_sync" {
					return errInjected
				}

				return nil
			}
			if _, err := r.ReconcileRewrite(t.Context(), id); !errors.Is(err, ErrUncertain) || !errors.Is(err, errInjected) {
				t.Fatalf("%s lookup did not confirm durability: %v", outcome, err)
			}
		})
	}
}

func TestRewriteReconciliationPreservesLateCancellation(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	source := commitMultiSessionFixture(t, r)

	id := newID()
	if _, err := r.RemoveSessions(t.Context(), id, source.ID, []string{"s0"}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	r.fault = func(phase string) error {
		if phase == "rewrite_reconcile_sync" {
			cancel()
		}

		return nil
	}
	if _, err := r.ReconcileRewrite(ctx, id); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUncertain) {
		t.Fatalf("lost late cancellation/uncertainty: %v", err)
	}
}

func TestRewriteCatalogRejectsContradictoryHistory(t *testing.T) {
	for _, kind := range []string{"pending_replacement_exists", "source_failed", "completed_replacement_pending", "duplicate_source", "duplicate_replacement", "pin_on_pending_source"} {
		t.Run(kind, func(t *testing.T) {
			r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
			source := commitMultiSessionFixture(t, r)
			other := commitMultiSessionFixture(t, r)

			catalog, err := r.Inspect(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			rw := Rewrite{ID: newID(), Source: source.ID, Replacement: newID(), State: rewriteStatePending, Sessions: []string{"s0"}, Artifacts: []string{}}

			switch kind {
			case "pending_replacement_exists":
				rw.Replacement = other.ID
			case "source_failed":
				catalog.Generations = catalog.Generations[1:]
				catalog.Attempts[0].State = "failed"
				catalog.Attempts[0].Reason = "save_failed"
				catalog.Attempts[0].ManifestSHA256 = ""
				rw.State = rewriteStateRetired
				rw.Replacement = ""
			case "completed_replacement_pending":
				catalog.Generations = catalog.Generations[1:]
				catalog.Sequence++
				catalog.Attempts = append(catalog.Attempts, Attempt{ID: rw.Replacement, Sequence: catalog.Sequence, State: "pending", Reason: "capture_pending"})
				rw.State = rewriteStateCompleted
			case "duplicate_source":
				catalog.Generations = catalog.Generations[1:]
				rw.State = rewriteStateRetired
				rw.Replacement = ""
				duplicate := rw
				duplicate.ID = newID()
				catalog.Rewrites = append(catalog.Rewrites, duplicate)
			case "duplicate_replacement":
				duplicate := rw
				duplicate.ID = newID()
				duplicate.Source = other.ID
				catalog.Rewrites = append(catalog.Rewrites, duplicate)
			case "pin_on_pending_source":
				catalog.Operations = append(catalog.Operations, Operation{ID: newID(), Source: source.ID, Target: []byte("synthetic"), State: "active"})
			}

			catalog.Rewrites = append(catalog.Rewrites, rw)
			if err := catalog.validate(); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("accepted %s: %v", kind, err)
			}
		})
	}
}

func TestRewriteRejectsCorruptSourceBeforeRegistration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	name := filepath.Join(path, "generations", source.ID, "artifacts", "a0.bin.gz")
	if err := os.WriteFile(name, []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.RemoveSessions(t.Context(), newID(), source.ID, []string{"s0"}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt source accepted: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	if string(before) != string(after) {
		t.Fatal("failed source verification changed catalog")
	}
}

func TestRewriteSequenceExhaustionHasNoEffects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	catalog.Sequence = ^uint64(0)
	if err := r.publish(catalog); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.RemoveSessions(t.Context(), newID(), source.ID, []string{"s0"}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("exhausted sequence: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	if string(before) != string(after) {
		t.Fatal("known exhausted sequence published pending intent")
	}

	entries, err := os.ReadDir(filepath.Join(path, "staging"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("staged despite exhausted sequence: %v %v", entries, err)
	}
}

func TestRetiringIncompleteCurrentHasNoFallback(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	commitMultiSessionFixture(t, r)

	source := commitFixture(t, r)
	if _, err := r.RemoveSessions(t.Context(), newID(), source.ID, []string{"s0"}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Current(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("retired current has no explicit absence: %v", err)
	}
}

func TestRewriteCatalogSchemaRequiresJournalField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	data, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	changed := bytes.Replace(data, []byte(`,"rewrites":[]`), nil, 1)
	if bytes.Equal(changed, data) {
		t.Fatal("fixture did not remove journal field")
	}

	writeCatalogCorruption(t, path, changed)

	if _, err := r.Inspect(t.Context()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("inferred missing native field: %v", err)
	}
}

func TestRewriteReservesCatalogCapacityBeforeEffects(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(strconv.FormatBool(empty), func(t *testing.T) {
			checkRewriteCapacity(t, empty)
		})
	}
}

func TestRewritePublicDiagnostics(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	_, err := r.RemoveSessions(t.Context(), "invalid", newID(), nil)
	assertRepositoryDiagnostic(t, err, "remove_sessions", "repository_corrupt", ErrCorrupt)
	_, err = r.ReconcileRewrite(t.Context(), newID())
	assertRepositoryDiagnostic(t, err, "reconcile_rewrite", "snapshot_unavailable", ErrUnavailable)
}

func TestRewriteDoesNotOvertakeRegisteredSave(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	source := commitMultiSessionFixture(t, r)

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	result, err := r.RemoveSessions(t.Context(), newID(), source.ID, []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.RemoveSessions(t.Context(), newID(), result.Replacement.ID, []string{"s1"}); err != nil {
		t.Fatal(err)
	}

	manifest, artifacts := multiSessionSnapshotFixture()
	if _, err := r.Commit(t.Context(), attempt.ID, manifest, artifacts); err != nil {
		t.Fatal(err)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Current != attempt.ID {
		t.Fatal("rewrite sequence displaced a newer save")
	}
}

func TestLateOlderSaveDoesNotUndoCurrentRetirement(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))

	older, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	current := commitMultiSessionFixture(t, r)
	if _, err := r.RemoveSessions(t.Context(), newID(), current.ID, []string{"s0", "s1", "s2"}); err != nil {
		t.Fatal(err)
	}

	manifest, artifacts := multiSessionSnapshotFixture()
	if _, err := r.Commit(t.Context(), older.ID, manifest, artifacts); err != nil {
		t.Fatal(err)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Current != "" {
		t.Fatal("late older save undid explicit no-current state")
	}

	if _, err := r.Read(t.Context(), older.ID); err != nil {
		t.Fatal("older explicit save was not preserved", err)
	}
}

func registerRewriteOutcome(t *testing.T, r *Repository, source, id, outcome string) {
	t.Helper()

	if outcome == "absent" {
		return
	}

	var want error
	if outcome == "pending" {
		want = errInjected
		r.fault = func(phase string) error {
			if phase == "rewrite_registered" {
				return errInjected
			}

			return nil
		}
	}

	if _, err := r.RemoveSessions(t.Context(), id, source, []string{"s0"}); !errors.Is(err, want) {
		t.Fatal("rewrite fixture outcome", err)
	}
}

func checkRewriteCapacity(t *testing.T, empty bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for len(catalog.Attempts)+len(catalog.Generations) < maxEntries-1 {
		catalog.Sequence++
		catalog.Attempts = append(catalog.Attempts, Attempt{ID: fmt.Sprintf("%032x", catalog.Sequence), Sequence: catalog.Sequence, State: "pending", Reason: "capture_pending"})
	}

	if err := r.publish(catalog); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	selected := []string{"s0"}
	if empty {
		selected = []string{"s0", "s1", "s2"}
	}

	_, err = r.RemoveSessions(t.Context(), newID(), source.ID, selected)
	if empty {
		if err != nil {
			t.Fatal("retirement should fit final journal slot", err)
		}

		return
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("replacement capacity: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(before, after) {
		t.Fatal("capacity failure changed catalog")
	}
}
