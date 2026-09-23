//go:build linux || darwin

package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/sys/unix"
)

func interruptedSaveFixture(t *testing.T, r *Repository) string {
	t.Helper()

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	r.fault = func(phase string) error {
		if phase == faultGenerationPublished {
			return errInjected
		}

		return nil
	}

	manifest, artifacts := snapshotFixture()
	if _, err := r.Commit(t.Context(), attempt.ID, manifest, artifacts); !errors.Is(err, errInjected) {
		t.Fatalf("interrupted save = %v", err)
	}

	r.fault = nil

	return attempt.ID
}

func stagedSaveFixture(t *testing.T, r *Repository) string {
	t.Helper()

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	r.fault = func(phase string) error {
		if phase == faultStageSynced {
			return errInjected
		}

		return nil
	}

	manifest, artifacts := snapshotFixture()
	if _, err := r.Commit(t.Context(), attempt.ID, manifest, artifacts); !errors.Is(err, errInjected) {
		t.Fatalf("staged save = %v", err)
	}

	r.fault = nil

	return attempt.ID
}

func TestCollectionPreservesInterruptedSaveSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	orphan := interruptedSaveFixture(t, r)
	staged := stagedSaveFixture(t, r)
	current := commitFixture(t, r)

	orphanManifest := filepath.Join(path, "generations", orphan, "manifest.json")
	if _, err := os.Stat(orphanManifest); err != nil {
		t.Fatal("fixture did not publish an orphan generation", err)
	}

	staging := filepath.Join(path, "staging", staged)
	if _, err := os.Stat(staging); err != nil {
		t.Fatal("fixture did not leave staged data", err)
	}

	orphanBefore, err := os.ReadFile(orphanManifest)
	if err != nil {
		t.Fatal(err)
	}

	result := collectGenerations(t, r)

	want := []CollectionSkip{{Generation: orphan, Reason: CollectionReasonPending}}
	if diff := cmp.Diff(want, result.Preserved); diff != "" {
		t.Fatal(diff)
	}

	assertNothingCollected(t, result)
	assertSourcePreserved(t, orphanManifest, orphanBefore, staging)

	if _, err := r.Read(t.Context(), current.ID); err != nil {
		t.Fatal("committed generation lost", err)
	}
}

func TestCollectionPreservesFailedAttemptSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	orphan := interruptedSaveFixture(t, r)
	current := commitFixture(t, r)

	orphanManifest := filepath.Join(path, "generations", orphan, "manifest.json")

	orphanBefore, err := os.ReadFile(orphanManifest)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Fail(t.Context(), orphan); err != nil {
		t.Fatal(err)
	}

	result := collectGenerations(t, r)

	want := []CollectionSkip{{Generation: orphan, Reason: CollectionReasonFailed}}
	if diff := cmp.Diff(want, result.Preserved); diff != "" {
		t.Fatal(diff)
	}

	assertNothingCollected(t, result)
	assertSourcePreserved(t, orphanManifest, orphanBefore, "")

	if _, err := r.Read(t.Context(), current.ID); err != nil {
		t.Fatal("committed generation lost", err)
	}
}

func assertNothingCollected(t *testing.T, result CollectionResult) {
	t.Helper()

	if len(result.Removed) != 0 || result.Bytes != 0 {
		t.Fatalf("collection removed interrupted save data: %+v", result)
	}
}

func assertSourcePreserved(t *testing.T, manifest string, before []byte, staging string) {
	t.Helper()

	after, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal("interrupted save source was destroyed", err)
	}

	if diff := cmp.Diff(before, after); diff != "" {
		t.Fatal("interrupted save source content changed")
	}

	if staging == "" {
		return
	}

	if _, err := os.Stat(staging); err != nil {
		t.Fatal("staged save data was touched", err)
	}
}

