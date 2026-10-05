//go:build linux || darwin

package store

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestRetainedSourceReleasedOnlyAfterVerification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	op, err := r.Pin(t.Context(), source.ID, model.Bytes("synthetic-target"), []Action{{ID: "launch", Kind: actionLaunch, State: actionPlanned}})
	if err != nil {
		t.Fatal(err)
	}

	for range 7 {
		commitFixture(t, r)
	}

	reopened := openRepository(t, path)
	if _, err := reopened.Retain(t.Context(), minimumRetained); err != nil {
		t.Fatal(err)
	}

	if _, err := reopened.Read(t.Context(), source.ID); err != nil {
		t.Fatal("active recovery source pruned", err)
	}

	for _, step := range [][2]model.ActionState{{actionPlanned, actionIssued}, {actionIssued, actionUnknown}, {actionUnknown, actionConfirmed}, {actionConfirmed, actionVerified}} {
		if err := reopened.Transition(t.Context(), op.ID, "launch", step[0], step[1], model.Bytes("runtime-receipt")); err != nil {
			t.Fatal(err)
		}
	}

	if err := reopened.Finish(t.Context(), op.ID, false); err != nil {
		t.Fatal(err)
	}

	removed, err := reopened.Retain(t.Context(), minimumRetained)
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff([]string{source.ID}, removed); diff != "" {
		t.Fatal(diff)
	}

	if _, err := reopened.Read(t.Context(), source.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatal("retired generation still selectable", err)
	}

	if _, err := os.Stat(filepath.Join(path, "generations", source.ID, "manifest.json")); err != nil {
		t.Fatal("recovery bytes destroyed", err)
	}
}

func TestRetainRejectsCountBelowMinimum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	for range 6 {
		commitFixture(t, r)
	}

	cases := []int{0, 1, 4, -1}
	for _, count := range cases {
		removed, err := r.Retain(t.Context(), count)
		assertRepositoryDiagnostic(t, err, "retain", "repository_conflict", ErrConflict)

		if len(removed) != 0 {
			t.Fatalf("expected nil removed on conflict error, got %v", removed)
		}
	}
}

func TestRetainPublicationFaultDoesNotReportRemovals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	for range 8 {
		commitFixture(t, r)
	}

	r.fault = func(phase string) error {
		if phase == faultCatalogReplaced {
			return errInjected
		}

		return nil
	}

	removed, err := r.Retain(t.Context(), minimumRetained)
	assertRepositoryDiagnostic(t, err, "retain", "publication_uncertain", ErrUncertain)

	if len(removed) != 0 {
		t.Fatalf("expected nil removed on uncertain publication, got %v", removed)
	}
}

func TestDeleteRejectsCurrentOrPinned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	gen1 := commitFixture(t, r)
	gen2 := commitFixture(t, r)

	err := r.Delete(t.Context(), gen2.ID)
	assertRepositoryDiagnostic(t, err, "delete", "repository_conflict", ErrConflict)

	op, err := r.Pin(t.Context(), gen1.ID, model.Bytes("target"), []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned}})
	if err != nil {
		t.Fatal(err)
	}

	err = r.Delete(t.Context(), gen1.ID)
	assertRepositoryDiagnostic(t, err, "delete", "repository_conflict", ErrConflict)

	err = r.Delete(t.Context(), "0123456789abcdef0123456789abcdef")
	assertRepositoryDiagnostic(t, err, "delete", "snapshot_unavailable", ErrUnavailable)

	if err := r.Finish(t.Context(), op.ID, true); err != nil {
		t.Fatal(err)
	}

	if err := r.Delete(t.Context(), gen1.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Read(t.Context(), gen1.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable after delete, got %v", err)
	}
}

