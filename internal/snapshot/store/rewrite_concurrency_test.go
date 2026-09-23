//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestConcurrentRewriteAndSameSourcePinRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	rMain := openRepository(t, path)
	source := commitMultiSessionFixture(t, rMain)

	r1 := openRepository(t, path)
	r2 := openRepository(t, path)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	var (
		rewriteRes RewriteResult
		pinnedOp   Operation
		rewriteErr error
		pinErr     error
	)

	wg.Add(2)

	go func() {
		defer wg.Done()

		rewriteRes, rewriteErr = r1.RemoveSessions(ctx, newID(), source.ID, []string{"s0"})
	}()

	go func() {
		defer wg.Done()

		target := model.Bytes("race-pin-target")
		actions := []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}}
		pinnedOp, pinErr = r2.Pin(ctx, source.ID, target, actions)
	}()

	wg.Wait()

	verifySameSourcePinRaceOutcome(t, rMain, path, source.ID, rewriteRes, pinnedOp, rewriteErr, pinErr)
}

func verifySameSourcePinRaceOutcome(t *testing.T, r *Repository, path, sourceID string, rewriteRes RewriteResult, pinnedOp Operation, rewriteErr, pinErr error) {
	t.Helper()

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatalf("catalog corrupt after race: %v", err)
	}

	rewriteWon := rewriteErr == nil && errors.Is(pinErr, ErrUnavailable)
	pinWon := pinErr == nil && errors.Is(rewriteErr, ErrConflict)

	if !rewriteWon && !pinWon {
		t.Fatalf("unexpected race outcome: rewriteErr=%v pinErr=%v", rewriteErr, pinErr)
	}

	if rewriteWon {
		verifyRewriteWonOutcome(t, r, path, catalog, sourceID, rewriteRes)
		return
	}

	verifyPinWonOutcome(t, r, path, catalog, sourceID, pinnedOp)
}

func verifyRewriteWonOutcome(t *testing.T, r *Repository, path string, catalog Catalog, sourceID string, rewriteRes RewriteResult) {
	t.Helper()

	if catalog.generationIndex(sourceID) >= 0 {
		t.Fatal("source still present after rewrite won")
	}

	survivorManifest, err := r.Read(t.Context(), rewriteRes.Replacement.ID)
	if err != nil {
		t.Fatalf("reading survivor after rewrite won failed: %v", err)
	}

	if len(survivorManifest.Sessions) != 2 {
		t.Fatalf("unexpected survivor sessions count: %d", len(survivorManifest.Sessions))
	}

	art0 := readGzipFixture(t, filepath.Join(path, "generations", rewriteRes.Replacement.ID, "artifacts", "a0.bin.gz"))
	if !bytes.Equal(art0, []byte("shared window content\n")) {
		t.Fatal("survivor artifact a0 bytes corrupted")
	}
}

func verifyPinWonOutcome(t *testing.T, r *Repository, path string, catalog Catalog, sourceID string, pinnedOp Operation) {
	t.Helper()

	if catalog.generationIndex(sourceID) < 0 {
		t.Fatal("source removed after pin won")
	}

	collectResult, err := r.Collect(t.Context(), CollectionBudget{Generations: 10, Bytes: 10 << 20})
	if err != nil {
		t.Fatalf("collect failed during pin verification: %v", err)
	}

	if slices.Contains(collectResult.Removed, sourceID) {
		t.Fatal("pinned source was illegally collected")
	}

	pinnedManifest, err := r.Read(t.Context(), sourceID)
	if err != nil {
		t.Fatalf("reading pinned source after collect failed: %v", err)
	}

	if len(pinnedManifest.Sessions) != 3 {
		t.Fatalf("pinned manifest corrupted: %d sessions", len(pinnedManifest.Sessions))
	}

	art0 := readGzipFixture(t, filepath.Join(path, "generations", sourceID, "artifacts", "a0.bin.gz"))
	if !bytes.Equal(art0, []byte("shared window content\n")) {
		t.Fatal("pinned artifact a0 bytes corrupted")
	}

	if err := r.Finish(t.Context(), pinnedOp.ID, true); err != nil {
		t.Fatalf("finish pin failed: %v", err)
	}
}

