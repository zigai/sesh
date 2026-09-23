//go:build linux || darwin

package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestSaveLeaseCoversCaptureWithoutBlockingOtherEndpoints(t *testing.T) {
	first, contended, independent := testRepository(t), testRepository(t), testRepository(t)
	leaseRoot, endpoint := testLeaseEndpoint(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})

	var (
		firstResult SaveResult
		firstErr    error
	)

	go func() {
		defer close(finished)

		firstResult, firstErr = save(ctx, first, leaseRoot, endpoint, func(ctx context.Context) (capture.Observation, error) {
			close(entered)

			select {
			case <-release:
				return observationFixture(), nil
			case <-ctx.Done():
				return capture.Observation{}, ctx.Err()
			}
		})
	}()

	defer func() { cancel(); <-finished }()

	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first save never entered capture")
	}

	assertSaveBlockedBeforeRegistration(ctx, t, contended, leaseRoot, endpoint)

	other, err := save(ctx, independent, leaseRoot, filepath.Join(t.TempDir(), "other.sock"), func(context.Context) (capture.Observation, error) {
		return observationFixture(), nil
	})
	if err != nil || other.Status != "partial" {
		t.Fatalf("independent endpoint blocked behind first save: %#v, %v", other, err)
	}

	close(release)
	<-finished

	if firstErr != nil || firstResult.Status != "partial" {
		t.Fatalf("first save did not finish: %#v, %v", firstResult, firstErr)
	}
}

func assertSaveBlockedBeforeRegistration(ctx context.Context, t *testing.T, contended *store.Repository, leaseRoot, endpoint string) {
	t.Helper()

	blockedCtx, blockedCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer blockedCancel()

	called := false

	blocked, err := save(blockedCtx, contended, leaseRoot, endpoint, func(context.Context) (capture.Observation, error) {
		called = true
		return observationFixture(), nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || called || blocked.Attempt != "" {
		t.Fatalf("contended save reached registration or capture: %#v, %v", blocked, err)
	}

	catalog, err := contended.Inspect(ctx)
	if err != nil || len(catalog.Attempts) != 0 {
		t.Fatalf("contended save changed catalog: %#v, %v", catalog, err)
	}
}
