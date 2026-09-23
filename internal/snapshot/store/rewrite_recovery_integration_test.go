//go:build integration && (linux || darwin)

package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	resumeWorkerModePartialArtifact = "partial_artifact"
	resumeWorkerModeResumeInterrupt = "resume_interrupt"
	resumeWorkerModeVerify          = "verify"
	maxArtifactDecompressLimit      = 10_000_000
)

func TestProcessDeathResumeRewriteBoundaries(t *testing.T) {
	testCases := []struct {
		name     string
		boundary string
		kind     string
		custom   string
		partial  bool
	}{
		{
			name:     "replacement_stage_synced_sigkill",
			boundary: boundaryRewriteStageSynced,
			kind:     rewriteKindReplacement,
		},
		{
			name:     "replacement_generation_published_sigkill",
			boundary: boundaryRewriteGenPublished,
			kind:     rewriteKindReplacement,
		},
		{
			name:     "replacement_pending_catalog_replaced_sigkill",
			boundary: boundaryCatalogReplacedPending,
			kind:     rewriteKindReplacement,
		},
		{
			name:     "retirement_pending_catalog_replaced_sigkill",
			boundary: boundaryCatalogReplacedPending,
			kind:     rewriteKindRetirement,
		},
		{
			name:     "replacement_partial_artifact_sigkill",
			boundary: "rewrite_registered",
			kind:     rewriteKindReplacement,
			custom:   resumeWorkerModePartialArtifact,
		},
		{
			name:     "interruption_of_resume_itself_sigkill",
			boundary: boundaryRewriteStageSynced,
			kind:     rewriteKindReplacement,
			custom:   resumeWorkerModeResumeInterrupt,
		},
		{name: "partial_compressed_body", boundary: "gzip_body", kind: rewriteKindReplacement, custom: resumeWorkerModePartialArtifact},
		{name: "second_death_during_partial_repair", boundary: "rewrite_resume_artifact_synced", kind: rewriteKindReplacement, custom: resumeWorkerModeResumeInterrupt, partial: true},
		{name: "replacement_resume_catalog_synced", boundary: "catalog_synced", kind: rewriteKindReplacement, custom: resumeWorkerModeResumeInterrupt},
		{name: "replacement_resume_catalog_replaced", boundary: "catalog_replaced", kind: rewriteKindReplacement, custom: resumeWorkerModeResumeInterrupt},
		{name: "retirement_resume_catalog_synced", boundary: "catalog_synced", kind: rewriteKindRetirement, custom: resumeWorkerModeResumeInterrupt},
		{name: "retirement_resume_catalog_replaced", boundary: "catalog_replaced", kind: rewriteKindRetirement, custom: resumeWorkerModeResumeInterrupt},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			source := commitMultiSessionFixture(t, r)

			mutationID := newID()
			if tc.partial {
				killWorkerAtBoundary(t, resumeWorkerCmd(t, resumeWorkerModePartialArtifact, path, mutationID, source.ID, "gzip_body"))
			} else if tc.custom == resumeWorkerModeResumeInterrupt {
				sessions := []string{"s0"}
				if tc.kind == rewriteKindRetirement {
					sessions = []string{"s0", "s1", "s2"}
				}

				mutationID, _ = registerPendingRewriteFixture(t, r, source.ID, sessions)
			}

			var cmd *exec.Cmd
			if tc.custom != "" {
				cmd = resumeWorkerCmd(t, tc.custom, path, mutationID, source.ID, tc.boundary)
			} else {
				cmd = rewriteWorker(t, "rewrite", path, mutationID, source.ID, tc.boundary, tc.kind)
			}

			killWorkerAtBoundary(t, cmd)

			verifyCmd := resumeWorkerCmd(t, resumeWorkerModeVerify, path, mutationID, source.ID, tc.boundary)

			output, err := verifyCmd.CombinedOutput()
			if err != nil {
				t.Fatalf("fresh process resume recovery failed for %s: %v\n%s", tc.name, err, output)
			}
		})
	}
}