func compactHistoryThroughRetention(t *testing.T, r *Repository) Catalog {
	t.Helper()

	if _, err := r.RetainPolicy(t.Context(), time.Time{}, RetentionPolicy{MaxAge: 0, MaxCount: 0, MaxBytes: 0}); err != nil {
		t.Fatal(err)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return catalog
}

func failedAttempt(t *testing.T, r *Repository) string {
	t.Helper()

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Fail(t.Context(), attempt.ID); err != nil {
		t.Fatal(err)
	}

	return attempt.ID
}

func attemptIDs(catalog Catalog) []string {
	ids := make([]string, 0, len(catalog.Attempts))
	for _, attempt := range catalog.Attempts {
		ids = append(ids, attempt.ID)
	}

	return ids
}

func TestRetentionCompactsHistoryOfCollectedGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	failedBeforeHistory := failedAttempt(t, r)
	generations := retireGenerations(t, r, 3)
	failedAfterCurrent := failedAttempt(t, r)
	current := generations[2]

	beforeCollection := compactHistoryThroughRetention(t, r)
	for _, retired := range generations[:2] {
		if beforeCollection.attemptIndex(retired.ID) < 0 {
			t.Fatal("retention dropped the evidence collection needs for a generation still on disk")
		}
	}

	collectGenerations(t, r)

	listing, err := r.ReadCurrentListing(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	compacted := compactHistoryThroughRetention(t, r)

	want := []string{current.ID, failedAfterCurrent}
	if diff := cmp.Diff(want, attemptIDs(compacted)); diff != "" {
		t.Fatalf("attempts after compaction (-want +got):\n%s", diff)
	}

	if slices.Contains(attemptIDs(compacted), failedBeforeHistory) {
		t.Fatal("failed attempt older than every generation survived compaction")
	}

	for _, retired := range generations[:2] {
		if _, err := os.Stat(filepath.Join(path, collectionDirectory, retired.ID+collectionRecordSuffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("settled collection record survived compaction: %v", err)
		}
	}

	after, err := r.ReadCurrentListing(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(listing, after); diff != "" {
		t.Fatalf("compaction changed the current listing:\n%s", diff)
	}

	if result := collectGenerations(t, r); len(result.Removed)+len(result.Preserved)+len(result.Quarantined) != 0 {
		t.Fatalf("collection after compaction = %+v, want no work", result)
	}
}

func TestRetentionKeepsEvidenceForDeferredAndInterruptedCollection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 3)
	interrupted, deferred := generations[0], generations[1]

	interruptCollection(t, r, collectionPhaseGenerationUnlinked)

	compacted := compactHistoryThroughRetention(t, r)
	for _, id := range []string{interrupted.ID, deferred.ID} {
		if compacted.attemptIndex(id) < 0 {
			t.Fatalf("retention dropped evidence for %s before its collection settled", id)
		}
	}

	collectGenerations(t, r)
	assertGenerationRemoved(t, path, interrupted.ID)
	assertGenerationRemoved(t, path, deferred.ID)

	compacted = compactHistoryThroughRetention(t, r)
	for _, id := range []string{interrupted.ID, deferred.ID} {
		if compacted.attemptIndex(id) >= 0 {
			t.Fatalf("settled collection of %s kept its attempt", id)
		}
	}
}

func TestRetentionKeepsReferencedPendingAndRecentAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	oldest := commitCompleteFixture(t, r)
	failedWithinHistory := failedAttempt(t, r)
	source := commitMultiSessionFixture(t, r)

	if _, err := r.RemoveSessions(t.Context(), newID(), source.ID, []string{"s0"}); err != nil {
		t.Fatal(err)
	}

	collectGenerations(t, r)
	commitCompleteFixture(t, r)

	pending, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	compacted := compactHistoryThroughRetention(t, r)
	for name, id := range map[string]string{
		"oldest generation":                 oldest.ID,
		"failed attempt within the history": failedWithinHistory,
		"rewrite source":                    source.ID,
		"pending attempt":                   pending.ID,
	} {
		if compacted.attemptIndex(id) < 0 {
			t.Fatalf("compaction removed the %s", name)
		}
	}
}
