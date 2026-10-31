//go:build linux || darwin

package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func commitTimedFixture(t *testing.T, r *Repository, started, finished int64) Generation {
	t.Helper()

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	m, artifacts := snapshotFixture()
	m.Started = started
	m.Finished = finished

	generation, err := r.Commit(t.Context(), attempt.ID, m, artifacts)
	if err != nil {
		t.Fatal(err)
	}

	return generation
}

func commitSnapshotAt(t *testing.T, r *Repository, finishedTime time.Time) Generation {
	t.Helper()

	var (
		finished = finishedTime.UnixNano()
		started  = finished - int64(time.Millisecond)
	)
	if started <= 0 {
		started = 1
	}

	if finished <= started {
		finished = started + 1
	}

	return commitTimedFixture(t, r, started, finished)
}

func TestRetentionPolicyCountPruning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	gens := make([]Generation, 10)
	for i := range gens {
		gens[i] = commitSnapshotAt(t, r, now.Add(time.Duration(i)*time.Minute))
	}

	result, err := r.RetainPolicy(t.Context(), now, RetentionPolicy{MaxCount: 7})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Removed) != 3 {
		t.Fatalf("expected 3 removed, got %d: %v", len(result.Removed), result.Removed)
	}

	if len(result.Retained) != 7 {
		t.Fatalf("expected 7 retained, got %d: %v", len(result.Retained), result.Retained)
	}

	wantRemoved := []string{gens[2].ID, gens[1].ID, gens[0].ID}
	if diff := cmp.Diff(wantRemoved, result.Removed); diff != "" {
		t.Fatalf("removed diff (-want +got):\n%s", diff)
	}

	reopened := openRepository(t, path)

	catalog, err := reopened.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Generations) != 7 {
		t.Fatalf("expected 7 generations in catalog, got %d", len(catalog.Generations))
	}

	for _, id := range wantRemoved {
		if _, err := reopened.Read(t.Context(), id); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable for pruned %s, got %v", id, err)
		}
	}
}

func TestRetentionPolicyAgePruning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	offsets := []time.Duration{
		24 * time.Hour,
		20 * time.Hour,
		15 * time.Hour,
		8 * time.Hour,
		6 * time.Hour,
		4 * time.Hour,
		2 * time.Hour,
		1 * time.Hour,
	}

	gens := make([]Generation, len(offsets))
	for i, offset := range offsets {
		gens[i] = commitSnapshotAt(t, r, now.Add(-offset))
	}

	policy := RetentionPolicy{MaxAge: 10 * time.Hour, MaxCount: 0}

	result, err := r.RetainPolicy(t.Context(), now, policy)
	if err != nil {
		t.Fatal(err)
	}

	wantRemoved := []string{gens[2].ID, gens[1].ID, gens[0].ID}
	if diff := cmp.Diff(wantRemoved, result.Removed); diff != "" {
		t.Fatalf("removed diff (-want +got):\n%s", diff)
	}

	wantRetained := []string{gens[7].ID, gens[6].ID, gens[5].ID, gens[4].ID, gens[3].ID}
	if diff := cmp.Diff(wantRetained, result.Retained); diff != "" {
		t.Fatalf("retained diff (-want +got):\n%s", diff)
	}

	reopened := openRepository(t, path)
	for _, id := range wantRemoved {
		if _, err := reopened.Read(t.Context(), id); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable for %s, got %v", id, err)
		}
	}

	for _, id := range wantRetained {
		if _, err := reopened.Read(t.Context(), id); err != nil {
			t.Fatalf("expected Read to succeed for %s, got %v", id, err)
		}
	}
}

