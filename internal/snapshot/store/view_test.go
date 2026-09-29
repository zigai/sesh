//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func openView(t *testing.T, path string) *View {
	t.Helper()

	view, err := OpenView(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Error(err)
		}
	})

	return view
}

func TestViewOfMissingRepositoryListsNothingAndCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")

	view := openView(t, path)
	if _, _, err := view.ReadPracticalCurrent(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing repository: %v, want ErrUnavailable", err)
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("view created the repository: %v", err)
	}
}

func TestViewLeavesInterruptedInitializationForDurableOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	_, err := openWithFault(t.Context(), path, func(current string) error {
		if current == faultCatalogReplaced {
			return errInjected
		}

		return nil
	})
	if !errors.Is(err, errInjected) {
		t.Fatal(err)
	}

	before := repositoryEntries(t, path)

	view := openView(t, path)
	if _, _, err := view.ReadPracticalCurrent(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("interrupted initialization: %v, want ErrUnavailable", err)
	}

	if diff := cmp.Diff(before, repositoryEntries(t, path)); diff != "" {
		t.Fatalf("view changed the repository:\n%s", diff)
	}
}

func TestViewReadsCurrentWhileRepositoryLockIsHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generation := commitFixture(t, r)

	lock, err := r.root.Lock(t.Context(), "repository.lock")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = lock.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	view := openView(t, path)

	got, manifest, err := view.ReadPracticalCurrent(ctx)
	if err != nil || got != generation || len(manifest.Sessions) == 0 {
		t.Fatalf("view current under a held lock = %v, %v; want generation %v", got, err, generation)
	}

	if _, err := view.Read(ctx, generation.ID); err != nil {
		t.Fatalf("view read under a held lock: %v", err)
	}
}

func TestViewPairsGenerationWithManifestDuringSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	r := openRepository(t, path)
	if err := commitNamedCurrent(t.Context(), r); err != nil {
		t.Fatal(err)
	}

	view := openView(t, path)

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
		generation, manifest, err := view.ReadPracticalCurrent(t.Context())
		if errors.Is(err, ErrRecoveryRequired) {
			continue
		}

		if err != nil {
			t.Fatal(err)
		}

		if string(manifest.Sessions[0].Name) != generation.ID {
			t.Fatal("generation and bytes came from different catalog publications")
		}
	}

	workers.Wait()

	if writerErr != nil {
		t.Fatal(writerErr)
	}
}
