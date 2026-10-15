//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestResolveCurrentSnapshotPreservesCanceledPracticalGenerationRead(t *testing.T) {
	t.Parallel()

	const generationID = "practical-generation"

	reader := &snapshotReaderStub{
		catalog: store.Catalog{
			Version:  model.Schema,
			Sequence: 1,
			Current:  generationID,
			Attempts: []store.Attempt{
				{
					ID:             generationID,
					Sequence:       1,
					State:          "incomplete",
					Reason:         "parity_incomplete",
					ManifestSHA256: "",
				},
			},
			Generations: []store.Generation{
				{
					ID:             generationID,
					Sequence:       1,
					ManifestSHA256: "",
					Complete:       false,
				},
			},
			Operations: []store.Operation{},
			Rewrites:   []store.Rewrite{},
		},
		currentErr: store.ErrRecoveryRequired,
		readErr:    context.Canceled,
	}

	result, err := NewService(Options{}).resolveCurrentSnapshot(t.Context(), reader)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}

	if result.ID != "" {
		t.Fatalf("result = %#v, want empty result for canceled read", result)
	}
}

func TestInspectSnapshotPreservesCancellation(t *testing.T) {
	t.Parallel()

	reader := &snapshotReaderStub{
		inspectErr: context.Canceled,
	}

	repoRes, curRes, err := NewService(Options{}).inspectSnapshot(t.Context(), reader)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}

	if repoRes.ID != "" || curRes.ID != "" {
		t.Fatalf("results = (%#v, %#v), want zero results for canceled inspection", repoRes, curRes)
	}
}

func TestInspectSnapshotClassifiesCorruptCatalog(t *testing.T) {
	t.Parallel()

	reader := &snapshotReaderStub{
		inspectErr: store.ErrCorrupt,
	}

	repoRes, curRes, err := NewService(Options{}).inspectSnapshot(t.Context(), reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repoRes.Status != StatusFail || repoRes.Reason != ReasonCorrupt || repoRes.Fix == nil {
		t.Fatalf("repoRes = %+v, want a corrupt failure with a fix", repoRes)
	}

	if curRes.Status != StatusSkip || curRes.Needs != CheckSnapshotRepository {
		t.Fatalf("curRes = %+v, want a skip that needs %s", curRes, CheckSnapshotRepository)
	}
}

type snapshotReaderStub struct {
	catalog    store.Catalog
	inspectErr error
	currentErr error
	readErr    error
}

func (s *snapshotReaderStub) Inspect(context.Context) (store.Catalog, error) {
	return s.catalog, s.inspectErr
}

func (s *snapshotReaderStub) Read(context.Context, string) (model.Manifest, error) {
	return model.Manifest{}, s.readErr
}

func (s *snapshotReaderStub) ReadCurrent(context.Context) (store.Generation, model.Manifest, error) {
	return store.Generation{}, model.Manifest{}, s.currentErr
}