func TestRetentionPolicyComposition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	durations := []time.Duration{
		30 * time.Hour,
		25 * time.Hour,
		5 * time.Hour,
		15 * time.Hour,
		4 * time.Hour,
		3 * time.Hour,
		2 * time.Hour,
		1 * time.Hour,
		30 * time.Minute,
		10 * time.Minute,
	}

	gens := make([]Generation, len(durations))
	for i, d := range durations {
		gens[i] = commitSnapshotAt(t, r, now.Add(-d))
	}

	policy := RetentionPolicy{MaxCount: 7, MaxAge: 10 * time.Hour}

	result, err := r.RetainPolicy(t.Context(), now, policy)
	if err != nil {
		t.Fatal(err)
	}

	wantRemoved := []string{gens[3].ID, gens[2].ID, gens[1].ID, gens[0].ID}
	if diff := cmp.Diff(wantRemoved, result.Removed); diff != "" {
		t.Fatalf("removed diff (-want +got):\n%s", diff)
	}

	wantRetained := []string{gens[9].ID, gens[8].ID, gens[7].ID, gens[6].ID, gens[5].ID, gens[4].ID}
	if diff := cmp.Diff(wantRetained, result.Retained); diff != "" {
		t.Fatalf("retained diff (-want +got):\n%s", diff)
	}
}

func TestRetentionExactCutoffEquality(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	maxAge := 2 * time.Hour
	cutoff := now.UTC().Add(-maxAge)
	cutoffNano := cutoff.UnixNano()

	gens := make([]Generation, 8)
	gens[0] = commitTimedFixture(t, r, cutoffNano-2000, cutoffNano-1)
	gens[1] = commitTimedFixture(t, r, cutoffNano-1000, cutoffNano)
	gens[2] = commitTimedFixture(t, r, cutoffNano, cutoffNano+1)

	for i := 3; i < 8; i++ {
		gens[i] = commitSnapshotAt(t, r, cutoff.Add(time.Duration(i)*time.Minute))
	}

	policy := RetentionPolicy{MaxAge: maxAge, MaxCount: 0}

	result, err := r.RetainPolicy(t.Context(), now, policy)
	if err != nil {
		t.Fatal(err)
	}

	wantRemoved := []string{gens[0].ID}
	if diff := cmp.Diff(wantRemoved, result.Removed); diff != "" {
		t.Fatalf("removed diff (-want +got):\n%s", diff)
	}

	if !slices.Contains(result.Retained, gens[1].ID) {
		t.Fatalf("expected exact cutoff equality generation %s to be retained", gens[1].ID)
	}

	if !slices.Contains(result.Retained, gens[2].ID) {
		t.Fatalf("expected newer generation %s to be retained", gens[2].ID)
	}
}

func TestRetentionFutureTimestampsAndClockRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	maxAge := 2 * time.Hour

	gens := make([]Generation, 8)
	gens[0] = commitSnapshotAt(t, r, now.Add(-5*time.Hour))
	gens[1] = commitSnapshotAt(t, r, now.Add(1*time.Hour))
	gens[2] = commitSnapshotAt(t, r, now.Add(2*time.Hour))

	for i := 3; i < 8; i++ {
		gens[i] = commitSnapshotAt(t, r, now.Add(-time.Duration(i)*time.Minute))
	}

	policy := RetentionPolicy{MaxAge: maxAge, MaxCount: 0}

	result, err := r.RetainPolicy(t.Context(), now, policy)
	if err != nil {
		t.Fatal(err)
	}

	wantRemoved := []string{gens[0].ID}
	if diff := cmp.Diff(wantRemoved, result.Removed); diff != "" {
		t.Fatalf("removed diff (-want +got):\n%s", diff)
	}

	if !slices.Contains(result.Retained, gens[1].ID) {
		t.Fatalf("expected future timestamp generation %s to be retained", gens[1].ID)
	}

	if !slices.Contains(result.Retained, gens[2].ID) {
		t.Fatalf("expected future timestamp generation %s to be retained", gens[2].ID)
	}
}

