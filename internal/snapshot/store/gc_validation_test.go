//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestCollectionPreservesReplacedRetiredManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	retired := retireGenerations(t, r, 2)[0]
	name := filepath.Join(path, "generations", retired.ID, "manifest.json")

	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}

	manifest, err := model.Decode(data)
	if err != nil {
		t.Fatal(err)
	}

	manifest.Sessions[0].Name = model.Bytes("unrelated replacement")

	replacement, err := model.Encode(manifest)
	if err != nil {
		t.Fatal(err)
	}

	rewriteFixtureFile(t, name, replacement)

	result := collectGenerations(t, r)
	if len(result.Removed) != 0 || !slices.Contains(result.Preserved, CollectionSkip{Generation: retired.ID, Reason: CollectionReasonInvalidContent}) {
		t.Fatalf("replacement was not preserved: %+v", result)
	}

	got, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("replacement bytes changed: %v", err)
	}
}

func TestCollectionResumeValidatesArtifactContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	retired := retireGenerations(t, r, 2)[0]
	interruptCollection(t, r, collectionPhaseIntentSynced)

	name := filepath.Join(path, "generations", retired.ID, "artifacts", "a0.bin.gz")

	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}

	data[len(data)-1] ^= 0xff
	rewriteFixtureFile(t, name, data)

	result := collectGenerations(t, r)
	if len(result.Removed) != 0 || !slices.Contains(result.Quarantined, CollectionSkip{Generation: retired.ID, Reason: CollectionQuarantineContent}) {
		t.Fatalf("same-size replaced artifact was not quarantined: %+v", result)
	}

	got, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("untrusted artifact changed: %v", err)
	}
}

func TestCollectionResumeBudgetIncludesManifest(t *testing.T) {
	for _, phase := range []string{collectionPhaseIntentSynced, collectionPhaseArtifactsUnlinked} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			retired := retireGenerations(t, r, 2)[0]
			interruptCollection(t, r, phase)

			info, err := os.Stat(filepath.Join(path, "generations", retired.ID, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}

			result, err := r.Collect(t.Context(), CollectionBudget{Generations: 1, Bytes: info.Size() - 1})
			if err != nil {
				t.Fatal(err)
			}

			if result.Bytes != 0 || len(result.Removed) != 0 || !slices.Contains(result.Deferred, retired.ID) {
				t.Fatalf("resumed collection exceeded byte budget: %+v", result)
			}

			assertGenerationPresent(t, path, retired.ID)
		})
	}
}

func TestCollectionCancellationBeforeManifestRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	retired := retireGenerations(t, r, 2)[0]

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	r.fault = func(phase string) error {
		if phase == collectionPhaseArtifactsUnlinked {
			cancel()
		}

		return nil
	}

	_, err := r.Collect(ctx, CollectionBudget{Generations: 1, Bytes: 1 << 30})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("collection ignored cancellation before manifest unlink: %v", err)
	}

	assertGenerationPresent(t, path, retired.ID)
}

func TestCollectionInterruptedManifestRemovalReportsObservedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	retired := retireGenerations(t, r, 2)[0]
	want := generationStoredBytes(t, path, retired.ID)
	r.fault = func(phase string) error {
		if phase == collectionPhaseManifestUnlinked {
			return errInjected
		}

		return nil
	}

	result, err := r.Collect(t.Context(), CollectionBudget{Generations: 1, Bytes: 1 << 30})
	if !errors.Is(err, errInjected) || !errors.Is(err, ErrUncertain) {
		t.Fatalf("partial collection lost failure or uncertainty: %v", err)
	}

	if result.Bytes != want {
		t.Fatalf("observed unlinked bytes = %d, want %d", result.Bytes, want)
	}
}

func TestPublishedAttemptRetainsDigestAfterCollection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	retired := retireGenerations(t, r, 2)[0]
	collectGenerations(t, r)
	reopened := openRepository(t, path)

	catalog, err := reopened.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	index := catalog.attemptIndex(retired.ID)
	if index < 0 || catalog.Attempts[index].ManifestSHA256 != retired.ManifestSHA256 {
		t.Fatal("collection lost committed manifest identity")
	}

	if catalog.generationIndex(retired.ID) >= 0 {
		t.Fatal("retired generation returned to membership")
	}
}

func TestCatalogRejectsMissingAttemptDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	gen := commitCompleteFixture(t, r)

	data, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	missing := bytes.Replace(data, []byte(`,"manifestSha256":"`+gen.ManifestSHA256+`"`), nil, 1)
	if bytes.Equal(missing, data) {
		t.Fatal("test failed to remove attempt digest field")
	}

	writeCatalogCorruption(t, path, missing)

	if _, err := r.Inspect(t.Context()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("catalog inferred a missing digest: %v", err)
	}
}

func TestCollectionInspectionPreservesUnsafeFileCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	retired := retireGenerations(t, r, 2)[0]
	interruptCollection(t, r, collectionPhaseIntentSynced)

	if err := os.Chmod(filepath.Join(path, "gc", retired.ID+".json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := r.InspectCollection(t.Context(), retired.ID)
	if !errors.Is(err, ErrCorrupt) || !errors.Is(err, privatefs.ErrUnsafe) {
		t.Fatalf("collection inspection lost unsafe-file cause: %v", err)
	}
}
