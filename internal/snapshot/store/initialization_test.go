//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestInitializationNormalBrandNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Sequence != 0 || len(catalog.Attempts) != 0 || len(catalog.Generations) != 0 || len(catalog.Operations) != 0 {
		t.Fatalf("unexpected catalog state: %+v", catalog)
	}

	verifyInitLayout(t, path)

	generation := commitFixture(t, r)

	got, err := r.Read(t.Context(), generation.ID)
	if err != nil {
		t.Fatal(err)
	}

	want, _ := snapshotFixture()
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Fatal(diff)
	}
}

func verifyInitLayout(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(filepath.Join(path, initializationMarker)); !os.IsNotExist(err) {
		t.Fatalf("initialization record still exists: %v", err)
	}

	for _, dir := range []string{"generations", "staging"} {
		info, err := os.Stat(filepath.Join(path, dir))
		if err != nil {
			t.Fatal(err)
		}

		if info.Mode().Perm() != 0o700 {
			t.Fatalf("wrong mode for %s: %v", dir, info.Mode())
		}
	}

	info, err := os.Stat(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("wrong mode for catalog.json: %v", info.Mode())
	}
}

func TestInitializationDeterministicFaultBoundaries(t *testing.T) {
	phases := []string{
		faultInitRecorded,
		faultInitGenerationsCreated,
		faultInitStagingCreated,
		faultCatalogSynced,
		faultCatalogReplaced,
		faultInitCatalogSync,
		faultInitCatalogConfirmed,
		faultInitMarkerRemoved,
		faultInitFinalized,
	}

	for _, phase := range phases {
		testFaultBoundaryPhase(t, phase)
	}
}

func testFaultBoundaryPhase(t *testing.T, phase string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "repository")

	r, err := openWithFault(t.Context(), path, func(current string) error {
		if current == phase {
			return errInjected
		}

		return nil
	})
	if r != nil {
		_ = r.Close()
	}

	if !errors.Is(err, errInjected) {
		t.Fatalf("expected injected fault at %s, got: %v", phase, err)
	}

	if phase == faultCatalogReplaced && !errors.Is(err, ErrUncertain) {
		t.Fatal("lost uncertainty classification at catalog_replaced")
	}

	reopened := openRepository(t, path)

	catalog, err := reopened.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Sequence != 0 {
		t.Fatalf("unexpected sequence: %d", catalog.Sequence)
	}

	generation := commitFixture(t, reopened)
	if _, err := reopened.Read(t.Context(), generation.ID); err != nil {
		t.Fatal("read committed generation failed", err)
	}
}

func TestInitializationRepeatedInterruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	phases := []string{
		faultInitRecorded,
		faultInitGenerationsCreated,
		faultInitStagingCreated,
		faultCatalogSynced,
	}

	for _, phase := range phases {
		r, err := openWithFault(t.Context(), path, func(current string) error {
			if current == phase {
				return errInjected
			}

			return nil
		})
		if r != nil {
			_ = r.Close()
		}

		if !errors.Is(err, errInjected) {
			t.Fatalf("expected injected fault at %s, got: %v", phase, err)
		}
	}

	r := openRepository(t, path)

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Sequence != 0 {
		t.Fatalf("unexpected sequence: %d", catalog.Sequence)
	}

	generation := commitFixture(t, r)
	if _, err := r.Read(t.Context(), generation.ID); err != nil {
		t.Fatal(err)
	}
}

func TestInitializationConcurrentOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	const count = 4

	var wg sync.WaitGroup

	repos := make([]*Repository, count)
	errs := make([]error, count)

	for i := range count {
		wg.Go(func() {
			repos[i], errs[i] = Open(t.Context(), path)
		})
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent open %d failed: %v", i, err)
		}

		defer func() {
			if closeErr := repos[i].Close(); closeErr != nil {
				t.Error(closeErr)
			}
		}()

		catalog, err := repos[i].Inspect(t.Context())
		if err != nil {
			t.Fatalf("inspect %d failed: %v", i, err)
		}

		if catalog.Sequence != 0 || len(catalog.Attempts) != 0 {
			t.Fatalf("unexpected catalog on repo %d: %+v", i, catalog)
		}
	}

	gen := commitFixture(t, repos[0])
	if _, err := repos[1].Read(t.Context(), gen.ID); err != nil {
		t.Fatal("cross-reader failed", err)
	}
}

func TestInitializationUnknownFileRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(path, "unknown.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Open(t.Context(), path)
	if r != nil {
		_ = r.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt for unknown file, got: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(path, "unknown.txt"))
	if err != nil || string(data) != "data" {
		t.Fatal("unknown file modified or deleted", err)
	}
}

func TestInitializationUnknownDirectoryRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(path, "foreign"), 0o700); err != nil {
		t.Fatal(err)
	}

	r, err := Open(t.Context(), path)
	if r != nil {
		_ = r.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt for unknown directory, got: %v", err)
	}
}

func TestInitializationArbitraryTempFileRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(path, ".temp-catalog"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Open(t.Context(), path)
	if r != nil {
		_ = r.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt for arbitrary temp file, got: %v", err)
	}
}

func TestInitializationMalformedTempCatalogRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	writeTestInitMarker(t, path)

	if err := os.WriteFile(filepath.Join(path, ".catalog-nothex"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Open(t.Context(), path)
	if r != nil {
		_ = r.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt for malformed temp catalog, got: %v", err)
	}
}

func writeTestInitMarker(t *testing.T, path string) {
	t.Helper()

	if err := os.Mkdir(filepath.Join(path, initializationMarker), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestInitializationSymlinkedRecordRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "real-init.json")
	writeTestInitMarker(t, filepath.Dir(target))

	if err := os.Rename(filepath.Join(filepath.Dir(target), initializationMarker), target); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, filepath.Join(path, initializationMarker)); err != nil {
		t.Fatal(err)
	}

	r, err := Open(t.Context(), path)
	if r != nil {
		_ = r.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt for symlinked initialization record, got: %v", err)
	}
}

func testWrongModeDir(t *testing.T, dirName string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if dirName != initializationMarker {
		writeTestInitMarker(t, path)
	}

	if err := os.Mkdir(filepath.Join(path, dirName), 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := Open(t.Context(), path)
	if r != nil {
		_ = r.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt for 0755 %s dir, got: %v", dirName, err)
	}
}

func testSymlinkDir(t *testing.T, dirName string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if dirName != initializationMarker {
		writeTestInitMarker(t, path)
	}

	target := filepath.Join(t.TempDir(), "real-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, filepath.Join(path, dirName)); err != nil {
		t.Fatal(err)
	}

	r, err := Open(t.Context(), path)
	if r != nil {
		_ = r.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt for symlinked %s dir, got: %v", dirName, err)
	}
}

func TestInitializationWrongDirectoryModeRejected(t *testing.T) {
	for _, dir := range []string{initializationMarker, "generations", "staging"} {
		testWrongModeDir(t, dir)
	}
}

func TestInitializationSymlinkedDirectoryRejected(t *testing.T) {
	for _, dir := range []string{initializationMarker, "generations", "staging"} {
		testSymlinkDir(t, dir)
	}
}

func TestEstablishedMissingCatalogPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	r, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	gen := commitFixture(t, r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	genDir := filepath.Join(path, "generations", gen.ID)

	manifestData, err := os.ReadFile(filepath.Join(genDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}

	artifactData, err := os.ReadFile(filepath.Join(genDir, "artifacts", "a0.bin.gz"))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(path, "catalog.json")); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), path)
	if reopened != nil {
		_ = reopened.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt for missing catalog on established repo, got: %v", err)
	}

	afterManifest, err := os.ReadFile(filepath.Join(genDir, "manifest.json"))
	if err != nil || !bytes.Equal(manifestData, afterManifest) {
		t.Fatal("manifest was altered or deleted")
	}

	afterArtifact, err := os.ReadFile(filepath.Join(genDir, "artifacts", "a0.bin.gz"))
	if err != nil || !bytes.Equal(artifactData, afterArtifact) {
		t.Fatal("artifact was altered or deleted")
	}
}