func TestRetentionNewestFiveBySequenceNotWallClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	gens := make([]Generation, 8)
	for i := range 3 {
		gens[i] = commitSnapshotAt(t, r, now.Add(time.Duration(i)*time.Minute))
	}

	for i := 3; i < 8; i++ {
		gens[i] = commitSnapshotAt(t, r, now.Add(-100*time.Hour).Add(time.Duration(i)*time.Minute))
	}

	policy := RetentionPolicy{MaxCount: 5, MaxAge: 1 * time.Hour}

	result, err := r.RetainPolicy(t.Context(), now, policy)
	if err != nil {
		t.Fatal(err)
	}

	wantRemoved := []string{gens[2].ID, gens[1].ID, gens[0].ID}
	if diff := cmp.Diff(wantRemoved, result.Removed); diff != "" {
		t.Fatalf("removed diff (-want +got):\n%s", diff)
	}

	wantRetained := []string{gens[7].ID, gens[6].ID, gens[5].ID, gens[4].ID, gens[3].ID}
	if diff := cmp.Diff(wantRetained, result.Retained); diff != "" {
		t.Fatalf("retained diff (-want +got):\n%s", diff)
	}
}

func TestRetentionProtectsCurrentGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	gens := make([]Generation, 8)
	for i := range gens {
		gens[i] = commitSnapshotAt(t, r, now.Add(-10*time.Hour))
	}

	err := r.update(t.Context(), func(c *Catalog) error {
		c.Current = gens[0].ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	policy := RetentionPolicy{MaxCount: 5}

	result, err := r.RetainPolicy(t.Context(), now, policy)
	if err != nil {
		t.Fatal(err)
	}

	if slices.Contains(result.Removed, gens[0].ID) {
		t.Fatalf("current generation %s was removed", gens[0].ID)
	}

	if !slices.Contains(result.Protected, gens[0].ID) {
		t.Fatalf("expected current generation %s to be reported protected", gens[0].ID)
	}

	wantRemoved := []string{gens[2].ID, gens[1].ID}
	if diff := cmp.Diff(wantRemoved, result.Removed); diff != "" {
		t.Fatalf("removed diff (-want +got):\n%s", diff)
	}
}

func TestRetentionProtectsActiveRecoveryPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	gens := make([]Generation, 8)
	for i := range gens {
		gens[i] = commitSnapshotAt(t, r, now.Add(-10*time.Hour))
	}

	op, err := r.Pin(t.Context(), gens[0].ID, model.Bytes("target"), []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned}})
	if err != nil {
		t.Fatal(err)
	}

	policy := RetentionPolicy{MaxCount: 5, MaxAge: 1 * time.Hour}

	result, err := r.RetainPolicy(t.Context(), now, policy)
	if err != nil {
		t.Fatal(err)
	}

	if slices.Contains(result.Removed, gens[0].ID) {
		t.Fatalf("pinned generation %s was pruned", gens[0].ID)
	}

	if !slices.Contains(result.Protected, gens[0].ID) {
		t.Fatalf("expected pinned generation %s to be in protected list", gens[0].ID)
	}

	if err := r.Finish(t.Context(), op.ID, true); err != nil {
		t.Fatal(err)
	}

	result2, err := r.RetainPolicy(t.Context(), now, policy)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(result2.Removed, gens[0].ID) {
		t.Fatalf("unpinned generation %s was not pruned", gens[0].ID)
	}
}

func TestRetentionPreservesLatestAttemptSafeguards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for range 6 {
		commitSnapshotAt(t, r, now)
	}

	pendingAttempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.RetainPolicy(t.Context(), now, RetentionPolicy{MaxCount: 5})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Current(t.Context()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired after retention with pending attempt, got %v", err)
	}

	if err := r.Fail(t.Context(), pendingAttempt.ID); err != nil {
		t.Fatal(err)
	}

	_, err = r.RetainPolicy(t.Context(), now, RetentionPolicy{MaxCount: 5})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.Current(t.Context()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired after retention with failed attempt, got %v", err)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	latestAttempt := catalog.Attempts[len(catalog.Attempts)-1]
	if latestAttempt.ID != pendingAttempt.ID || latestAttempt.State != "failed" {
		t.Fatalf("latest attempt was altered by retention: %+v", latestAttempt)
	}
}

func TestRetentionDisabledSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	gens := make([]Generation, 8)
	for i := range gens {
		gens[i] = commitSnapshotAt(t, r, now.Add(-10*time.Hour))
	}

	result, err := r.RetainPolicy(t.Context(), now, RetentionPolicy{})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Removed) != 0 {
		t.Fatalf("expected 0 removed with disabled policy, got %v", result.Removed)
	}

	if len(result.Retained) != 8 {
		t.Fatalf("expected all 8 retained, got %d", len(result.Retained))
	}

	result2, err := r.RetainPolicy(t.Context(), now, RetentionPolicy{MaxCount: 6, MaxAge: 0})
	if err != nil {
		t.Fatal(err)
	}

	if len(result2.Removed) != 2 {
		t.Fatalf("expected 2 removed with count=6, got %v", result2.Removed)
	}

	result3, err := r.RetainPolicy(t.Context(), now, RetentionPolicy{MaxAge: 1 * time.Hour, MaxCount: 0})
	if err != nil {
		t.Fatal(err)
	}

	if len(result3.Removed) != 1 {
		t.Fatalf("expected 1 removed by age, got %v", result3.Removed)
	}
}

func TestRetentionInvalidPolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		now    time.Time
		policy RetentionPolicy
	}{
		{"count below minimum", now, RetentionPolicy{MaxCount: 3}},
		{"negative count", now, RetentionPolicy{MaxCount: -1}},
		{"negative age", now, RetentionPolicy{MaxAge: -time.Hour}},
		{"zero time with age enabled", time.Time{}, RetentionPolicy{MaxAge: time.Hour}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.RetainPolicy(t.Context(), tc.now, tc.policy)
			assertRepositoryDiagnostic(t, err, "retain", "repository_conflict", ErrConflict)
		})
	}
}

func TestRetentionRejectsTamperedManifestDigest(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	policy := RetentionPolicy{MaxAge: 1 * time.Hour, MaxCount: 0}
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	gens := make([]Generation, 7)
	for i := range gens {
		gens[i] = commitSnapshotAt(t, r, now.Add(-5*time.Hour))
	}

	manifestPath := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "manifest.json"))

	m, err := r.Read(t.Context(), gens[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	m.Source.TmuxVersion = "3.7"

	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = r.RetainPolicy(t.Context(), now, policy)
	assertRepositoryDiagnostic(t, err, "retain", "repository_corrupt", ErrCorrupt)
}

func TestRetentionRejectsMalformedManifestJSON(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	policy := RetentionPolicy{MaxAge: 1 * time.Hour, MaxCount: 0}
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	gens := make([]Generation, 7)
	for i := range gens {
		gens[i] = commitSnapshotAt(t, r, now.Add(-5*time.Hour))
	}

	manifestPath := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "manifest.json"))
	if err := os.WriteFile(manifestPath, []byte("{malformed json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := r.RetainPolicy(t.Context(), now, policy)
	assertRepositoryDiagnostic(t, err, "retain", "repository_corrupt", ErrCorrupt)
}

func TestRetentionRejectsInvalidStartedTimestamp(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	policy := RetentionPolicy{MaxAge: 1 * time.Hour, MaxCount: 0}
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	gens := make([]Generation, 7)
	for i := range gens {
		gens[i] = commitSnapshotAt(t, r, now.Add(-5*time.Hour))
	}

	manifestPath := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "manifest.json"))

	m, err := r.Read(t.Context(), gens[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	m.Started = 0

	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = r.RetainPolicy(t.Context(), now, policy)
	assertRepositoryDiagnostic(t, err, "retain", "repository_corrupt", ErrCorrupt)
}

func TestRetentionRejectsInvalidFinishedTimestamp(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	policy := RetentionPolicy{MaxAge: 1 * time.Hour, MaxCount: 0}
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	gens := make([]Generation, 7)
	for i := range gens {
		gens[i] = commitSnapshotAt(t, r, now.Add(-5*time.Hour))
	}

	manifestPath := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "manifest.json"))

	m, err := r.Read(t.Context(), gens[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	m.Finished = m.Started - 1

	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = r.RetainPolicy(t.Context(), now, policy)
	assertRepositoryDiagnostic(t, err, "retain", "repository_corrupt", ErrCorrupt)
}

func TestRetentionRejectsCompletenessMismatch(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	policy := RetentionPolicy{MaxAge: 1 * time.Hour, MaxCount: 0}
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	gens := make([]Generation, 7)
	for i := range gens {
		gens[i] = commitCompleteFixture(t, r)
	}

	manifestPath := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "manifest.json"))

	m, err := r.Read(t.Context(), gens[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	m.Source.Consistency = "observed"

	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	newDigest := digest(encoded)

	err = r.update(t.Context(), func(c *Catalog) error {
		idx := c.generationIndex(gens[0].ID)
		c.Generations[idx].ManifestSHA256 = newDigest
		c.Attempts[c.attemptIndex(gens[0].ID)].ManifestSHA256 = newDigest

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.RetainPolicy(t.Context(), now, policy)
	assertRepositoryDiagnostic(t, err, "retain", "repository_corrupt", ErrCorrupt)
}

func TestRetentionRejectsMissingManifest(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	policy := RetentionPolicy{MaxAge: 1 * time.Hour, MaxCount: 0}
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	gens := make([]Generation, 7)
	for i := range gens {
		gens[i] = commitSnapshotAt(t, r, now.Add(-5*time.Hour))
	}

	manifestPath := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "manifest.json"))
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}

	_, err := r.RetainPolicy(t.Context(), now, policy)
	assertRepositoryDiagnostic(t, err, "retain", "repository_corrupt", ErrCorrupt)
}

