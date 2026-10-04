//go:build integration && (linux || darwin)

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/testsupport/testenv"
)

const (
	rewriteKindReplacement = "replacement"
	rewriteKindRetirement  = "retirement"

	boundaryCatalogSyncedPending   = "catalog_synced_pending"
	boundaryCatalogReplacedPending = "catalog_replaced_pending"
	boundaryRewriteStageSynced     = "rewrite_stage_synced"
	boundaryRewriteGenPublished    = "rewrite_generation_published"
	boundaryCatalogSyncedTerminal  = "catalog_synced_terminal"
	boundaryCatalogReplacedTerm    = "catalog_replaced_terminal"
)

func TestProcessDeathAtRewriteBoundaries(t *testing.T) {
	testCases := []struct {
		kind     string
		boundary string
	}{
		{rewriteKindReplacement, boundaryCatalogSyncedPending},
		{rewriteKindReplacement, boundaryCatalogReplacedPending},
		{rewriteKindReplacement, boundaryRewriteStageSynced},
		{rewriteKindReplacement, boundaryRewriteGenPublished},
		{rewriteKindReplacement, boundaryCatalogSyncedTerminal},
		{rewriteKindReplacement, boundaryCatalogReplacedTerm},
		{rewriteKindRetirement, boundaryCatalogSyncedPending},
		{rewriteKindRetirement, boundaryCatalogReplacedPending},
		{rewriteKindRetirement, boundaryCatalogSyncedTerminal},
		{rewriteKindRetirement, boundaryCatalogReplacedTerm},
	}

	for _, tc := range testCases {
		name := fmt.Sprintf("%s/%s", tc.kind, tc.boundary)
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			source := commitMultiSessionFixture(t, r)

			mutationID := newID()

			cmd := rewriteWorker(t, "rewrite", path, mutationID, source.ID, tc.boundary, tc.kind)
			killWorkerAtBoundary(t, cmd)

			verifyCmd := rewriteWorker(t, "verify", path, mutationID, source.ID, tc.boundary, tc.kind)
			if output, err := verifyCmd.CombinedOutput(); err != nil {
				t.Fatalf("fresh process recovery failed for %s: %v\n%s", name, err, output)
			}
		})
	}
}

func rewriteWorker(t *testing.T, mode, path, id, source, boundary, kind string) *exec.Cmd {
	t.Helper()

	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, program, "-test.run=^TestRewriteWorker$")
	cmd.Env = []string{
		testenv.IsolatedMarker + "=1",
		"SESH_TEST_REWRITE_WORKER=" + mode,
		"SESH_TEST_REWRITE_ROOT=" + path,
		"SESH_TEST_REWRITE_ID=" + id,
		"SESH_TEST_REWRITE_SOURCE=" + source,
		"SESH_TEST_REWRITE_PHASE=" + boundary,
		"SESH_TEST_REWRITE_KIND=" + kind,
	}

	return cmd
}

func TestRewriteWorker(t *testing.T) {
	mode := os.Getenv("SESH_TEST_REWRITE_WORKER")
	if mode == "" {
		t.Skip("subprocess entry point")
	}

	path := os.Getenv("SESH_TEST_REWRITE_ROOT")
	mutationID := os.Getenv("SESH_TEST_REWRITE_ID")
	sourceID := os.Getenv("SESH_TEST_REWRITE_SOURCE")
	boundary := os.Getenv("SESH_TEST_REWRITE_PHASE")
	kind := os.Getenv("SESH_TEST_REWRITE_KIND")

	if mode == "verify" {
		verifyRewriteProcessDeath(t, path, mutationID, sourceID, boundary, kind)
		return
	}

	if mode != "rewrite" {
		t.Fatal("invalid rewrite worker mode")
	}

	executeRewriteWorkerSubprocess(t, path, mutationID, sourceID, boundary, kind)
}