func TestEstablishedEmptyDirsMissingCatalogPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	r, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(path, "catalog.json")); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), path)
	if reopened != nil {
		_ = reopened.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt for missing catalog without init record, got: %v", err)
	}
}

func TestEstablishedPlantedRecordCannotOverwriteGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	r, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	gen := commitFixture(t, r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(path, "catalog.json")); err != nil {
		t.Fatal(err)
	}

	writeTestInitMarker(t, path)

	reopened, err := Open(t.Context(), path)
	if reopened != nil {
		_ = reopened.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt when generations is non-empty, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(path, "generations", gen.ID, "manifest.json")); err != nil {
		t.Fatal("generation manifest was lost", err)
	}
}

func TestEstablishedPlantedRecordCannotOverwriteStaging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	r, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(path, "catalog.json")); err != nil {
		t.Fatal(err)
	}

	writeTestInitMarker(t, path)

	stageDir := filepath.Join(path, "staging", "staged-data")
	if err := os.Mkdir(stageDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(stageDir, "dummy"), []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), path)
	if reopened != nil {
		_ = reopened.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt when staging is non-empty, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(stageDir, "dummy")); err != nil {
		t.Fatal("staged data was lost", err)
	}

	if _, err := os.Stat(filepath.Join(path, "generations", "staged-data")); !os.IsNotExist(err) {
		t.Fatal("staged data was promoted to generations")
	}
}

func TestEstablishedRepositoryCorruptCatalogPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	r, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	gen := commitFixture(t, r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	corruptBytes := []byte("corrupted catalog content that is not valid JSON")

	writeTestInitMarker(t, path)

	if err := os.WriteFile(filepath.Join(path, "catalog.json"), corruptBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	genManifest := filepath.Join(path, "generations", gen.ID, "manifest.json")

	manifestBytes, err := os.ReadFile(genManifest)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), path)
	if reopened != nil {
		_ = reopened.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt for corrupt catalog, got: %v", err)
	}

	afterCatalog, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil || !bytes.Equal(corruptBytes, afterCatalog) {
		t.Fatal("corrupt catalog was altered or rebuilt")
	}

	afterManifest, err := os.ReadFile(genManifest)
	if err != nil || !bytes.Equal(manifestBytes, afterManifest) {
		t.Fatal("generation manifest was altered or deleted")
	}
}

func TestInitializationPreservesPartialCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	writeTestInitMarker(t, path)
	temp := filepath.Join(path, ".catalog-0123456789abcdef0123456789abcdef")

	partial := []byte("{")
	if err := os.WriteFile(temp, partial, 0o600); err != nil {
		t.Fatal(err)
	}

	r := openRepository(t, path)
	verifyInitLayout(t, path)

	got, err := os.ReadFile(temp)
	if err != nil || !bytes.Equal(got, partial) {
		t.Fatalf("temporary source changed: %v", err)
	}

	commitFixture(t, r)
}

func TestInitializationUnsafeMarkerPreserved(t *testing.T) {
	for _, published := range []bool{false, true} {
		for _, kind := range []string{"file", "nonempty"} {
			t.Run(kind+map[bool]string{false: "_unpublished", true: "_published"}[published], func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "repository")
				if published {
					openRepository(t, path)
				} else if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}

				marker := makeInvalidInitMarker(t, path, kind)

				_, err := Open(t.Context(), path)
				if !errors.Is(err, ErrCorrupt) {
					t.Fatalf("accepted unsafe marker: %v", err)
				}

				if _, err := os.Lstat(marker); err != nil {
					t.Fatalf("removed marker: %v", err)
				}
			})
		}
	}
}