func TestConcurrentRewriteAndDifferentSourcePinRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	rMain := openRepository(t, path)
	g1 := commitMultiSessionFixture(t, rMain)
	g2 := commitCompleteFixture(t, rMain)

	r1 := openRepository(t, path)
	r2 := openRepository(t, path)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	var (
		rewriteErr error
		pinErr     error
	)

	wg.Add(2)

	go func() {
		defer wg.Done()

		_, rewriteErr = r1.RemoveSessions(ctx, newID(), g1.ID, []string{"s0"})
	}()

	go func() {
		defer wg.Done()

		target := model.Bytes("different-source-target")
		actions := []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}}
		_, pinErr = r2.Pin(ctx, g2.ID, target, actions)
	}()

	wg.Wait()

	if rewriteErr != nil || pinErr != nil {
		t.Fatalf("both should succeed on different sources: rewriteErr=%v pinErr=%v", rewriteErr, pinErr)
	}

	catalog, err := rMain.Inspect(t.Context())
	if err != nil {
		t.Fatalf("catalog corrupt after race: %v", err)
	}

	if catalog.generationIndex(g1.ID) >= 0 {
		t.Fatal("g1 should be replaced after rewrite")
	}

	if catalog.generationIndex(g2.ID) < 0 {
		t.Fatal("g2 should remain present after pin")
	}
}

func TestConcurrentRewriteAndUnrelatedSaveRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	rMain := openRepository(t, path)
	g1 := commitMultiSessionFixture(t, rMain)

	r1 := openRepository(t, path)
	r2 := openRepository(t, path)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	var (
		rewriteRes RewriteResult
		saveGen    Generation
		rewriteErr error
		saveErr    error
	)

	wg.Add(2)

	go func() {
		defer wg.Done()

		rewriteRes, rewriteErr = r1.RemoveSessions(ctx, newID(), g1.ID, []string{"s0"})
	}()

	go func() {
		defer wg.Done()

		attempt, err := r2.Begin(ctx)
		if err != nil {
			saveErr = err
			return
		}

		m, artifacts := snapshotFixture()
		m.Source.Consistency = "checkpoint"

		for i := range m.Evidence {
			m.Evidence[i].Status = "verified"
			m.Evidence[i].Reason = ""
		}

		saveGen, saveErr = r2.Commit(ctx, attempt.ID, m, artifacts)
	}()

	wg.Wait()

	if rewriteErr != nil || saveErr != nil {
		t.Fatalf("both rewrite and unrelated save should succeed: rewriteErr=%v saveErr=%v", rewriteErr, saveErr)
	}

	catalog, err := rMain.Inspect(t.Context())
	if err != nil {
		t.Fatalf("catalog corrupt: %v", err)
	}

	if catalog.generationIndex(g1.ID) >= 0 {
		t.Fatal("g1 was not replaced")
	}

	if len(catalog.Generations) != 2 {
		t.Fatalf("expected 2 generations (g1 replacement + new save), got %d", len(catalog.Generations))
	}

	if catalog.Current != saveGen.ID {
		t.Fatalf("expected Current to be save %s for either race ordering, got %s", saveGen.ID, catalog.Current)
	}

	verifyUnrelatedSaveSourceBytes(t, rMain, path, saveGen, rewriteRes)
}

func verifyUnrelatedSaveSourceBytes(t *testing.T, r *Repository, path string, saveGen Generation, rewriteRes RewriteResult) {
	t.Helper()

	saveManifest, err := r.Read(t.Context(), saveGen.ID)
	if err != nil {
		t.Fatalf("reading save generation failed: %v", err)
	}

	if len(saveManifest.Sessions) != 1 || saveManifest.Sessions[0].ID != "s0" {
		t.Fatalf("unexpected save sessions: %v", saveManifest.Sessions)
	}

	saveArt := readGzipFixture(t, filepath.Join(path, "generations", saveGen.ID, "artifacts", "a0.bin.gz"))
	if !bytes.Equal(saveArt, []byte("  synthetic \x00\xff terminal history\n")) {
		t.Fatal("save artifact bytes corrupted")
	}

	replacementManifest, err := r.Read(t.Context(), rewriteRes.Replacement.ID)
	if err != nil {
		t.Fatalf("reading rewrite replacement generation failed: %v", err)
	}

	if len(replacementManifest.Sessions) != 2 {
		t.Fatalf("unexpected replacement sessions: %d", len(replacementManifest.Sessions))
	}

	repArt := readGzipFixture(t, filepath.Join(path, "generations", rewriteRes.Replacement.ID, "artifacts", "a0.bin.gz"))
	if !bytes.Equal(repArt, []byte("shared window content\n")) {
		t.Fatal("replacement artifact bytes corrupted")
	}
}