func executeRewriteWorkerSubprocess(t *testing.T, path, mutationID, sourceID, boundary, kind string) {
	t.Helper()

	r := openRepository(t, path)

	ready := os.NewFile(3, "rewrite-boundary")

	t.Cleanup(func() {
		if err := ready.Close(); err != nil {
			t.Error(err)
		}
	})

	var catalogSyncedCount, catalogReplacedCount int

	r.fault = func(current string) error {
		if checkBoundaryTrigger(current, boundary, &catalogSyncedCount, &catalogReplacedCount) {
			if _, err := ready.Write([]byte{1}); err != nil {
				return fmt.Errorf("signal rewrite boundary: %w", err)
			}

			<-t.Context().Done()

			return errInjected
		}

		return nil
	}

	sessions := []string{"s0"}
	if kind == rewriteKindRetirement {
		sessions = []string{"s0", "s1", "s2"}
	}

	if _, err := r.RemoveSessions(t.Context(), mutationID, sourceID, sessions); err != nil {
		t.Fatal(err)
	}

	t.Fatal("rewrite worker escaped interruption")
}

func checkBoundaryTrigger(current, boundary string, syncedCount, replacedCount *int) bool {
	switch current {
	case "catalog_synced":
		*syncedCount++

		return (boundary == boundaryCatalogSyncedPending && *syncedCount == 1) ||
			(boundary == boundaryCatalogSyncedTerminal && *syncedCount == 2)
	case "catalog_replaced":
		*replacedCount++

		return (boundary == boundaryCatalogReplacedPending && *replacedCount == 1) ||
			(boundary == boundaryCatalogReplacedTerm && *replacedCount == 2)
	default:
		return current == boundary
	}
}

func verifyRewriteProcessDeath(t *testing.T, path, mutationID, sourceID, boundary, kind string) {
	t.Helper()

	r := openRepository(t, path)

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatalf("catalog corrupt after process death at %s: %v", boundary, err)
	}

	switch boundary {
	case boundaryCatalogSyncedPending:
		verifySyncedPendingState(t, r, catalog, mutationID, sourceID, kind)
	case boundaryCatalogReplacedTerm:
		verifyReplacedTerminalState(t, r, catalog, mutationID, sourceID, kind)
	default:
		verifyPendingProtectedState(t, r, catalog, mutationID, sourceID, boundary, kind)
	}
}

func verifySyncedPendingState(t *testing.T, r *Repository, catalog Catalog, mutationID, sourceID, kind string) {
	t.Helper()

	if catalog.rewriteIndex(mutationID) >= 0 {
		t.Fatal("pending rewrite should not be visible before first catalog rename")
	}

	if catalog.generationIndex(sourceID) < 0 {
		t.Fatal("source must still be present in catalog generations")
	}

	if catalog.Current != sourceID {
		t.Fatalf("current altered before rename: got %s, want %s", catalog.Current, sourceID)
	}

	if _, err := r.ReconcileRewrite(t.Context(), mutationID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable before registration rename, got %v", err)
	}

	target := model.Bytes("verify-pin-target")
	actions := []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}}

	op, err := r.Pin(t.Context(), sourceID, target, actions)
	if err != nil {
		t.Fatalf("pinning source must succeed when unpinned: %v", err)
	}

	if err := r.Finish(t.Context(), op.ID, true); err != nil {
		t.Fatalf("finish pin failed: %v", err)
	}

	sessions := []string{"s0"}
	if kind == rewriteKindRetirement {
		sessions = []string{"s0", "s1", "s2"}
	}

	result, err := r.RemoveSessions(t.Context(), mutationID, sourceID, sessions)
	if err != nil {
		t.Fatalf("fresh rewrite must succeed after interrupted pre-rename attempt: %v", err)
	}

	if (kind == rewriteKindRetirement) != result.Empty {
		t.Fatalf("unexpected rewrite outcome: kind=%s empty=%v", kind, result.Empty)
	}
}

