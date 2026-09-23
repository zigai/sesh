//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestResumeRewriteRacesSavePinsRetentionAndCollection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitMultiSessionFixture(t, r)
	id, _ := registerPendingRewriteFixture(t, r, source.ID, []string{"s0"})
	pinned := commitMultiSessionFixture(t, r)

	actions := []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}}
	if _, err := r.Pin(t.Context(), pinned.ID, model.Bytes("protected-fixture"), actions); err != nil {
		t.Fatal(err)
	}

	for range 5 {
		commitMultiSessionFixture(t, r)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	resumeRepo, saveRepo := openRepository(t, path), openRepository(t, path)
	pinRepo, retainRepo, collectRepo := openRepository(t, path), openRepository(t, path), openRepository(t, path)

	attempt, err := saveRepo.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}

	m, artifacts := multiSessionSnapshotFixture()

	var (
		resumed RewriteResult
		saved   Generation
		wg      sync.WaitGroup
	)

	errs := make([]error, 5)
	start := make(chan struct{})

	wg.Go(func() { <-start; resumed, errs[0] = resumeRepo.ResumeRewrite(ctx, id) })
	wg.Go(func() { <-start; saved, errs[1] = saveRepo.Commit(ctx, attempt.ID, m, artifacts) })
	wg.Go(func() { <-start; _, errs[2] = pinRepo.Pin(ctx, pinned.ID, model.Bytes("racing-fixture"), actions) })
	wg.Go(func() { <-start; _, errs[3] = retainRepo.RetainPolicy(ctx, time.Time{}, RetentionPolicy{MaxCount: 5}) })
	wg.Go(func() {
		<-start

		_, errs[4] = collectRepo.Collect(ctx, CollectionBudget{Generations: 20, Bytes: 10 << 20})
	})
	close(start)
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}

	catalog, err := r.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if catalog.Current != saved.ID || catalog.generationIndex(source.ID) >= 0 || catalog.generationIndex(pinned.ID) < 0 {
		t.Fatalf("lost save order, rewrite, or pinned source: %+v", catalog)
	}

	for _, gen := range []string{resumed.Replacement.ID, pinned.ID, saved.ID} {
		verifySurvivorFixtureBytes(t, r, gen, map[string]string{"a0": "shared window content\n", "a1": "s2 exclusive content\n"})
	}
}
