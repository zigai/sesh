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

func repeatedCollection(ctx context.Context, r *Repository) error {
	for range 4 {
		if _, err := r.Collect(ctx, CollectionBudget{Generations: 8, Bytes: 1 << 30}); err != nil {
			return err
		}
	}

	return nil
}

func repeatedReader(ctx context.Context, r *Repository, id string) error {
	for range 10 {
		if _, err := r.Read(ctx, id); err != nil {
			return err
		}

		if _, err := r.Current(ctx); err != nil {
			return err
		}
	}

	return nil
}

func singleRetention(ctx context.Context, r *Repository) error {
	_, err := r.RetainPolicy(ctx, time.Time{}, RetentionPolicy{MaxAge: 0, MaxCount: minimumRetained, MaxBytes: 0})

	return err
}

func TestCollectionConcurrentWithReadsPinsAndRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	other := openRepository(t, path)

	generations := make([]Generation, 0, 8)
	for range 8 {
		generations = append(generations, commitCompleteFixture(t, r))
	}

	current := generations[len(generations)-1]

	op, err := r.Pin(t.Context(), generations[0].ID, model.Bytes("synthetic-target"), []Action{{ID: actionLaunch, Kind: actionLaunch, State: actionPlanned}})
	if err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	failures := make(chan error, 3)

	workers.Go(func() { failures <- repeatedCollection(ctx, other) })
	workers.Go(func() { failures <- repeatedReader(ctx, r, current.ID) })
	workers.Go(func() { failures <- singleRetention(ctx, r) })

	workers.Wait()
	close(failures)

	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, generation := range generations[len(generations)-minimumRetained:] {
		if _, err := r.Read(t.Context(), generation.ID); err != nil {
			t.Fatal("protected generation lost", err)
		}
	}

	if _, err := r.Read(t.Context(), op.Source); err != nil {
		t.Fatal("pinned source lost", err)
	}

	if _, err := r.Current(t.Context()); err != nil {
		t.Fatal("current selection lost", err)
	}
}

func TestCollectionAndRetentionRaceWithNewPin(t *testing.T) {
	for range 5 {
		testCollectionPinRace(t)
	}
}

func testCollectionPinRace(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	retention := openRepository(t, path)
	collector := openRepository(t, path)

	source := commitCompleteFixture(t, r)
	for range 5 {
		commitCompleteFixture(t, r)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var (
		pinErr, retentionErr, collectionErr error
		workers                             sync.WaitGroup
	)

	start := make(chan struct{})

	workers.Go(func() {
		<-start

		_, pinErr = r.Pin(ctx, source.ID, model.Bytes("synthetic-target"), nil)
	})
	workers.Go(func() {
		<-start

		_, retentionErr = retention.Retain(ctx, 5)
	})
	workers.Go(func() {
		<-start

		collectionErr = repeatedCollection(ctx, collector)
	})
	close(start)
	workers.Wait()

	if err := errors.Join(retentionErr, collectionErr); err != nil {
		t.Fatal(err)
	}

	collectGenerations(t, collector)

	if pinErr == nil {
		if _, err := r.Read(t.Context(), source.ID); err != nil {
			t.Fatal("successful concurrent pin lost its physical recovery source", err)
		}

		return
	}

	if !errors.Is(pinErr, ErrUnavailable) {
		t.Fatal("unexpected pin outcome", pinErr)
	}

	assertGenerationRemoved(t, path, source.ID)
}