func TestInitializationUnsafeCatalogTemporaryPreserved(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}

			writeTestInitMarker(t, path)

			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
				t.Fatal(err)
			}

			temp := filepath.Join(path, ".catalog-0123456789abcdef0123456789abcdef")

			makeUnsafeCatalogTemporary(t, kind, target, temp)

			_, err := Open(t.Context(), path)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("accepted unsafe temporary: %v", err)
			}

			if _, err := os.Lstat(temp); err != nil {
				t.Fatalf("removed temporary: %v", err)
			}

			data, err := os.ReadFile(target)
			if err != nil || string(data) != "private" {
				t.Fatalf("changed target: %v", err)
			}
		})
	}
}

func TestInitializationCancellation(t *testing.T) {
	for _, phase := range []string{faultInitRecorded, faultInitStagingCreated, faultInitCatalogConfirmed} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			_, err := openWithFault(ctx, path, func(current string) error {
				if current == phase {
					cancel()
				}

				return nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}

			if phase == faultInitCatalogConfirmed && !errors.Is(err, ErrUncertain) {
				t.Fatal("lost publication uncertainty")
			}

			openRepository(t, path)
			verifyInitLayout(t, path)
		})
	}
}

func TestInitializationSyncFailureKeepsMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	_, err := openWithFault(t.Context(), path, func(phase string) error {
		if phase == faultInitCatalogSync {
			return errInjected
		}

		return nil
	})
	if !errors.Is(err, ErrUncertain) || !errors.Is(err, errInjected) {
		t.Fatalf("sync failure: %v", err)
	}

	if _, err := os.Stat(filepath.Join(path, initializationMarker)); err != nil {
		t.Fatal("lost recovery marker", err)
	}

	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	openRepository(t, path)

	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rewrote authoritative catalog", err)
	}
}

func TestInitializationMarkerWithEstablishedCatalogRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	commitFixture(t, r)
	writeTestInitMarker(t, path)

	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = Open(t.Context(), path)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("accepted stale intent: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("changed catalog", err)
	}

	if _, err := os.Stat(filepath.Join(path, initializationMarker)); err != nil {
		t.Fatal("removed stale marker", err)
	}
}

func TestInitializationInterruptedMarkerRemovalRequiresSync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	_, err := openWithFault(t.Context(), path, func(phase string) error {
		if phase == faultInitMarkerRemoved {
			return errInjected
		}

		return nil
	})
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("expected uncertain unlink: %v", err)
	}

	if _, err := os.Stat(filepath.Join(path, initializationMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker still exists: %v", err)
	}

	_, err = openWithFault(t.Context(), path, func(phase string) error {
		if phase == faultInitCatalogSync {
			return errInjected
		}

		return nil
	})
	if !errors.Is(err, ErrUncertain) || !errors.Is(err, errInjected) {
		t.Fatalf("admitted operations without confirmation: %v", err)
	}

	openRepository(t, path)
}

func TestInitializationPublishedMissingDirectoryRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	openRepository(t, path)
	writeTestInitMarker(t, path)

	if err := os.Remove(filepath.Join(path, "staging")); err != nil {
		t.Fatal(err)
	}

	_, err := Open(t.Context(), path)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("accepted missing directory: %v", err)
	}
}

func makeInvalidInitMarker(t *testing.T, path, kind string) string {
	t.Helper()

	marker := filepath.Join(path, initializationMarker)
	file := marker

	if kind == "nonempty" {
		writeTestInitMarker(t, path)

		file = filepath.Join(marker, "foreign")
	}

	if err := os.WriteFile(file, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	return marker
}

func makeUnsafeCatalogTemporary(t *testing.T, kind, target, temp string) {
	t.Helper()

	var err error

	switch kind {
	case "symlink":
		err = os.Symlink(target, temp)
	case "hardlink":
		err = os.Link(target, temp)
	case "directory":
		err = os.Mkdir(temp, 0o700)
	}

	if err != nil {
		t.Fatal(err)
	}
}