func TestConcurrentDuplicateSameIDRewriteRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	rMain := openRepository(t, path)
	source := commitMultiSessionFixture(t, rMain)

	r1 := openRepository(t, path)
	r2 := openRepository(t, path)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	mutationID := newID()

	var wg sync.WaitGroup

	var (
		res1, res2 RewriteResult
		err1, err2 error
	)

	wg.Add(2)

	go func() {
		defer wg.Done()

		res1, err1 = r1.RemoveSessions(ctx, mutationID, source.ID, []string{"s0"})
	}()

	go func() {
		defer wg.Done()

		res2, err2 = r2.RemoveSessions(ctx, mutationID, source.ID, []string{"s0"})
	}()

	wg.Wait()

	if err1 != nil || err2 != nil {
		t.Fatalf("both duplicate same-ID rewrites should succeed: err1=%v err2=%v", err1, err2)
	}

	if diff := cmp.Diff(res1, res2); diff != "" {
		t.Fatalf("racing same-ID rewrites returned differing results: %s", diff)
	}

	catalog, err := rMain.Inspect(t.Context())
	if err != nil {
		t.Fatalf("catalog corrupt: %v", err)
	}

	if len(catalog.Rewrites) != 1 {
		t.Fatalf("expected exactly 1 rewrite entry in catalog, got %d", len(catalog.Rewrites))
	}
}

func TestConcurrentSameIDConflictingRewriteRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	rMain := openRepository(t, path)
	source := commitMultiSessionFixture(t, rMain)

	r1 := openRepository(t, path)
	r2 := openRepository(t, path)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	mutationID := newID()

	var wg sync.WaitGroup

	var err1, err2 error

	wg.Add(2)

	go func() {
		defer wg.Done()

		_, err1 = r1.RemoveSessions(ctx, mutationID, source.ID, []string{"s0"})
	}()

	go func() {
		defer wg.Done()

		_, err2 = r2.RemoveSessions(ctx, mutationID, source.ID, []string{"s1"})
	}()

	wg.Wait()

	firstWon := err1 == nil && errors.Is(err2, ErrConflict)
	secondWon := err2 == nil && errors.Is(err1, ErrConflict)

	if !firstWon && !secondWon {
		t.Fatalf("expected exactly one winner and one ErrConflict: err1=%v err2=%v", err1, err2)
	}

	catalog, err := rMain.Inspect(t.Context())
	if err != nil {
		t.Fatalf("catalog corrupt: %v", err)
	}

	if len(catalog.Rewrites) != 1 {
		t.Fatalf("expected exactly 1 rewrite entry in catalog, got %d", len(catalog.Rewrites))
	}
}

func TestConcurrentRewriteAndRetentionRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	rMain := openRepository(t, path)

	gens := make([]Generation, 0, 6)
	for range 6 {
		gens = append(gens, commitCompleteFixture(t, rMain))
	}

	targetGen := gens[0]

	r1 := openRepository(t, path)
	r2 := openRepository(t, path)
	r3 := openRepository(t, path)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	var (
		rewriteErr error
		retainErr  error
		collectErr error
	)

	wg.Add(3)

	start := make(chan struct{})

	go func() {
		defer wg.Done()

		<-start

		_, rewriteErr = r1.RemoveSessions(ctx, newID(), targetGen.ID, []string{"s0"})
	}()

	go func() {
		defer wg.Done()

		<-start

		_, retainErr = r2.Retain(ctx, 5)
	}()

	go func() {
		defer wg.Done()

		<-start

		_, collectErr = r3.Collect(ctx, CollectionBudget{Generations: 10, Bytes: 1 << 30})
	}()

	close(start)
	wg.Wait()

	if err := errors.Join(retainErr, collectErr); err != nil {
		t.Fatalf("retention/collection failed: %v", err)
	}

	if rewriteErr != nil && !errors.Is(rewriteErr, ErrUnavailable) {
		t.Fatalf("unexpected rewrite error: %v", rewriteErr)
	}

	collectGenerations(t, rMain)
	assertGenerationRemoved(t, path, targetGen.ID)

	if _, err := rMain.Read(t.Context(), gens[len(gens)-1].ID); err != nil {
		t.Fatal("unrelated current source lost", err)
	}

	catalog, err := rMain.Inspect(t.Context())
	if err != nil {
		t.Fatalf("catalog corrupt: %v", err)
	}

	if len(catalog.Generations) > 5 {
		t.Fatalf("expected <= 5 generations retained, got %d", len(catalog.Generations))
	}
}