func TestCollectionPreservesEntriesWithoutAuthorizedEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	planted := "0123456789abcdef0123456789abcdef"
	plantedDir := filepath.Join(path, "generations", planted)

	if err := os.MkdirAll(filepath.Join(plantedDir, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(plantedDir, "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(path, "generations", "not-a-generation-id"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(path, "generations", "README"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	foreign := filepath.Join(path, "foreign-data")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	symlinked := "fedcba9876543210fedcba9876543210"
	if err := os.Symlink(foreign, filepath.Join(path, "generations", symlinked)); err != nil {
		t.Fatal(err)
	}

	tree := repositoryTree(t, path)

	result := collectGenerations(t, r)

	if len(result.Removed) != 0 || result.Bytes != 0 {
		t.Fatalf("unauthorized entries were collected: %+v", result)
	}

	want := sortedSkips([]CollectionSkip{
		{Generation: "README", Reason: CollectionReasonUnrecognized},
		{Generation: symlinked, Reason: CollectionReasonNoEvidence},
		{Generation: planted, Reason: CollectionReasonNoEvidence},
		{Generation: "not-a-generation-id", Reason: CollectionReasonUnrecognized},
	})
	if diff := cmp.Diff(want, sortedSkips(result.Preserved)); diff != "" {
		t.Fatal(diff)
	}

	if diff := cmp.Diff(tree, repositoryTree(t, path)); diff != "" {
		t.Fatal(diff)
	}
}

func unsafeEntryFixtures() []struct {
	name   string
	damage func(t *testing.T, path, id string)
} {
	return []struct {
		name   string
		damage func(t *testing.T, path, id string)
	}{
		{name: "symlinked generation directory", damage: replaceGenerationWithSymlink},
		{name: "fifo instead of a generation directory", damage: replaceGenerationWithFifo},
		{name: "unsafe generation directory mode", damage: chmodGenerationDirectory},
		{name: "unsafe artifact directory mode", damage: chmodArtifactDirectory},
		{name: "artifact with a second hard link", damage: hardlinkArtifact},
	}
}

func replaceGenerationWithSymlink(t *testing.T, path, id string) {
	t.Helper()

	target := filepath.Join(path, "foreign-data")
	if err := os.WriteFile(target, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Join(path, generationsDirectory, id)); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, filepath.Join(path, generationsDirectory, id)); err != nil {
		t.Fatal(err)
	}
}

func replaceGenerationWithFifo(t *testing.T, path, id string) {
	t.Helper()

	entry := filepath.Join(path, generationsDirectory, id)
	if err := os.RemoveAll(entry); err != nil {
		t.Fatal(err)
	}

	if err := unix.Mkfifo(entry, 0o600); err != nil {
		t.Fatal(err)
	}
}

func chmodGenerationDirectory(t *testing.T, path, id string) {
	t.Helper()

	if err := os.Chmod(filepath.Join(path, generationsDirectory, id), 0o755); err != nil {
		t.Fatal(err)
	}
}

func chmodArtifactDirectory(t *testing.T, path, id string) {
	t.Helper()

	if err := os.Chmod(filepath.Join(path, generationsDirectory, id, collectionArtifactsName), 0o755); err != nil {
		t.Fatal(err)
	}
}

func hardlinkArtifact(t *testing.T, path, id string) {
	t.Helper()

	source := filepath.Join(path, generationsDirectory, id, collectionArtifactsName, "a0.bin.gz")
	if err := os.Link(source, filepath.Join(path, "foreign-hardlink")); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionPreservesUnsafeRetiredGenerationEntries(t *testing.T) {
	for _, test := range unsafeEntryFixtures() {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			generations := retireGenerations(t, r, 2)
			retired, current := generations[0], generations[1]

			test.damage(t, path, retired.ID)
			assertGenerationEntryPreserved(t, r, path, retired.ID, CollectionReasonUnsafe)

			if _, err := r.Read(t.Context(), current.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertGenerationEntryPreserved(t *testing.T, r *Repository, path, id, reason string) {
	t.Helper()

	result := collectGenerations(t, r)

	if len(result.Removed) != 0 || result.Bytes != 0 {
		t.Fatalf("refused generation was collected: %+v", result)
	}

	want := []CollectionSkip{{Generation: id, Reason: reason}}
	if diff := cmp.Diff(want, result.Preserved); diff != "" {
		t.Fatal(diff)
	}

	if _, err := os.Lstat(filepath.Join(path, generationsDirectory, id)); err != nil {
		t.Fatal("refused entry was removed", err)
	}
}

func unknownContentFixtures() []struct {
	name   string
	damage func(t *testing.T, path, id string)
	want   string
} {
	return []struct {
		name   string
		damage func(t *testing.T, path, id string)
		want   string
	}{
		{name: "unexpected entry in the generation directory", damage: plantGenerationEntry, want: CollectionReasonUnknownContent},
		{name: "unexpected entry in the artifact directory", damage: plantArtifactEntry, want: CollectionReasonUnknownContent},
		{name: "fifo replacing an artifact", damage: replaceArtifactWithFifo, want: CollectionReasonUnsafe},
		{name: "missing manifest with intact artifacts", damage: removeGenerationManifest, want: CollectionReasonUnknownContent},
		{name: "corrupt manifest", damage: truncateGenerationManifest, want: CollectionReasonInvalidContent},
		{name: "artifact replaced by different bytes", damage: replaceArtifactBytes, want: CollectionReasonInvalidContent},
	}
}

func plantGenerationEntry(t *testing.T, path, id string) {
	t.Helper()

	name := filepath.Join(path, generationsDirectory, id, "extra.txt")
	if err := os.WriteFile(name, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func plantArtifactEntry(t *testing.T, path, id string) {
	t.Helper()

	name := filepath.Join(path, generationsDirectory, id, collectionArtifactsName, "stray")
	if err := os.WriteFile(name, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func replaceArtifactWithFifo(t *testing.T, path, id string) {
	t.Helper()

	name := filepath.Join(path, generationsDirectory, id, collectionArtifactsName, "a0.bin.gz")
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}

	if err := unix.Mkfifo(name, 0o600); err != nil {
		t.Fatal(err)
	}
}

func removeGenerationManifest(t *testing.T, path, id string) {
	t.Helper()

	if err := os.Remove(filepath.Join(path, generationsDirectory, id, collectionManifestName)); err != nil {
		t.Fatal(err)
	}
}

func truncateGenerationManifest(t *testing.T, path, id string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(path, generationsDirectory, id, collectionManifestName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func replaceArtifactBytes(t *testing.T, path, id string) {
	t.Helper()

	name := filepath.Join(path, generationsDirectory, id, collectionArtifactsName, "a0.bin.gz")
	if err := os.WriteFile(name, []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionPreservesUnknownRetiredGenerationContent(t *testing.T) {
	for _, test := range unknownContentFixtures() {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			generations := retireGenerations(t, r, 2)
			retired := generations[0]

			test.damage(t, path, retired.ID)

			tree := repositoryTree(t, path)
			result := collectGenerations(t, r)

			if len(result.Removed) != 0 || result.Bytes != 0 {
				t.Fatalf("contradictory content was collected: %+v", result)
			}

			want := []CollectionSkip{{Generation: retired.ID, Reason: test.want}}
			if diff := cmp.Diff(want, result.Preserved); diff != "" {
				t.Fatal(diff)
			}

			if diff := cmp.Diff(tree, repositoryTree(t, path)); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestCollectionRejectsUnsafeIntentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	retired := generations[0]

	target := filepath.Join(path, "foreign-data")
	if err := os.WriteFile(target, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, filepath.Join(path, collectionDirectory)); err != nil {
		t.Fatal(err)
	}

	tree := repositoryTree(t, path)

	result, err := r.Collect(t.Context(), CollectionBudget{Generations: 8, Bytes: 1 << 30})
	if err == nil {
		t.Fatal("collection accepted an unsafe intent directory")
	}

	if len(result.Removed) != 0 || result.Bytes != 0 {
		t.Fatalf("unsafe intent directory authorized removal: %+v", result)
	}

	if diff := cmp.Diff(tree, repositoryTree(t, path)); diff != "" {
		t.Fatal(diff)
	}

	assertGenerationPresent(t, path, retired.ID)
}

func TestCollectionPreservesForeignIntentDirectoryContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	retired := generations[0]

	dir := filepath.Join(path, collectionDirectory)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	foreign := filepath.Join(dir, "foreign.tmp")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := collectGenerations(t, r)

	if diff := cmp.Diff([]string{retired.ID}, result.Removed); diff != "" {
		t.Fatal(diff)
	}

	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("foreign intent directory entry was removed", err)
	}
}

func TestCollectionNeverFollowsGenerationSymlinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	retired := generations[0]

	decoy := filepath.Join(path, "decoy")
	if err := os.MkdirAll(filepath.Join(decoy, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(decoy, "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	entry := filepath.Join(path, "generations", retired.ID)
	if err := os.RemoveAll(entry); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(decoy, entry); err != nil {
		t.Fatal(err)
	}

	result := collectGenerations(t, r)

	if len(result.Removed) != 0 {
		t.Fatalf("symlinked generation was followed: %+v", result)
	}

	if _, err := os.Stat(filepath.Join(decoy, "manifest.json")); err != nil {
		t.Fatal("symlink target was modified", err)
	}

	if info, err := os.Lstat(entry); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("symlink entry changed: %v", err)
	}
}

func TestCollectionPreservesMemberWithOutstandingIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generations := retireGenerations(t, r, 2)
	current := generations[1]

	writeCollectionRecordFixture(t, path, current.ID, encodeFixtureIntent(t, validFixtureIntent(current.ID, 2)))

	result := collectGenerations(t, r)

	if diff := cmp.Diff([]string{generations[0].ID}, result.Removed); diff != "" {
		t.Fatal(diff)
	}

	assertGenerationPresent(t, path, current.ID)

	if len(result.Preserved) != 0 || len(result.Quarantined) != 0 {
		t.Fatalf("member generation reported as collectable: %+v", result)
	}

	if _, err := r.Read(t.Context(), current.ID); err != nil {
		t.Fatal("member generation was reclaimed", err)
	}

	stored := readCollectionRecordFixture(t, path, current.ID)
	if stored.State != collectionStateDeleting {
		t.Fatalf("member intent was rewritten: %+v", stored)
	}
}

func TestCollectionPreservesGenerationWithoutIntentRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	generations := retireGenerations(t, r, 2)
	retired := generations[0]

	if err := os.RemoveAll(filepath.Join(path, "generations", retired.ID)); err != nil {
		t.Fatal(err)
	}

	result := collectGenerations(t, r)

	if len(result.Removed)+len(result.Deferred)+len(result.Preserved)+len(result.Quarantined) != 0 || result.Bytes != 0 {
		t.Fatalf("absent generation reported work: %+v", result)
	}

	if _, err := os.Stat(filepath.Join(path, collectionDirectory)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("absent generation created collection state")
	}
}