func TestRetentionPublicationFaultPreservesUncertainty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for range 8 {
		commitSnapshotAt(t, r, now.Add(-10*time.Hour))
	}

	r.fault = func(phase string) error {
		if phase == faultCatalogReplaced {
			return errInjected
		}

		return nil
	}

	result, err := r.RetainPolicy(t.Context(), now, RetentionPolicy{MaxCount: 5})
	assertRepositoryDiagnostic(t, err, "retain", "publication_uncertain", ErrUncertain)

	if len(result.Removed) != 0 {
		t.Fatalf("proposed removals reported on uncertain publication: %v", result.Removed)
	}
}

func TestRetentionCancellationDoesNotReportRemovals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for range 8 {
		commitSnapshotAt(t, r, now.Add(-10*time.Hour))
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	result, err := r.RetainPolicy(ctx, now, RetentionPolicy{MaxCount: 5})
	assertRepositoryDiagnostic(t, err, "retain", "operation_canceled", context.Canceled)

	if len(result.Removed) != 0 {
		t.Fatalf("removals reported on canceled operation: %v", result.Removed)
	}
}

func TestRetentionCatalogMembershipOnlyNoPhysicalDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	gens := make([]Generation, 8)
	for i := range gens {
		gens[i] = commitSnapshotAt(t, r, now.Add(-10*time.Hour))
	}

	result, err := r.RetainPolicy(t.Context(), now, RetentionPolicy{MaxCount: 5, MaxAge: 0})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Removed) != 3 {
		t.Fatalf("expected 3 removed, got %d", len(result.Removed))
	}

	reopened := openRepository(t, path)

	catalog, err := reopened.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range result.Removed {
		if catalog.generationIndex(id) >= 0 {
			t.Fatalf("pruned generation %s still in catalog", id)
		}

		if _, err := reopened.Read(t.Context(), id); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable for %s, got %v", id, err)
		}

		manifestPath := filepath.Join(path, "generations", id, "manifest.json")
		if _, err := os.Stat(manifestPath); err != nil {
			t.Fatalf("quarantined manifest destroyed on disk: %v", err)
		}

		artifactsDir := filepath.Join(path, "generations", id, "artifacts")
		if _, err := os.Stat(artifactsDir); err != nil {
			t.Fatalf("quarantined artifacts destroyed on disk: %v", err)
		}
	}
}

func TestConcurrentRetentionPinDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for range 8 {
		commitSnapshotAt(t, r, now)
	}

	var wg sync.WaitGroup

	ctx := t.Context()

	wg.Go(func() {
		for range 15 {
			_, _ = r.RetainPolicy(ctx, now, RetentionPolicy{MaxCount: 5, MaxAge: 0})
		}
	})

	wg.Go(func() {
		for range 5 {
			attempt, err := r.Begin(ctx)
			if err != nil {
				return
			}

			m, artifacts := snapshotFixture()
			_, _ = r.Commit(ctx, attempt.ID, m, artifacts)
		}
	})

	wg.Go(func() {
		for range 5 {
			catalog, err := r.Inspect(ctx)
			if err != nil || len(catalog.Generations) == 0 {
				continue
			}

			source := catalog.Generations[0].ID

			op, err := r.Pin(ctx, source, model.Bytes("target"), []Action{{ID: "act", Kind: actionLaunch, State: actionPlanned}})
			if err != nil {
				continue
			}

			_ = r.Finish(ctx, op.ID, true)
		}
	})
	wg.Wait()

	catalog, err := r.Inspect(ctx)
	if err != nil {
		t.Fatalf("catalog corrupt after concurrent operations: %v", err)
	}

	if len(catalog.Generations) < minimumRetained {
		t.Fatalf("retention pruned below minimumRetained: %d", len(catalog.Generations))
	}
}

func TestRetentionNoOpDoesNotPublishOrReplaceCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for range 5 {
		commitSnapshotAt(t, r, now)
	}

	catalogBefore, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	r.fault = func(phase string) error {
		if phase == faultCatalogSynced || phase == faultCatalogReplaced {
			return errInjected
		}

		return nil
	}

	for _, policy := range []RetentionPolicy{
		{MaxCount: 0, MaxAge: 0}, {MaxCount: 5, MaxAge: 0}, {MaxCount: 10, MaxAge: 24 * time.Hour},
	} {
		result, err := r.RetainPolicy(t.Context(), now, policy)
		if err != nil {
			t.Fatalf("no-op retention: %v", err)
		}

		if len(result.Removed) != 0 {
			t.Fatalf("unexpected removals: %v", result.Removed)
		}
	}

	catalogAfter, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalogAfter.Sequence != catalogBefore.Sequence {
		t.Fatalf("catalog sequence incremented during no-op retention: before %d, after %d",
			catalogBefore.Sequence, catalogAfter.Sequence)
	}
}

func TestRetentionEmptyRepositoryDoesNotPublish(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	emptyPath := filepath.Join(t.TempDir(), "empty-repo")
	emptyRepo := openRepository(t, emptyPath)
	emptyRepo.fault = func(phase string) error {
		return errInjected
	}

	emptyResult, err := emptyRepo.RetainPolicy(t.Context(), now, RetentionPolicy{MaxCount: 5, MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("expected no error on empty repository no-op, got %v", err)
	}

	if len(emptyResult.Removed) != 0 || len(emptyResult.Retained) != 0 {
		t.Fatalf("expected empty result on empty repo, got %+v", emptyResult)
	}
}

func TestRetentionFutureTimestampSubjectToCountPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	gens := make([]Generation, 8)

	gens[0] = commitSnapshotAt(t, r, now.Add(5*time.Hour))
	gens[1] = commitSnapshotAt(t, r, now.Add(4*time.Hour))

	gens[2] = commitSnapshotAt(t, r, now.Add(3*time.Hour))
	for i := 3; i < 8; i++ {
		gens[i] = commitSnapshotAt(t, r, now.Add(time.Duration(i)*time.Minute))
	}

	policy := RetentionPolicy{MaxCount: 6, MaxAge: 1 * time.Hour}

	result, err := r.RetainPolicy(t.Context(), now, policy)
	if err != nil {
		t.Fatal(err)
	}

	wantRemoved := []string{gens[1].ID, gens[0].ID}
	if diff := cmp.Diff(wantRemoved, result.Removed); diff != "" {
		t.Fatalf("removed diff (-want +got):\n%s", diff)
	}

	if !slices.Contains(result.Retained, gens[2].ID) {
		t.Fatalf("expected rank-5 future generation %s to be retained", gens[2].ID)
	}
}

