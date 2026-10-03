//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestOpenExistingDoesNotCreateMissingRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenExisting(t.Context(), path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing repository: %v", err)
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created repository: %v", err)
	}

	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenExisting(t.Context(), path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty directory: %v", err)
	}

	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read created entries: %v, %v", entries, err)
	}
}

func TestOpenExistingPreservesInterruptedInitialization(t *testing.T) {
	for _, phase := range []string{faultInitRecorded, faultCatalogReplaced} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")

			_, err := openWithFault(t.Context(), path, func(current string) error {
				if current == phase {
					return errInjected
				}

				return nil
			})
			if !errors.Is(err, errInjected) {
				t.Fatal(err)
			}

			before := repositoryEntries(t, path)
			if _, err := OpenExisting(t.Context(), path); !errors.Is(err, ErrRecoveryRequired) {
				t.Fatal(err)
			}

			if diff := cmp.Diff(before, repositoryEntries(t, path)); diff != "" {
				t.Fatal(diff)
			}

			_ = openRepository(t, path)
		})
	}
}

func TestReadCurrentKeepsLatestAttemptBarrier(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	if _, _, err := r.ReadCurrent(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}

	if _, err := r.Begin(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, _, err := r.ReadCurrent(t.Context()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal(err)
	}

	generation := commitCompleteFixture(t, r)

	got, manifest, err := r.ReadCurrent(t.Context())
	if err != nil || got != generation || !manifest.Complete() {
		t.Fatalf("current view: %v, %v", got, err)
	}

	_ = commitFixture(t, r)
	if _, _, err := r.ReadCurrent(t.Context()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal(err)
	}

	if _, err := r.Read(t.Context(), generation.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReadCurrentPairsGenerationWithManifestDuringSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	r := openRepository(t, path)
	if err := commitNamedCurrent(t.Context(), r); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})

	var (
		writerErr error
		workers   sync.WaitGroup
	)

	workers.Go(func() {
		for range 12 {
			if err := commitNamedCurrent(t.Context(), r); err != nil {
				writerErr = err
				return
			}
		}
	})
	defer workers.Wait()

	for range 40 {
		generation, manifest, err := reader.ReadCurrent(t.Context())
		if errors.Is(err, ErrRecoveryRequired) {
			continue
		}

		if err != nil {
			t.Fatal(err)
		}

		if string(manifest.Sessions[0].Name) != generation.ID {
			t.Fatal("generation and bytes came from different catalog views")
		}
	}

	workers.Wait()

	if writerErr != nil {
		t.Fatal(writerErr)
	}
}

func commitNamedCurrent(ctx context.Context, r *Repository) error {
	attempt, err := r.Begin(ctx)
	if err != nil {
		return err
	}

	m, artifacts := snapshotFixture()

	m.Sessions[0].Name = model.Bytes(attempt.ID)
	for i := range m.Evidence {
		m.Evidence[i].Status, m.Evidence[i].Reason = "verified", ""
	}

	_, err = r.Commit(ctx, attempt.ID, m, artifacts)

	return err
}

func repositoryEntries(t *testing.T, path string) []string {
	t.Helper()

	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}

	return names
}

func TestReadPracticalCurrentAdmitsLatestObservation(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	if _, _, err := r.ReadPracticalCurrent(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty repository: %v", err)
	}

	observation := commitFixture(t, r)

	if _, _, err := r.ReadCurrent(t.Context()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("strict current view of an observation: %v", err)
	}

	got, manifest, err := r.ReadPracticalCurrent(t.Context())
	if err != nil || got != observation || manifest.Complete() || len(manifest.Sessions) != 1 {
		t.Fatalf("practical current view = %v, %v", got, err)
	}
}

func TestReadPracticalCurrentUsesNewestGenerationAfterUnfinishedSave(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	observation := commitFixture(t, r)

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	assertPracticalCurrentAfterUnfinishedSave(t, r, observation, "pending")

	if err := r.Fail(t.Context(), attempt.ID); err != nil {
		t.Fatal(err)
	}

	assertPracticalCurrentAfterUnfinishedSave(t, r, observation, "failed")
}

func TestReadPracticalCurrentUsesCompleteGenerationAfterFailedSave(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	generation := commitCompleteFixture(t, r)

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Fail(t.Context(), attempt.ID); err != nil {
		t.Fatal(err)
	}

	assertPracticalCurrentAfterUnfinishedSave(t, r, generation, "failed")
}

func TestReadPracticalCurrentWithoutAnyGenerationKeepsBarrier(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Fail(t.Context(), attempt.ID); err != nil {
		t.Fatal(err)
	}

	if _, _, err := r.ReadPracticalCurrent(t.Context()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("failed first save has no generation to use: %v", err)
	}
}

func assertPracticalCurrentAfterUnfinishedSave(t *testing.T, r *Repository, want Generation, state string) {
	t.Helper()

	if _, _, err := r.ReadCurrent(t.Context()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("strict current view after %s save: %v", state, err)
	}

	got, _, err := r.ReadPracticalCurrent(t.Context())
	if err != nil || got.ID != want.ID {
		t.Fatalf("practical current view after %s save = %v, %v; want newest generation %s", state, got.ID, err, want.ID)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if !catalog.LatestSaveUnfinished() {
		t.Fatalf("LatestSaveUnfinished() = false after %s save", state)
	}
}

func TestReadPracticalCurrentPrefersCompleteCurrent(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))
	generation := commitCompleteFixture(t, r)

	got, manifest, err := r.ReadPracticalCurrent(t.Context())
	if err != nil || got != generation || !manifest.Complete() {
		t.Fatalf("practical current view = %v, %v", got, err)
	}
}