func resumeWorkerCmd(t *testing.T, mode, path, id, source, boundary string) *exec.Cmd {
	t.Helper()

	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, program, "-test.run=^TestResumeRewriteWorker$")
	cmd.Env = []string{
		"SESH_TEST_RESUME_WORKER=" + mode,
		"SESH_TEST_RESUME_ROOT=" + path,
		"SESH_TEST_RESUME_ID=" + id,
		"SESH_TEST_RESUME_SOURCE=" + source,
		"SESH_TEST_RESUME_PHASE=" + boundary,
	}

	return cmd
}

func TestResumeRewriteWorker(t *testing.T) {
	mode := os.Getenv("SESH_TEST_RESUME_WORKER")
	if mode == "" {
		t.Skip("subprocess entry point")
	}

	path := os.Getenv("SESH_TEST_RESUME_ROOT")
	mutationID := os.Getenv("SESH_TEST_RESUME_ID")
	sourceID := os.Getenv("SESH_TEST_RESUME_SOURCE")
	boundary := os.Getenv("SESH_TEST_RESUME_PHASE")

	switch mode {
	case resumeWorkerModePartialArtifact:
		executePartialArtifactWorkerSubprocess(t, path, mutationID, sourceID, boundary)
	case resumeWorkerModeResumeInterrupt:
		executeResumeInterruptWorkerSubprocess(t, path, mutationID, boundary)
	case resumeWorkerModeVerify:
		executeVerifyWorkerSubprocess(t, path, mutationID, sourceID)
	default:
		t.Fatalf("unknown resume worker mode: %s", mode)
	}
}

func executePartialArtifactWorkerSubprocess(t *testing.T, path, mutationID, sourceID, boundary string) {
	t.Helper()

	r := openRepository(t, path)
	ready := os.NewFile(3, "resume-boundary")

	t.Cleanup(func() {
		if err := ready.Close(); err != nil {
			t.Error(err)
		}
	})

	writes := 0
	r.fault = func(current string) error {
		if current != "rewrite_artifact_copy" {
			return nil
		}

		writes++
		if boundary == "gzip_body" && writes < 2 {
			return nil
		}

		if _, err := ready.Write([]byte{1}); err != nil {
			return fmt.Errorf("signal boundary: %w", err)
		}

		<-t.Context().Done()

		return errInjected
	}

	sessions := []string{"s0"}
	if _, err := r.RemoveSessions(t.Context(), mutationID, sourceID, sessions); err != nil {
		t.Fatal(err)
	}

	t.Fatal("partial artifact worker escaped interruption")
}

func executeResumeInterruptWorkerSubprocess(t *testing.T, path, mutationID, boundary string) {
	t.Helper()

	rSetup := openRepository(t, path)
	ready := os.NewFile(3, "resume-boundary")

	t.Cleanup(func() {
		if err := ready.Close(); err != nil {
			t.Error(err)
		}
	})

	rSetup.fault = func(current string) error {
		if current == boundary {
			if _, err := ready.Write([]byte{1}); err != nil {
				return fmt.Errorf("signal boundary: %w", err)
			}

			<-t.Context().Done()

			return errInjected
		}

		return nil
	}

	if _, err := rSetup.ResumeRewrite(t.Context(), mutationID); err != nil {
		t.Fatal(err)
	}

	t.Fatal("resume interrupt worker escaped interruption")
}

func executeVerifyWorkerSubprocess(t *testing.T, path, mutationID, sourceID string) {
	t.Helper()

	r := openRepository(t, path)

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatalf("Inspect catalog failed: %v", err)
	}

	rwIdx := catalog.rewriteIndex(mutationID)
	if rwIdx < 0 {
		t.Fatalf("rewrite %s not found in catalog", mutationID)
	}

	verifyReconcileAndIdempotentResume(t, r, catalog, mutationID)

	result, err := r.ResumeRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ResumeRewrite failed in fresh process: %v", err)
	}

	finalCatalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatalf("final Inspect catalog failed: %v", err)
	}

	if result.Empty {
		verifyRetirementFinalCatalog(t, r, finalCatalog, sourceID, result)
	} else {
		verifyReplacementFinalCatalog(t, r, finalCatalog, sourceID, result)
	}
}