func TestRetentionRetainsUnderlyingErrorCauses(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	policy := RetentionPolicy{MaxAge: 1 * time.Hour, MaxCount: 0}

	t.Run("missing manifest preserves os.ErrNotExist", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "repository")
		r := openRepository(t, path)

		gens := make([]Generation, 7)
		for i := range gens {
			gens[i] = commitSnapshotAt(t, r, now.Add(-5*time.Hour))
		}

		manifestPath := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "manifest.json"))
		if err := os.Remove(manifestPath); err != nil {
			t.Fatal(err)
		}

		_, err := r.RetainPolicy(t.Context(), now, policy)
		assertRepositoryDiagnostic(t, err, "retain", "repository_corrupt", ErrCorrupt)

		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected underlying os.ErrNotExist cause to be preserved, got %v", err)
		}
	})

	t.Run("decode failure preserves model.ErrInvalid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "repository")
		r := openRepository(t, path)

		gens := make([]Generation, 7)
		for i := range gens {
			gens[i] = commitSnapshotAt(t, r, now.Add(-5*time.Hour))
		}

		manifestPath := filepath.Clean(filepath.Join(path, "generations", gens[0].ID, "manifest.json"))

		data := []byte(`{"not": "valid manifest"}`)
		if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
			t.Fatal(err)
		}

		newDigest := digest(data)

		err := r.update(t.Context(), func(c *Catalog) error {
			idx := c.generationIndex(gens[0].ID)
			c.Generations[idx].ManifestSHA256 = newDigest
			c.Attempts[c.attemptIndex(gens[0].ID)].ManifestSHA256 = newDigest

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		_, err = r.RetainPolicy(t.Context(), now, policy)
		assertRepositoryDiagnostic(t, err, "retain", "repository_corrupt", ErrCorrupt)

		if !errors.Is(err, model.ErrInvalid) {
			t.Fatalf("expected underlying model.ErrInvalid cause to be preserved, got %v", err)
		}
	})
}

func retireManifestSchema(t *testing.T, r *Repository, path, id string) {
	t.Helper()

	manifestPath := filepath.Clean(filepath.Join(path, "generations", id, "manifest.json"))

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}

	fields["version"] = model.Schema - 1

	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	newDigest := digest(encoded)

	err = r.update(t.Context(), func(c *Catalog) error {
		c.Generations[c.generationIndex(id)].ManifestSHA256 = newDigest
		c.Attempts[c.attemptIndex(id)].ManifestSHA256 = newDigest

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetentionRemovesGenerationsWithUnsupportedSchema(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	policies := map[string]RetentionPolicy{
		"age":   {MaxAge: 24 * time.Hour},
		"bytes": {MaxBytes: 1 << 30},
	}

	for name, policy := range policies {
		t.Run(name+"/older generations", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)

			gens := make([]Generation, 6)
			for i := range gens {
				gens[i] = commitSnapshotAt(t, r, now.Add(time.Duration(i-10)*time.Minute))
			}

			for _, gen := range gens[:4] {
				retireManifestSchema(t, r, path, gen.ID)
			}

			result, err := r.RetainPolicy(t.Context(), now, policy)
			if err != nil {
				t.Fatal(err)
			}

			wantRemoved := []string{gens[3].ID, gens[2].ID, gens[1].ID, gens[0].ID}
			if diff := cmp.Diff(wantRemoved, result.Removed); diff != "" {
				t.Fatalf("removed generations mismatch (-want +got):\n%s", diff)
			}

			catalog, err := r.Inspect(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			if len(catalog.Generations) != 2 {
				t.Fatalf("catalog still holds retired generations: %+v", catalog.Generations)
			}
		})

		t.Run(name+"/current generation", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)

			older := commitSnapshotAt(t, r, now.Add(-2*time.Minute))
			current := commitSnapshotAt(t, r, now.Add(-time.Minute))

			retireManifestSchema(t, r, path, older.ID)
			retireManifestSchema(t, r, path, current.ID)

			result, err := r.RetainPolicy(t.Context(), now, policy)
			if err != nil {
				t.Fatal(err)
			}

			if diff := cmp.Diff([]string{older.ID}, result.Removed); diff != "" {
				t.Fatalf("removed generations mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff([]string{current.ID}, result.Protected); diff != "" {
				t.Fatalf("protected generations mismatch (-want +got):\n%s", diff)
			}

			if result.BudgetUnmet {
				t.Fatal("a kept retired generation must not count against the byte budget")
			}
		})
	}
}
