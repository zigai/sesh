//go:build linux || darwin

package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const maxFixtureDecompressLimit = 10_000_000

func registerPendingRewriteFixture(t *testing.T, r *Repository, sourceID string, sessions []string) (string, model.Removal) {
	t.Helper()

	mutationID := newID()

	r.fault = func(phase string) error {
		if phase == "rewrite_registered" {
			return errInjected
		}

		return nil
	}

	_, err := r.RemoveSessions(t.Context(), mutationID, sourceID, sessions)
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired after injected fault at rewrite_registered, got %v", err)
	}

	r.fault = nil

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	idx := catalog.rewriteIndex(mutationID)
	if idx < 0 || catalog.Rewrites[idx].State != rewriteStatePending {
		t.Fatalf("expected pending rewrite in catalog, got %v", catalog.Rewrites)
	}

	sourceGen := catalog.Generations[catalog.generationIndex(sourceID)]

	sourceManifest, err := r.readGeneration(t.Context(), sourceGen)
	if err != nil {
		t.Fatal(err)
	}

	removal, err := model.RemoveSessions(sourceManifest, sessions)
	if err != nil {
		t.Fatal(err)
	}

	return mutationID, removal
}

func TestResumeRewriteInvalidID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	invalidIDs := []string{
		"",
		"not-hex",
		"123",
		"ABCDEF0123456789abcdef0123456789",
		"abcdef0123456789abcdef01234567890",
		"abcdef0123456789abcdef012345678",
		"../escape",
	}

	for _, badID := range invalidIDs {
		t.Run("bad_id_"+badID, func(t *testing.T) {
			_, err := r.ResumeRewrite(t.Context(), badID)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("expected ErrCorrupt for invalid ID %q, got %v", badID, err)
			}

			if oopsErr, ok := errors.AsType[oops.OopsError](err); ok {
				if oopsErr.Code() != "repository_corrupt" {
					t.Fatalf("expected code repository_corrupt, got %s", oopsErr.Code())
				}
			}
		})
	}
}

func TestResumeRewriteUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	randomID := newID()

	_, err := r.ResumeRewrite(t.Context(), randomID)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for missing rewrite ID, got %v", err)
	}

	if oopsErr, ok := errors.AsType[oops.OopsError](err); ok {
		if oopsErr.Code() != "snapshot_unavailable" {
			t.Fatalf("expected code snapshot_unavailable, got %s", oopsErr.Code())
		}
	}
}

func TestResumeRewriteTerminalSameIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	testResumeRewriteTerminalCompleted(t, r, source.ID)
	testResumeRewriteTerminalRetired(t, r)
}

func testResumeRewriteTerminalCompleted(t *testing.T, r *Repository, sourceID string) {
	t.Helper()

	mutationID := newID()

	origRes, err := r.RemoveSessions(t.Context(), mutationID, sourceID, []string{"s0"})
	if err != nil {
		t.Fatalf("RemoveSessions failed: %v", err)
	}

	if origRes.State != rewriteStateCompleted {
		t.Fatalf("expected completed state, got %s", origRes.State)
	}

	resumedRes, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite on completed rewrite failed: %v", err)
	}

	if resumedRes.ID != origRes.ID || resumedRes.State != origRes.State ||
		resumedRes.Replacement != origRes.Replacement ||
		!slices.Equal(resumedRes.Removed, origRes.Removed) ||
		!slices.Equal(resumedRes.Artifacts, origRes.Artifacts) ||
		resumedRes.Empty != origRes.Empty {
		t.Fatalf("terminal same-intent resume mismatch: resumed=%+v orig=%+v", resumedRes, origRes)
	}
}

func testResumeRewriteTerminalRetired(t *testing.T, r *Repository) {
	t.Helper()

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	currentGenID := catalog.Current
	mutationID := newID()

	origRes, err := r.RemoveSessions(t.Context(), mutationID, currentGenID, []string{"s1", "s2"})
	if err != nil {
		t.Fatalf("RemoveSessions all-retirement failed: %v", err)
	}

	if origRes.State != rewriteStateRetired || !origRes.Empty {
		t.Fatalf("expected retired state, got %s empty=%v", origRes.State, origRes.Empty)
	}

	resumedRes, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite on retired rewrite failed: %v", err)
	}

	if resumedRes.ID != origRes.ID || resumedRes.State != origRes.State ||
		resumedRes.Empty != origRes.Empty ||
		!slices.Equal(resumedRes.Removed, origRes.Removed) ||
		!slices.Equal(resumedRes.Artifacts, origRes.Artifacts) {
		t.Fatalf("terminal retired same-intent resume mismatch: resumed=%+v orig=%+v", resumedRes, origRes)
	}
}

func TestResumeRewritePendingAllSessionRetirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID, removal := registerPendingRewriteFixture(t, r, source.ID, []string{"s0", "s1", "s2"})
	if !removal.Empty() {
		t.Fatalf("expected removal to be empty for all sessions")
	}

	result, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite failed on all-session retirement: %v", err)
	}

	if result.State != rewriteStateRetired || !result.Empty {
		t.Fatalf("unexpected result: state=%s empty=%v", result.State, result.Empty)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.generationIndex(source.ID) >= 0 {
		t.Fatal("source generation must be removed from generations")
	}

	if catalog.Current != "" {
		t.Fatalf("expected Current to be empty after retirement, got %s", catalog.Current)
	}

	if _, err := r.Current(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable from Current() after retirement, got %v", err)
	}

	verifyEmptyStagingDirectory(t, r)
}

func verifyEmptyStagingDirectory(t *testing.T, r *Repository) {
	t.Helper()

	staging, err := r.root.Child("staging", false)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = staging.Close() }()

	names, err := staging.NamesLimit(maxArtifactListingPadding)
	if err != nil {
		t.Fatal(err)
	}

	if len(names) != 0 {
		t.Fatalf("staging must be empty, found: %v", names)
	}
}

func TestResumeRewriteDurableBoundaries(t *testing.T) {
	t.Run("boundary_no_stage_or_generation_files", testBoundaryNoStageOrGen)
	t.Run("boundary_fully_synced_stage", testBoundaryFullySyncedStage)
	t.Run("boundary_renamed_generation", testBoundaryRenamedGeneration)
	t.Run("boundary_partial_stage_files", testBoundaryPartialStageFiles)
}

func testBoundaryNoStageOrGen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	res, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite failed from no-stage boundary: %v", err)
	}

	if res.State != rewriteStateCompleted || res.Empty {
		t.Fatalf("unexpected res: %+v", res)
	}

	manifest, err := r.Read(t.Context(), res.Replacement.ID)
	if err != nil {
		t.Fatalf("read replacement failed: %v", err)
	}

	if len(manifest.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(manifest.Sessions))
	}
}

func testBoundaryFullySyncedStage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID := newID()

	r.fault = func(phase string) error {
		if phase == "rewrite_stage_synced" {
			return errInjected
		}

		return nil
	}

	_, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0"})
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired at rewrite_stage_synced, got %v", err)
	}

	r.fault = nil

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	rw := catalog.Rewrites[catalog.rewriteIndex(mutationID)]

	res, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite on fully synced stage failed: %v", err)
	}

	if res.State != rewriteStateCompleted {
		t.Fatalf("unexpected res: %+v", res)
	}

	staging, err := r.root.Child("staging", false)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = staging.Close() }()

	_, err = staging.Child(rw.Replacement, false)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected staging dir to be moved, got %v", err)
	}
}

func testBoundaryRenamedGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID := newID()

	r.fault = func(phase string) error {
		if phase == "rewrite_generation_published" {
			return errInjected
		}

		return nil
	}

	_, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0"})
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired at rewrite_generation_published, got %v", err)
	}

	r.fault = nil

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	rw := catalog.Rewrites[catalog.rewriteIndex(mutationID)]

	res, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite on renamed generation failed: %v", err)
	}

	if res.State != rewriteStateCompleted {
		t.Fatalf("unexpected res: %+v", res)
	}

	finalCatalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if finalCatalog.generationIndex(rw.Replacement) < 0 {
		t.Fatal("replacement generation must be in catalog")
	}
}

func testBoundaryPartialStageFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID, removal := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	rw := catalog.Rewrites[catalog.rewriteIndex(mutationID)]

	plantTruncatedStageArtifact(t, r, rw.Replacement)

	validatedHookHit := false

	r.fault = func(phase string) error {
		if phase == "rewrite_resume_stage_validated" {
			validatedHookHit = true
		}

		return nil
	}

	res, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite failed on partial stage: %v", err)
	}

	if !validatedHookHit {
		t.Fatal("expected rewrite_resume_stage_validated boundary hook to be called")
	}

	if res.State != rewriteStateCompleted {
		t.Fatalf("unexpected res state: %s", res.State)
	}

	repairedManifest, err := r.Read(t.Context(), res.Replacement.ID)
	if err != nil {
		t.Fatalf("read repaired generation failed: %v", err)
	}

	if len(repairedManifest.Sessions) != 2 || len(repairedManifest.Artifacts) != len(removal.Retained.Artifacts) {
		t.Fatalf("repaired manifest mismatch: %+v", repairedManifest)
	}
}

func plantTruncatedStageArtifact(t *testing.T, r *Repository, replacementID string) {
	t.Helper()

	staging, err := r.root.Child("staging", false)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = staging.Close() }()

	stageDir, err := staging.CreateDir(replacementID)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = stageDir.Close() }()

	artDir, err := stageDir.CreateDir("artifacts")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = artDir.Close() }()

	f, err := artDir.File("a0.bin.gz", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.Write([]byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 255}); err != nil {
		_ = f.Close()

		t.Fatal(err)
	}

	if err := errors.Join(f.Close(), artDir.Sync(), stageDir.Sync(), staging.Sync()); err != nil {
		t.Fatal(err)
	}
}

func TestResumeRewriteMalformedForeignCandidate(t *testing.T) {
	t.Run("foreign_file_in_staging", testForeignFileInStaging)
	t.Run("foreign_artifact_in_staging", testForeignArtifactInStaging)
	t.Run("foreign_file_in_generations", testForeignFileInGenerations)
	t.Run("corrupt_manifest_digest_in_generations", testCorruptManifestDigestInGenerations)
	t.Run("both_staging_and_generations_exist", testBothStagingAndGenerationsExist)
	t.Run("source_generation_missing_from_catalog", testSourceGenMissingFromCatalog)
	t.Run("tampered_pending_sessions_in_catalog", testTamperedPendingSessionsInCatalog)
}

func testForeignFileInStaging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	catalog, _ := r.Inspect(t.Context())
	rw := catalog.Rewrites[catalog.rewriteIndex(mutationID)]

	staging, _ := r.root.Child("staging", false)
	stageDir, _ := staging.CreateDir(rw.Replacement)
	f, _ := stageDir.File("foreign_unknown.txt", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	_, _ = f.WriteString("foreign payload")
	_ = errors.Join(f.Close(), stageDir.Sync(), stageDir.Close(), staging.Sync(), staging.Close())

	_, err := r.ResumeRewrite(t.Context(), mutationID)
	if !errors.Is(err, ErrCorrupt) || !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrCorrupt and ErrRecoveryRequired on foreign stage file, got %v", err)
	}
}

func testForeignArtifactInStaging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	catalog, _ := r.Inspect(t.Context())
	rw := catalog.Rewrites[catalog.rewriteIndex(mutationID)]

	staging, _ := r.root.Child("staging", false)
	stageDir, _ := staging.CreateDir(rw.Replacement)
	artDir, _ := stageDir.CreateDir("artifacts")
	f, _ := artDir.File("foreign_artifact.bin.gz", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	_, _ = f.WriteString("fake")
	_ = errors.Join(f.Close(), artDir.Sync(), artDir.Close(), stageDir.Sync(), stageDir.Close(), staging.Sync(), staging.Close())

	_, err := r.ResumeRewrite(t.Context(), mutationID)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on foreign artifact in stage, got %v", err)
	}
}

func testForeignFileInGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	catalog, _ := r.Inspect(t.Context())
	rw := catalog.Rewrites[catalog.rewriteIndex(mutationID)]

	generations, _ := r.root.Child("generations", false)
	genDir, _ := generations.CreateDir(rw.Replacement)
	f, _ := genDir.File("unexpected.txt", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	_, _ = f.WriteString("unexpected")
	_ = errors.Join(f.Close(), genDir.Sync(), genDir.Close(), generations.Sync(), generations.Close())

	_, err := r.ResumeRewrite(t.Context(), mutationID)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on foreign generation file, got %v", err)
	}
}

func testCorruptManifestDigestInGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	catalog, _ := r.Inspect(t.Context())
	rw := catalog.Rewrites[catalog.rewriteIndex(mutationID)]

	generations, _ := r.root.Child("generations", false)
	genDir, _ := generations.CreateDir(rw.Replacement)
	artDir, _ := genDir.CreateDir("artifacts")
	_ = artDir.Close()
	f, _ := genDir.File("manifest.json", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	_, _ = f.WriteString(`{"invalid":"json"}`)
	_ = errors.Join(f.Close(), genDir.Sync(), genDir.Close(), generations.Sync(), generations.Close())

	_, err := r.ResumeRewrite(t.Context(), mutationID)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on corrupt manifest in generation, got %v", err)
	}
}

func testBothStagingAndGenerationsExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	catalog, _ := r.Inspect(t.Context())
	rw := catalog.Rewrites[catalog.rewriteIndex(mutationID)]

	staging, _ := r.root.Child("staging", false)
	stageDir, _ := staging.CreateDir(rw.Replacement)
	_ = errors.Join(stageDir.Close(), staging.Close())

	generations, _ := r.root.Child("generations", false)
	genDir, _ := generations.CreateDir(rw.Replacement)
	_ = errors.Join(genDir.Close(), generations.Close())

	_, err := r.ResumeRewrite(t.Context(), mutationID)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt when candidate exists in both staging and generations, got %v", err)
	}
}

func testSourceGenMissingFromCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	catalog, _ := r.Inspect(t.Context())
	catalog.Generations = slices.DeleteFunc(catalog.Generations, func(g Generation) bool { return g.ID == source.ID })

	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}

	rewriteFixtureFile(t, filepath.Join(path, "catalog.json"), encoded)

	_, err = r.ResumeRewrite(t.Context(), mutationID)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt when pending source is missing from catalog, got %v", err)
	}
}

func testTamperedPendingSessionsInCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	catalog, _ := r.Inspect(t.Context())
	idx := catalog.rewriteIndex(mutationID)
	catalog.Rewrites[idx].Artifacts = []string{"a0"}

	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}

	rewriteFixtureFile(t, filepath.Join(path, "catalog.json"), encoded)

	_, err = r.ResumeRewrite(t.Context(), mutationID)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt when registered sessions list was tampered, got %v", err)
	}
}

func TestResumeRewriteSaveOrderingAndCurrentWatermark(t *testing.T) {
	t.Run("rewrite_of_current_generation_makes_replacement_current", testRewriteCurrentMakesReplacementCurrent)
	t.Run("rewrite_of_old_generation_does_not_overwrite_newer_save", testRewriteOldGenPreservesNewerSaveCurrent)
	t.Run("retiring_old_generation_does_not_clear_newer_save_current", testRetiringOldGenPreservesNewerSaveCurrent)
}

func testRewriteCurrentMakesReplacementCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	catalog, _ := r.Inspect(t.Context())
	if catalog.Current != source.ID {
		t.Fatalf("expected current to be source %s, got %s", source.ID, catalog.Current)
	}

	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	res, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite failed: %v", err)
	}

	finalCatalog, _ := r.Inspect(t.Context())
	if finalCatalog.Current != res.Replacement.ID {
		t.Fatalf("expected Current to be replacement %s, got %s", res.Replacement.ID, finalCatalog.Current)
	}

	curr, err := r.Current(t.Context())
	if err != nil {
		t.Fatalf("Current() failed: %v", err)
	}

	if len(curr.Sessions) != 2 {
		t.Fatalf("expected 2 surviving sessions in current, got %d", len(curr.Sessions))
	}
}

func testRewriteOldGenPreservesNewerSaveCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	g1 := commitMultiSessionFixture(t, r)

	mutationID, _ := registerPendingRewriteFixture(t, r, g1.ID, []string{"s0"})

	g2 := commitMultiSessionFixture(t, r)

	catalogAfterSave, _ := r.Inspect(t.Context())
	if catalogAfterSave.Current != g2.ID {
		t.Fatalf("expected Current to be new save %s, got %s", g2.ID, catalogAfterSave.Current)
	}

	res, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite on old generation failed: %v", err)
	}

	finalCatalog, _ := r.Inspect(t.Context())
	if finalCatalog.Current != g2.ID {
		t.Fatalf("Current was incorrectly overwritten with old rewrite replacement %s, want %s", res.Replacement.ID, g2.ID)
	}

	curr, err := r.Current(t.Context())
	if err != nil {
		t.Fatalf("Current() failed: %v", err)
	}

	if len(curr.Sessions) != 3 {
		t.Fatalf("expected g2 with 3 sessions in Current, got %d", len(curr.Sessions))
	}
}

func testRetiringOldGenPreservesNewerSaveCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	g1 := commitMultiSessionFixture(t, r)

	mutationID, _ := registerPendingRewriteFixture(t, r, g1.ID, []string{"s0", "s1", "s2"})
	g2 := commitMultiSessionFixture(t, r)

	res, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite retirement failed: %v", err)
	}

	if !res.Empty || res.State != rewriteStateRetired {
		t.Fatalf("expected retired result, got %+v", res)
	}

	finalCatalog, _ := r.Inspect(t.Context())
	if finalCatalog.Current != g2.ID {
		t.Fatalf("retiring older generation cleared Current! got %s, want %s", finalCatalog.Current, g2.ID)
	}
}

func TestResumeRewriteDecompressedFixtureByteFidelity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	res, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite failed: %v", err)
	}

	expectedContents := map[string]string{
		"a0": "shared window content\n",
		"a1": "s2 exclusive content\n",
	}

	verifySurvivorFixtureBytes(t, r, res.Replacement.ID, expectedContents)
}

func verifySurvivorFixtureBytes(t *testing.T, r *Repository, genID string, expected map[string]string) {
	t.Helper()

	generations, err := r.root.Child("generations", false)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = generations.Close() }()

	genDir, err := generations.Child(genID, false)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = genDir.Close() }()

	artDir, err := genDir.Child("artifacts", false)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = artDir.Close() }()

	for id, expectedStr := range expected {
		verifyDecompressedFixtureFile(t, r, artDir, genID, id, expectedStr)
	}
}

func verifyDecompressedFixtureFile(t *testing.T, r *Repository, artDir *privatefs.Dir, genID, id, expectedStr string) {
	t.Helper()

	f, err := artDir.File(id+".bin.gz", unix.O_RDONLY)
	if err != nil {
		t.Fatalf("open artifact %s failed: %v", id, err)
	}

	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()

		t.Fatalf("gzip reader for %s failed: %v", id, err)
	}

	var decompressed bytes.Buffer

	_, copyErr := io.Copy(&decompressed, io.LimitReader(gz, maxFixtureDecompressLimit))
	closeErr := errors.Join(gz.Close(), f.Close())

	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatalf("decompressing artifact %s failed: %v", id, err)
	}

	if decompressed.String() != expectedStr {
		t.Fatalf("artifact %s content mismatch: got %q, want %q", id, decompressed.String(), expectedStr)
	}

	h := sha256.Sum256(decompressed.Bytes())
	expectedSHA := hex.EncodeToString(h[:])

	m, err := r.Read(t.Context(), genID)
	if err != nil {
		t.Fatal(err)
	}

	for _, a := range m.Artifacts {
		if a.ID == id && a.SHA256 != expectedSHA {
			t.Fatalf("manifest SHA256 mismatch for %s: got %s, want %s", id, a.SHA256, expectedSHA)
		}
	}
}

func TestResumeRewriteCancellationAndFaults(t *testing.T) {
	t.Run("canceled_context_before_call", testResumeCanceledContext)
	t.Run("injected_fault_pre_publish", testResumeFaultPrePublish)
	t.Run("injected_fault_catalog_replaced_uncertain", testResumeFaultCatalogReplaced)
}

func testResumeCanceledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := r.ResumeRewrite(ctx, mutationID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if oopsErr, ok := errors.AsType[oops.OopsError](err); ok {
		if oopsErr.Code() != "operation_canceled" {
			t.Fatalf("expected code operation_canceled, got %s", oopsErr.Code())
		}
	}
}

func testResumeFaultPrePublish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	r.fault = func(phase string) error {
		if phase == "rewrite_resume_pre_publish" {
			return errInjected
		}

		return nil
	}

	_, err := r.ResumeRewrite(t.Context(), mutationID)
	if !errors.Is(err, ErrRecoveryRequired) || !errors.Is(err, errInjected) {
		t.Fatalf("expected ErrRecoveryRequired and errInjected, got %v", err)
	}

	r.fault = nil

	res, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("second ResumeRewrite failed: %v", err)
	}

	if res.State != rewriteStateCompleted {
		t.Fatalf("unexpected res state: %s", res.State)
	}
}

func testResumeFaultCatalogReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	r.fault = func(phase string) error {
		if phase == "catalog_replaced" {
			return errInjected
		}

		return nil
	}

	_, err := r.ResumeRewrite(t.Context(), mutationID)
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("expected ErrUncertain on catalog_replaced fault, got %v", err)
	}

	r.fault = nil

	reconciled, err := r.ReconcileRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ReconcileRewrite failed: %v", err)
	}

	if reconciled.State != rewriteStateCompleted {
		t.Fatalf("expected completed state, got %s", reconciled.State)
	}

	res, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite after reconcile failed: %v", err)
	}

	if res.State != rewriteStateCompleted {
		t.Fatalf("expected completed state, got %s", res.State)
	}
}

func TestResumeRewriteConcurrentDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")

	rInit, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	source := commitMultiSessionFixture(t, rInit)
	_ = rInit.Close()

	rSetup, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	mutationID, _ := registerPendingRewriteFixture(t, rSetup, source.ID, []string{"s0"})
	_ = rSetup.Close()

	const concurrency = 16

	var wg sync.WaitGroup

	wg.Add(concurrency)

	results := make([]RewriteResult, concurrency)
	errs := make([]error, concurrency)

	for i := range concurrency {
		go func(idx int) {
			defer wg.Done()

			r, openErr := Open(t.Context(), path)
			if openErr != nil {
				errs[idx] = openErr

				return
			}

			defer func() { _ = r.Close() }()

			res, resumeErr := r.ResumeRewrite(t.Context(), mutationID)
			results[idx] = res
			errs[idx] = resumeErr
		}(i)
	}

	wg.Wait()

	verifyConcurrentDuplicateResults(t, errs, results)

	rFinal := openRepository(t, path)

	catalog, err := rFinal.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Rewrites) != 1 {
		t.Fatalf("expected exactly 1 rewrite in catalog, got %d", len(catalog.Rewrites))
	}

	if catalog.Rewrites[0].State != rewriteStateCompleted {
		t.Fatalf("expected completed state, got %s", catalog.Rewrites[0].State)
	}
}

func verifyConcurrentDuplicateResults(t *testing.T, errs []error, results []RewriteResult) {
	t.Helper()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v", i, err)
		}

		if results[i].State != rewriteStateCompleted {
			t.Fatalf("goroutine %d returned non-completed state: %s", i, results[i].State)
		}

		if results[i].Replacement.ID != results[0].Replacement.ID {
			t.Fatalf("goroutine %d returned replacement %s, want %s", i, results[i].Replacement.ID, results[0].Replacement.ID)
		}

		if results[i].Replacement.Sequence != results[0].Replacement.Sequence {
			t.Fatalf("goroutine %d sequence mismatch: %d vs %d", i, results[i].Replacement.Sequence, results[0].Replacement.Sequence)
		}
	}
}

func TestResumeRewriteProtectsPendingSourceFromPinsAndCollection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})

	target := model.Bytes("pending-target")
	actions := []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}}

	_, err := r.Pin(t.Context(), source.ID, target, actions)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict pinning pending rewrite source, got %v", err)
	}

	g2 := commitMultiSessionFixture(t, r)

	op, err := r.Pin(t.Context(), g2.ID, target, actions)
	if err != nil {
		t.Fatalf("pinning unrelated save g2 failed: %v", err)
	}

	if err := r.Finish(t.Context(), op.ID, true); err != nil {
		t.Fatalf("finish pin on g2 failed: %v", err)
	}

	collectRes, err := r.Collect(t.Context(), CollectionBudget{Generations: 10, Bytes: 10_000_000})
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	if len(collectRes.Intents) != 0 {
		t.Fatalf("expected 0 generations collected, got %d", len(collectRes.Intents))
	}

	res, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite failed: %v", err)
	}

	if res.State != rewriteStateCompleted {
		t.Fatalf("expected completed state, got %s", res.State)
	}

	collectRes2, err := r.Collect(t.Context(), CollectionBudget{Generations: 10, Bytes: 10_000_000})
	if err != nil {
		t.Fatalf("Collect after rewrite failed: %v", err)
	}

	if len(collectRes2.Intents) != 1 {
		t.Fatalf("expected 1 generation (source) collected, got %d", len(collectRes2.Intents))
	}

	if _, err = r.Read(t.Context(), res.Replacement.ID); err != nil {
		t.Fatalf("read replacement after GC failed: %v", err)
	}

	if _, err = r.Read(t.Context(), g2.ID); err != nil {
		t.Fatalf("read g2 after GC failed: %v", err)
	}
}
