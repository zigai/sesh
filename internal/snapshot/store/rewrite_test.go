//go:build linux || darwin

package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func multiSessionSnapshotFixture() (model.Manifest, map[string]io.Reader) {
	c0 := []byte("shared window content\n")
	c1 := []byte("s2 exclusive content\n")

	h0 := sha256.Sum256(c0)
	h1 := sha256.Sum256(c1)

	m := model.Manifest{
		Format:   "sesh",
		Version:  model.Schema,
		Started:  10,
		Finished: 20,
		Source:   model.Source{TmuxVersion: "3.6", LibraryVersion: "v0.3.0", Consistency: "checkpoint"},
		Sessions: []model.Session{
			{ID: "s0", Name: model.Bytes("work"), Group: "g0", Directory: model.Bytes("/tmp")},
			{ID: "s1", Name: model.Bytes("play"), Group: "g0", Directory: model.Bytes("/tmp")},
			{ID: "s2", Name: model.Bytes("other"), Directory: model.Bytes("/tmp")},
		},
		Windows: []model.Window{
			{ID: "w0", Name: model.Bytes("shared"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"}},
			{ID: "w1", Name: model.Bytes("code"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p1"}},
		},
		Links: []model.Link{
			{Session: "s0", Window: "w0", Index: 0, Active: true},
			{Session: "s1", Window: "w0", Index: 0, Active: true},
			{Session: "s2", Window: "w0", Index: 0, Active: true},
			{Session: "s2", Window: "w1", Index: 1, Active: false},
		},
		Panes: []model.Pane{
			{ID: "p0", Window: "w0", Directory: model.Bytes("/tmp"), Width: 80, Height: 24, Active: true, Terminal: model.Terminal{Artifact: "a0"}, Launch: model.Launch{Recovery: "unverified"}},
			{ID: "p1", Window: "w1", Directory: model.Bytes("/tmp"), Width: 80, Height: 24, Active: true, Terminal: model.Terminal{Artifact: "a1"}, Launch: model.Launch{Recovery: "unverified"}},
		},
		Groups: []model.Group{
			{ID: "g0", Members: []string{"s0", "s1"}},
		},
		Artifacts: []model.Artifact{
			{ID: "a0", Kind: "terminal-text", Length: int64(len(c0)), SHA256: hex.EncodeToString(h0[:])},
			{ID: "a1", Kind: "terminal-text", Length: int64(len(c1)), SHA256: hex.EncodeToString(h1[:])},
		},
	}

	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		m.Evidence = append(m.Evidence, model.Evidence{Domain: domain, Status: "verified", Reason: ""})
	}

	artifacts := map[string]io.Reader{
		"a0": bytes.NewReader(c0),
		"a1": bytes.NewReader(c1),
	}

	return m, artifacts
}

func commitMultiSessionFixture(t *testing.T, r *Repository) Generation {
	t.Helper()

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	m, artifacts := multiSessionSnapshotFixture()

	gen, err := r.Commit(t.Context(), attempt.ID, m, artifacts)
	if err != nil {
		t.Fatal(err)
	}

	return gen
}

func TestRemoveSessionsExactSurvivorPreservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID := newID()

	result, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0"})
	if err != nil {
		t.Fatalf("RemoveSessions failed: %v", err)
	}

	if result.Empty || result.State != rewriteStateCompleted {
		t.Fatalf("unexpected result state: empty=%v state=%s", result.Empty, result.State)
	}

	verifyExactSurvivorManifest(t, r, result)
	verifyExactSurvivorSecondRemoval(t, r, source, result)
}

func verifyExactSurvivorManifest(t *testing.T, r *Repository, result RewriteResult) {
	t.Helper()

	if !slices.Equal(result.Removed, []string{"s0"}) || len(result.Artifacts) != 0 {
		t.Fatalf("unexpected removed/artifacts: %v / %v", result.Removed, result.Artifacts)
	}

	survivorManifest, err := r.Read(t.Context(), result.Replacement.ID)
	if err != nil {
		t.Fatalf("read survivor generation failed: %v", err)
	}

	if len(survivorManifest.Sessions) != 2 || len(survivorManifest.Windows) != 2 {
		t.Fatalf("unexpected survivor sessions/windows: %d / %d", len(survivorManifest.Sessions), len(survivorManifest.Windows))
	}

	if len(survivorManifest.Groups) != 1 || !slices.Equal(survivorManifest.Groups[0].Members, []string{"s1"}) {
		t.Fatalf("expected group g0 trimmed to [s1], got %v", survivorManifest.Groups)
	}

	actualArtifacts := make([]string, len(survivorManifest.Artifacts))
	for i, a := range survivorManifest.Artifacts {
		actualArtifacts[i] = a.ID
	}

	if !slices.Equal(actualArtifacts, []string{"a0", "a1"}) {
		t.Fatalf("unexpected artifacts: %v", actualArtifacts)
	}
}

func verifyExactSurvivorSecondRemoval(t *testing.T, r *Repository, source Generation, result RewriteResult) {
	t.Helper()

	mutationID2 := newID()

	result2, err := r.RemoveSessions(t.Context(), mutationID2, result.Replacement.ID, []string{"s2"})
	if err != nil {
		t.Fatalf("RemoveSessions on survivor failed: %v", err)
	}

	if !slices.Equal(result2.Removed, []string{"s2"}) || !slices.Equal(result2.Artifacts, []string{"a1"}) {
		t.Fatalf("unexpected result2 removed/artifacts: %v / %v", result2.Removed, result2.Artifacts)
	}

	sourceManifest, err := r.readGeneration(t.Context(), source)
	if err != nil {
		t.Fatalf("read source generation from disk failed: %v", err)
	}

	if len(sourceManifest.Sessions) != 3 {
		t.Fatalf("source manifest was altered: %d sessions", len(sourceManifest.Sessions))
	}

	verifyExactSurvivorCatalog(t, r, source, result.Replacement.ID, result2.Replacement.ID)
}

func verifyExactSurvivorCatalog(t *testing.T, r *Repository, source Generation, intermediateID, finalID string) {
	t.Helper()

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.generationIndex(source.ID) >= 0 || catalog.generationIndex(intermediateID) >= 0 {
		t.Fatal("retired generations still present in catalog")
	}

	if catalog.generationIndex(finalID) < 0 {
		t.Fatal("final replacement missing from catalog generations")
	}

	sourceAttemptIdx := catalog.attemptIndex(source.ID)
	if sourceAttemptIdx < 0 || catalog.Attempts[sourceAttemptIdx].ManifestSHA256 != source.ManifestSHA256 {
		t.Fatal("source attempt digest missing or altered")
	}
}

func TestRemoveSessionsAllRetirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID := newID()

	result, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0", "s1", "s2"})
	if err != nil {
		t.Fatalf("RemoveSessions all failed: %v", err)
	}

	if !result.Empty || result.State != rewriteStateRetired || result.Replacement != (Generation{}) {
		t.Fatalf("unexpected retirement result: %v", result)
	}

	if !slices.Equal(result.Removed, []string{"s0", "s1", "s2"}) {
		t.Fatalf("expected removed [s0 s1 s2], got %v", result.Removed)
	}

	verifyRetirementCatalog(t, r, source.ID, mutationID)
}

func verifyRetirementCatalog(t *testing.T, r *Repository, sourceID, mutationID string) {
	t.Helper()

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.generationIndex(sourceID) >= 0 || len(catalog.Generations) != 0 || catalog.Current != "" {
		t.Fatalf("unexpected catalog state after retirement: gens=%d current=%s", len(catalog.Generations), catalog.Current)
	}

	if _, err := r.Current(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable from Current(), got %v", err)
	}

	reconciled, err := r.ReconcileRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ReconcileRewrite failed: %v", err)
	}

	if !reconciled.Empty || reconciled.State != rewriteStateRetired {
		t.Fatalf("unexpected reconciled state: %v", reconciled)
	}
}

func TestRemoveSessionsEmptySelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID := newID()

	result, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{})
	if err != nil {
		t.Fatalf("RemoveSessions empty selection failed: %v", err)
	}

	if result.Empty || len(result.Removed) != 0 {
		t.Fatalf("unexpected empty selection result: %v", result)
	}

	survivor, err := r.Read(t.Context(), result.Replacement.ID)
	if err != nil {
		t.Fatalf("read survivor failed: %v", err)
	}

	if len(survivor.Sessions) != 3 {
		t.Fatalf("expected 3 sessions in survivor, got %d", len(survivor.Sessions))
	}
}

func TestRemoveSessionsInvalidAndUnknownSelections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	if _, err := r.RemoveSessions(t.Context(), newID(), source.ID, []string{"s_unknown"}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("expected model.ErrInvalid on unknown session, got %v", err)
	}

	if _, err := r.RemoveSessions(t.Context(), newID(), source.ID, []string{"bad/id"}); !errors.Is(err, model.ErrInvalid) {
		t.Fatalf("expected model.ErrInvalid on malformed session, got %v", err)
	}

	if _, err := r.RemoveSessions(t.Context(), "short", source.ID, []string{"s0"}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on invalid mutation ID, got %v", err)
	}

	if _, err := r.RemoveSessions(t.Context(), newID(), "short", []string{"s0"}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on invalid source ID, got %v", err)
	}

	if _, err := r.RemoveSessions(t.Context(), newID(), newID(), []string{"s0"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable on unknown source generation, got %v", err)
	}

	oversized := make([]string, model.MaxEntities+1)
	for i := range oversized {
		oversized[i] = fmt.Sprintf("s%d", i)
	}

	if _, err := r.RemoveSessions(t.Context(), newID(), source.ID, oversized); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt on oversized selection, got %v", err)
	}
}

func TestRemoveSessionsCurrentVsNonCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	g1 := commitMultiSessionFixture(t, r)
	g2 := commitCompleteFixture(t, r)

	currentBefore, err := r.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if currentBefore.Sessions[0].ID != "s0" {
		t.Fatal("unexpected current before")
	}

	result1 := testRewriteNonCurrent(t, r, g1, g2)
	testRetireCurrent(t, r, result1.Replacement.ID, g2)
}

func testRewriteNonCurrent(t *testing.T, r *Repository, g1, g2 Generation) RewriteResult {
	t.Helper()

	id1 := newID()

	result1, err := r.RemoveSessions(t.Context(), id1, g1.ID, []string{"s0"})
	if err != nil {
		t.Fatalf("rewrite g1 failed: %v", err)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Current != g2.ID {
		t.Fatalf("Current was altered by non-current rewrite: got %s, want %s", catalog.Current, g2.ID)
	}

	currentAfter, err := r.Current(t.Context())
	if err != nil || currentAfter.Sessions[0].ID != "s0" {
		t.Fatalf("Current() failed after non-current rewrite: %v", err)
	}

	g1Prime, err := r.Read(t.Context(), result1.Replacement.ID)
	if err != nil || len(g1Prime.Sessions) != 2 {
		t.Fatalf("read survivor g1' failed: %v", err)
	}

	return result1
}

func testRetireCurrent(t *testing.T, r *Repository, g1PrimeID string, g2 Generation) {
	t.Helper()

	id2 := newID()

	result2, err := r.RemoveSessions(t.Context(), id2, g2.ID, []string{"s0"})
	if err != nil {
		t.Fatalf("retire Current failed: %v", err)
	}

	if !result2.Empty {
		t.Fatal("expected empty survivor")
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Current != "" {
		t.Fatalf("expected Current == \"\", got %s", catalog.Current)
	}

	if _, err := r.Current(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable from Current(), got %v", err)
	}

	if _, err := r.Read(t.Context(), g1PrimeID); err != nil {
		t.Fatalf("g1' not readable after Current retired: %v", err)
	}
}

func TestRemoveSessionsActiveRecoveryPinConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	target := model.Bytes("test-pin-target")
	actions := []Action{{ID: "act0", Kind: actionLaunch, State: "planned"}}

	op, err := r.Pin(t.Context(), source.ID, target, actions)
	if err != nil {
		t.Fatalf("Pin failed: %v", err)
	}

	mutationID := newID()

	if _, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on pinned source, got %v", err)
	}

	if err := r.Finish(t.Context(), op.ID, true); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	result, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0"})
	if err != nil {
		t.Fatalf("RemoveSessions after Finish failed: %v", err)
	}

	if result.Empty {
		t.Fatal("expected non-empty survivor")
	}
}

func TestRemoveSessionsConcurrentPendingConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	r.fault = func(phase string) error {
		if phase == "rewrite_registered" {
			return errInjected
		}

		return nil
	}

	mutationID := newID()

	if _, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0"}); !errors.Is(err, errInjected) {
		t.Fatalf("expected injected fault, got %v", err)
	}

	r.fault = nil

	if _, err := r.Pin(t.Context(), source.ID, model.Bytes("target"), []Action{{ID: "a0", Kind: actionLaunch, State: "planned"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict when pinning source of pending rewrite, got %v", err)
	}

	if _, err := r.RemoveSessions(t.Context(), newID(), source.ID, []string{"s1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict when rewriting source of pending rewrite, got %v", err)
	}

	if err := r.Delete(t.Context(), source.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict when deleting protected source, got %v", err)
	}

	if _, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0"}); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired on retrying pending rewrite, got %v", err)
	}

	if _, err := r.ReconcileRewrite(t.Context(), mutationID); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired from ReconcileRewrite, got %v", err)
	}
}

func TestRemoveSessionsCallerIDConflictAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source1 := commitMultiSessionFixture(t, r)
	source2 := commitCompleteFixture(t, r)

	mutationID := newID()

	result1, err := r.RemoveSessions(t.Context(), mutationID, source1.ID, []string{"s0"})
	if err != nil {
		t.Fatalf("initial RemoveSessions failed: %v", err)
	}

	replay, err := r.RemoveSessions(t.Context(), mutationID, source1.ID, []string{"s0"})
	if err != nil {
		t.Fatalf("replay RemoveSessions failed: %v", err)
	}

	if diff := cmp.Diff(result1, replay); diff != "" {
		t.Fatalf("replay result differed: %s", diff)
	}

	replayDup, err := r.RemoveSessions(t.Context(), mutationID, source1.ID, []string{"s0", "s0"})
	if err != nil {
		t.Fatalf("replay with duplicates failed: %v", err)
	}

	if diff := cmp.Diff(result1, replayDup); diff != "" {
		t.Fatalf("replay with duplicates differed: %s", diff)
	}

	if _, err := r.RemoveSessions(t.Context(), mutationID, source2.ID, []string{"s0"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on different source, got %v", err)
	}

	if _, err := r.RemoveSessions(t.Context(), mutationID, source1.ID, []string{"s1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on different selection, got %v", err)
	}
}

func TestReconcileRewriteCallerDeath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID := newID()

	result, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0"})
	if err != nil {
		t.Fatal(err)
	}

	reopened := openRepository(t, path)

	reconciled, err := reopened.ReconcileRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ReconcileRewrite on fresh handle failed: %v", err)
	}

	if diff := cmp.Diff(result, reconciled); diff != "" {
		t.Fatalf("reconciled result differed: %s", diff)
	}

	if _, err := reopened.ReconcileRewrite(t.Context(), newID()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for unknown ID, got %v", err)
	}
}

func TestRemoveSessionsCallerOwnedResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID := newID()

	result, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s2"})
	if err != nil {
		t.Fatal(err)
	}

	result.Removed[0] = "mutated"
	result.Artifacts[0] = "mutated"

	reconciled, err := r.ReconcileRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatal(err)
	}

	if reconciled.Removed[0] != "s2" || reconciled.Artifacts[0] != "a1" {
		t.Fatalf("caller mutation affected catalog: %v / %v", reconciled.Removed, reconciled.Artifacts)
	}
}

func TestRemoveSessionsIncompleteSourcePreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	m, artifacts := multiSessionSnapshotFixture()
	m.Source.Consistency = "observed"

	for i := range m.Evidence {
		m.Evidence[i].Status = "unverified"
		m.Evidence[i].Reason = "not_captured"
	}

	sourceGen, err := r.Commit(t.Context(), attempt.ID, m, artifacts)
	if err != nil {
		t.Fatal(err)
	}

	if sourceGen.Complete {
		t.Fatal("expected incomplete source")
	}

	mutationID := newID()

	result, err := r.RemoveSessions(t.Context(), mutationID, sourceGen.ID, []string{"s0"})
	if err != nil {
		t.Fatalf("rewrite incomplete source failed: %v", err)
	}

	if result.Replacement.Complete {
		t.Fatal("expected survivor generation to remain incomplete")
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	attemptIdx := catalog.attemptIndex(result.Replacement.ID)
	if attemptIdx < 0 || catalog.Attempts[attemptIdx].State != "incomplete" {
		t.Fatalf("expected incomplete attempt in catalog: %v", catalog.Attempts)
	}
}

func TestRemoveSessionsSubsequentCollectionAndIdentitySurvival(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)

	mutationID := newID()

	result, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0", "s1", "s2"})
	if err != nil {
		t.Fatalf("RemoveSessions failed: %v", err)
	}

	collectResult, err := r.Collect(t.Context(), CollectionBudget{Generations: 10, Bytes: 10 << 20})
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	if !slices.Contains(collectResult.Removed, source.ID) {
		t.Fatalf("expected source %s in collectResult.Removed, got %v", source.ID, collectResult.Removed)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	rwIdx := catalog.rewriteIndex(mutationID)
	if rwIdx < 0 || catalog.Rewrites[rwIdx].State != rewriteStateRetired {
		t.Fatalf("unexpected rewrite state after collection: %v", catalog.Rewrites)
	}

	reconciled, err := r.ReconcileRewrite(t.Context(), mutationID)
	if err != nil {
		t.Fatalf("ReconcileRewrite after collection failed: %v", err)
	}

	if diff := cmp.Diff(result, reconciled); diff != "" {
		t.Fatalf("reconciled after collection differed: %s", diff)
	}
}

func TestRemoveSessionsFaultBoundaries(t *testing.T) {
	for _, boundary := range []string{"rewrite_stage_synced", "rewrite_generation_published", "catalog_replaced_pending", "catalog_replaced_commit"} {
		t.Run(boundary, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			source := commitMultiSessionFixture(t, r)

			runFaultBoundaryScenario(t, path, r, source, boundary)
		})
	}
}

func runFaultBoundaryScenario(t *testing.T, path string, r *Repository, source Generation, boundary string) {
	t.Helper()

	catalogReplacedCount := 0

	r.fault = func(phase string) error {
		if phase == "catalog_replaced" {
			catalogReplacedCount++
			if (boundary == "catalog_replaced_pending" && catalogReplacedCount == 1) ||
				(boundary == "catalog_replaced_commit" && catalogReplacedCount == 2) {
				return errInjected
			}
		}

		if phase == boundary {
			return errInjected
		}

		return nil
	}

	mutationID := newID()

	if _, err := r.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0"}); !errors.Is(err, errInjected) {
		t.Fatalf("expected injected fault at %s, got %v", boundary, err)
	}

	r.fault = nil

	verifyFaultBoundaryRecovery(t, path, mutationID, source, boundary)
}

func verifyFaultBoundaryRecovery(t *testing.T, path, mutationID string, source Generation, boundary string) {
	t.Helper()

	reopened := openRepository(t, path)

	if boundary == "catalog_replaced_commit" {
		result, err := reopened.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0"})
		if err != nil || result.State != rewriteStateCompleted {
			t.Fatalf("expected replay after commit replacement to succeed: %v / %s", err, result.State)
		}

		return
	}

	if _, err := reopened.RemoveSessions(t.Context(), mutationID, source.ID, []string{"s0"}); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired after interruption at %s, got %v", boundary, err)
	}

	if _, err := reopened.readGeneration(t.Context(), source); err != nil {
		t.Fatalf("source damaged by interruption at %s: %v", boundary, err)
	}
}