func verifyPendingProtectedState(t *testing.T, r *Repository, catalog Catalog, mutationID, sourceID, boundary, kind string) {
	t.Helper()

	rwIdx := catalog.rewriteIndex(mutationID)
	if rwIdx < 0 || catalog.Rewrites[rwIdx].State != rewriteStatePending {
		t.Fatalf("expected pending rewrite in catalog at %s: %v", boundary, catalog.Rewrites)
	}

	if catalog.generationIndex(sourceID) < 0 {
		t.Fatalf("source lost from generations after process death at %s", boundary)
	}

	genIdx := catalog.generationIndex(sourceID)
	if _, err := r.readGeneration(t.Context(), catalog.Generations[genIdx]); err != nil {
		t.Fatalf("source damaged by process death at %s: %v", boundary, err)
	}

	sessions := []string{"s0"}
	if kind == rewriteKindRetirement {
		sessions = []string{"s0", "s1", "s2"}
	}

	if _, err := r.RemoveSessions(t.Context(), mutationID, sourceID, sessions); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired on retry at %s, got %v", boundary, err)
	}

	if _, err := r.ReconcileRewrite(t.Context(), mutationID); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired from ReconcileRewrite at %s, got %v", boundary, err)
	}

	if err := r.Delete(t.Context(), sourceID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict deleting protected source at %s, got %v", boundary, err)
	}

	target := model.Bytes("pending-pin-target")
	actions := []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}}

	if _, err := r.Pin(t.Context(), sourceID, target, actions); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict pinning protected source at %s, got %v", boundary, err)
	}
}

func verifyReplacedTerminalState(t *testing.T, r *Repository, catalog Catalog, mutationID, sourceID, kind string) {
	t.Helper()

	if catalog.generationIndex(sourceID) >= 0 {
		t.Fatal("source must be removed from catalog generations after terminal replacement")
	}

	rwIdx := catalog.rewriteIndex(mutationID)
	if rwIdx < 0 {
		t.Fatal("rewrite record missing after terminal replacement")
	}

	reconciled, err := r.ReconcileRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ReconcileRewrite failed after terminal replacement: %v", err)
	}

	sessions := []string{"s0"}
	if kind == rewriteKindRetirement {
		sessions = []string{"s0", "s1", "s2"}
	}

	replayed, err := r.RemoveSessions(t.Context(), mutationID, sourceID, sessions)
	if err != nil {
		t.Fatalf("replay RemoveSessions failed after terminal replacement: %v", err)
	}

	if reconciled.State != replayed.State || reconciled.Empty != replayed.Empty {
		t.Fatalf("reconcile and replay outcomes differed: %v vs %v", reconciled, replayed)
	}

	if kind == rewriteKindReplacement {
		verifyTerminalReplacementOutcome(t, r, catalog, reconciled)
	} else {
		verifyTerminalRetirementOutcome(t, r, catalog, reconciled)
	}
}

func verifyTerminalReplacementOutcome(t *testing.T, r *Repository, catalog Catalog, res RewriteResult) {
	t.Helper()

	if res.State != rewriteStateCompleted || res.Empty {
		t.Fatalf("unexpected replacement state: %s empty=%v", res.State, res.Empty)
	}

	if catalog.generationIndex(res.Replacement.ID) < 0 {
		t.Fatal("replacement generation missing from catalog generations")
	}

	if catalog.Current != res.Replacement.ID {
		t.Fatalf("expected Current to be replacement %s, got %s", res.Replacement.ID, catalog.Current)
	}

	currentManifest, err := r.Current(t.Context())
	if err != nil {
		t.Fatalf("Current() failed after terminal replacement: %v", err)
	}

	if len(currentManifest.Sessions) != 2 {
		t.Fatalf("expected 2 surviving sessions in current, got %d", len(currentManifest.Sessions))
	}

	actualArtifacts := make([]string, 0, len(currentManifest.Artifacts))
	for _, a := range currentManifest.Artifacts {
		actualArtifacts = append(actualArtifacts, a.ID)
	}

	if !slices.Equal(actualArtifacts, []string{"a0", "a1"}) {
		t.Fatalf("unexpected surviving artifacts: %v", actualArtifacts)
	}
}

func verifyTerminalRetirementOutcome(t *testing.T, r *Repository, catalog Catalog, res RewriteResult) {
	t.Helper()

	if res.State != rewriteStateRetired || !res.Empty {
		t.Fatalf("unexpected retirement state: %s empty=%v", res.State, res.Empty)
	}

	if len(catalog.Generations) != 0 {
		t.Fatalf("expected 0 generations after retirement, got %d", len(catalog.Generations))
	}

	if catalog.Current != "" {
		t.Fatalf("expected Current to be empty after retirement, got %s", catalog.Current)
	}

	if _, err := r.Current(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable from Current() after retirement, got %v", err)
	}
}
