//go:build linux || darwin

package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func commitCompleteFixture(t *testing.T, r *Repository) Generation {
	t.Helper()

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	m, artifacts := snapshotFixture()

	m.Source.Consistency = "checkpoint"
	for i := range m.Evidence {
		m.Evidence[i].Status, m.Evidence[i].Reason = "verified", ""
	}

	generation, err := r.Commit(t.Context(), attempt.ID, m, artifacts)
	if err != nil {
		t.Fatal(err)
	}

	return generation
}

func TestPublicationNeverFallsBack(t *testing.T) {
	for _, state := range []string{"pending", "failed", "incomplete", faultStageSynced, faultGenerationPublished, faultCatalogSynced, faultCatalogReplaced} {
		t.Run(state, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			prior := commitCompleteFixture(t, r)

			before, err := r.Current(t.Context())
			if err != nil {
				t.Fatal("prior generation was not eligible", err)
			}

			attempt, err := r.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			stageAttempt(t, r, attempt.ID, state)

			reopened := openRepository(t, path)
			if _, err := reopened.Current(t.Context()); !errors.Is(err, ErrRecoveryRequired) {
				t.Fatal("latest failure silently selected older snapshot", err)
			}

			after, err := reopened.Read(t.Context(), prior.ID)
			if err != nil {
				t.Fatal("prior recovery source lost", err)
			}

			if diff := cmp.Diff(before, after, cmpopts.EquateEmpty()); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func stageAttempt(t *testing.T, r *Repository, id, state string) {
	t.Helper()

	if state == "pending" {
		return
	}

	if state == "failed" {
		if err := r.Fail(t.Context(), id); err != nil {
			t.Fatal(err)
		}

		return
	}

	r.fault = func(phase string) error {
		if phase == state {
			return errInjected
		}

		return nil
	}
	m, artifacts := snapshotFixture()

	_, err := r.Commit(t.Context(), id, m, artifacts)
	if state == "incomplete" {
		if err != nil {
			t.Fatal(err)
		}
	} else if !errors.Is(err, errInjected) {
		t.Fatal("publication boundary was not exercised", err)
	}
}

func TestOutOfOrderSaveCompletion(t *testing.T) {
	r := openRepository(t, filepath.Join(t.TempDir(), "repository"))

	older, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	newer := commitCompleteFixture(t, r)

	m, artifacts := snapshotFixture()
	if _, err := r.Commit(t.Context(), older.ID, m, artifacts); err != nil {
		t.Fatal(err)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Current != newer.ID {
		t.Fatal("late older save replaced newer head")
	}

	if _, err := r.Current(t.Context()); err != nil {
		t.Fatal("late older save poisoned newer complete head", err)
	}
}