func verifyReconcileAndIdempotentResume(t *testing.T, r *Repository, catalog Catalog, mutationID string) {
	t.Helper()

	rwIdx := catalog.rewriteIndex(mutationID)

	_, recErr := r.ReconcileRewrite(t.Context(), mutationID)
	if catalog.Rewrites[rwIdx].State == rewriteStatePending {
		if !errors.Is(recErr, ErrRecoveryRequired) {
			t.Fatalf("expected ErrRecoveryRequired from ReconcileRewrite for pending, got %v", recErr)
		}
	} else if recErr != nil {
		t.Fatalf("ReconcileRewrite failed: %v", recErr)
	}
}

func verifyRetirementFinalCatalog(t *testing.T, r *Repository, finalCatalog Catalog, sourceID string, result RewriteResult) {
	t.Helper()

	if result.State != rewriteStateRetired {
		t.Fatalf("expected retired result state, got %+v", result)
	}

	if finalCatalog.generationIndex(sourceID) >= 0 {
		t.Fatalf("source %s must be retired from generations", sourceID)
	}

	if finalCatalog.Current != "" {
		t.Fatalf("expected Current to be empty, got %s", finalCatalog.Current)
	}

	if _, err := r.Current(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable from Current(), got %v", err)
	}
}

func verifyReplacementFinalCatalog(t *testing.T, r *Repository, finalCatalog Catalog, sourceID string, result RewriteResult) {
	t.Helper()

	if result.State != rewriteStateCompleted {
		t.Fatalf("expected completed replacement result, got %+v", result)
	}

	if finalCatalog.generationIndex(sourceID) >= 0 {
		t.Fatalf("source %s must be removed from generations", sourceID)
	}

	if finalCatalog.generationIndex(result.Replacement.ID) < 0 {
		t.Fatalf("replacement %s must be present in generations", result.Replacement.ID)
	}

	if finalCatalog.Current != result.Replacement.ID {
		t.Fatalf("expected Current to be replacement %s, got %s", result.Replacement.ID, finalCatalog.Current)
	}

	survivorManifest, err := r.Read(t.Context(), result.Replacement.ID)
	if err != nil {
		t.Fatalf("Read replacement generation failed: %v", err)
	}

	if len(survivorManifest.Sessions) != 2 {
		t.Fatalf("expected 2 survivor sessions, got %d", len(survivorManifest.Sessions))
	}

	verifyDecompressedArtifacts(t, r, result.Replacement.ID, map[string]string{
		"a0": "shared window content\n",
		"a1": "s2 exclusive content\n",
	})
}

func verifyDecompressedArtifacts(t *testing.T, r *Repository, genID string, expected map[string]string) {
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

	for id, expectedContent := range expected {
		verifySingleArtifact(t, r, artDir, genID, id, expectedContent)
	}
}

func verifySingleArtifact(t *testing.T, r *Repository, artDir *privatefs.Dir, genID, id, expectedContent string) {
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

	_, copyErr := io.Copy(&decompressed, io.LimitReader(gz, maxArtifactDecompressLimit))
	closeErr := errors.Join(gz.Close(), f.Close())

	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatalf("decompressing artifact %s failed: %v", id, err)
	}

	if decompressed.String() != expectedContent {
		t.Fatalf("decompressed artifact %s content mismatch: got %q, want %q", id, decompressed.String(), expectedContent)
	}

	h := sha256.Sum256(decompressed.Bytes())
	expectedSHA := hex.EncodeToString(h[:])

	m, err := r.Read(t.Context(), genID)
	if err != nil {
		t.Fatal(err)
	}

	verifyArtifactSHAInManifest(t, m.Artifacts, id, expectedSHA)
}

func verifyArtifactSHAInManifest(t *testing.T, artifacts []model.Artifact, id, expectedSHA string) {
	t.Helper()

	found := false

	for _, a := range artifacts {
		if a.ID == id {
			found = true

			if a.SHA256 != expectedSHA {
				t.Fatalf("SHA256 mismatch for %s: got %s, want %s", id, a.SHA256, expectedSHA)
			}
		}
	}

	if !found {
		t.Fatalf("artifact %s not found in manifest", id)
	}
}
