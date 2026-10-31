//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"io"
	"runtime"
	"slices"

	"github.com/samber/oops"
	"golang.org/x/sync/errgroup"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func (r *Repository) Read(ctx context.Context, id string) (model.Manifest, error) {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return model.Manifest{}, repositoryError("read", oops.In("snapshot.store").With("operation", "lock_snapshot_read").Wrapf(err, "lock snapshot read"))
	}

	manifest, readErr := r.readLocked(ctx, id)

	return manifest, repositoryError("read", errors.Join(readErr, lock.Close()))
}

func (r *Repository) Current(ctx context.Context) (model.Manifest, error) {
	_, manifest, err := r.ReadCurrent(ctx)
	return manifest, err
}

func (r *Repository) ReadCurrent(ctx context.Context) (Generation, model.Manifest, error) {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return Generation{}, model.Manifest{}, repositoryError("current", oops.In("snapshot.store").With("operation", "lock_snapshot_read").Wrapf(err, "lock snapshot read"))
	}

	generation, manifest, readErr := r.currentLocked(ctx)
	if err := errors.Join(readErr, lock.Close()); err != nil {
		return Generation{}, model.Manifest{}, repositoryError("current", err)
	}

	return generation, manifest, nil
}

func (r *Repository) ReadPracticalCurrent(ctx context.Context) (Generation, model.Manifest, error) {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return Generation{}, model.Manifest{}, repositoryError("current", oops.In("snapshot.store").With("operation", "lock_snapshot_read").Wrapf(err, "lock snapshot read"))
	}

	generation, manifest, readErr := r.practicalCurrentLocked(ctx)
	if err := errors.Join(readErr, lock.Close()); err != nil {
		return Generation{}, model.Manifest{}, repositoryError("current", err)
	}

	return generation, manifest, nil
}

func (r *Repository) practicalCurrentLocked(ctx context.Context) (Generation, model.Manifest, error) {
	catalog, err := r.catalog()
	if err != nil {
		return Generation{}, model.Manifest{}, err
	}

	generation, err := catalog.practicalCurrentGeneration()
	if err != nil {
		return Generation{}, model.Manifest{}, err
	}

	manifest, err := r.readGeneration(ctx, generation)

	return generation, manifest, err
}

func (c Catalog) practicalCurrentGeneration() (Generation, error) {
	generation, err := c.currentGeneration()
	if !errors.Is(err, ErrRecoveryRequired) {
		return generation, err
	}

	if !c.PracticalCurrentAdmissible() {
		return Generation{}, ErrRecoveryRequired
	}

	return c.Generations[c.generationIndex(c.Current)], nil
}

// PracticalCurrentAdmissible admits the latest save's observation-only result, or the newest
// generation when the latest save failed or never finished; a pending rewrite always blocks.
func (c Catalog) PracticalCurrentAdmissible() bool {
	if c.Current == "" {
		return false
	}

	for _, rewrite := range c.Rewrites {
		if rewrite.State == rewriteStatePending && rewrite.Source == c.Current {
			return false
		}
	}

	index := c.generationIndex(c.Current)
	if index < 0 {
		return false
	}

	latest := c.latestSaveAttempt()

	switch latest.State {
	case attemptStateIncomplete:
		return c.rewriteDescendant(latest.ID) == c.Current && !c.Generations[index].Complete
	case attemptStatePending, attemptStateFailed:
		return true
	default:
		return false
	}
}

func (c Catalog) LatestSaveUnfinished() bool {
	state := c.latestSaveAttempt().State

	return state == attemptStatePending || state == attemptStateFailed
}

func (c Catalog) LatestSaveFailed() bool {
	return c.latestSaveAttempt().State == attemptStateFailed
}

func (c Catalog) rewriteDescendant(source string) string {
	for range len(c.Rewrites) {
		index := slices.IndexFunc(c.Rewrites, func(rewrite Rewrite) bool {
			return rewrite.State == rewriteStateCompleted && rewrite.Source == source && rewrite.Replacement != ""
		})
		if index < 0 {
			return source
		}

		source = c.Rewrites[index].Replacement
	}

	return source
}

func (r *Repository) currentLocked(ctx context.Context) (Generation, model.Manifest, error) {
	catalog, err := r.catalog()
	if err != nil {
		return Generation{}, model.Manifest{}, err
	}

	generation, err := catalog.currentGeneration()
	if err != nil {
		return Generation{}, model.Manifest{}, err
	}

	manifest, err := r.readGeneration(ctx, generation)

	return generation, manifest, err
}

func (c Catalog) currentGeneration() (Generation, error) {
	for _, rewrite := range c.Rewrites {
		if rewrite.State == rewriteStatePending && rewrite.Source == c.Current {
			return Generation{}, ErrRecoveryRequired
		}
	}

	latest := c.latestSaveAttempt()
	if c.Current == "" && latest.State != attemptStatePending {
		return Generation{}, ErrUnavailable
	}

	if latest.State != "committed" {
		return Generation{}, ErrRecoveryRequired
	}

	index := c.generationIndex(c.Current)
	if index < 0 {
		return Generation{}, ErrCorrupt
	}

	if !c.Generations[index].Complete {
		return Generation{}, ErrRecoveryRequired
	}

	return c.Generations[index], nil
}

func (r *Repository) readLocked(ctx context.Context, id string) (model.Manifest, error) {
	catalog, err := r.catalog()
	if err != nil {
		return model.Manifest{}, err
	}

	index := catalog.generationIndex(id)
	if index < 0 {
		return model.Manifest{}, ErrUnavailable
	}

	return r.readGeneration(ctx, catalog.Generations[index])
}

func (r *Repository) readGeneration(ctx context.Context, generation Generation) (model.Manifest, error) {
	manifest, err := r.readGenerationFiles(ctx, generation)
	if err != nil {
		return manifest, oops.In("snapshot.store").With("operation", "read_generation", "generation", generation.ID).Wrapf(err, "read generation")
	}

	return manifest, nil
}

func (r *Repository) readGenerationFiles(ctx context.Context, generation Generation) (model.Manifest, error) {
	parent, err := r.root.Child("generations", false)
	if err != nil {
		return model.Manifest{}, oops.In("snapshot.store").With("operation", "open_generations").Wrapf(err, "open generations")
	}

	dir, openErr := parent.Child(generation.ID, false)
	if openErr != nil {
		openErr = oops.In("snapshot.store").With("operation", "open_generation").Wrapf(openErr, "open generation")
	}

	if err := errors.Join(openErr, parent.Close()); err != nil {
		if dir != nil {
			err = errors.Join(err, dir.Close())
		}

		return model.Manifest{}, err
	}

	manifest, readErr := readManifest(ctx, dir, generation.ManifestSHA256)
	if readErr == nil && manifest.Complete() != generation.Complete {
		readErr = ErrCorrupt
	}

	return manifest, errors.Join(readErr, dir.Close())
}

func readManifest(ctx context.Context, dir *privatefs.Dir, expected string) (model.Manifest, error) {
	data, err := dir.Read("manifest.json", model.MaxManifestBytes)
	if err != nil {
		return model.Manifest{}, oops.In("snapshot.store").With("operation", "read_manifest").Wrapf(err, "read manifest")
	}

	if digest(data) != expected {
		return model.Manifest{}, ErrCorrupt
	}

	manifest, err := model.Decode(data)
	if err != nil {
		return model.Manifest{}, oops.In("snapshot.store").With("operation", "decode_manifest").Wrapf(err, "decode manifest")
	}

	artifacts, err := dir.Child("artifacts", false)
	if err != nil {
		return model.Manifest{}, oops.In("snapshot.store").With("operation", "open_artifacts").Wrapf(err, "open artifacts")
	}

	verifyErr := verifyArtifacts(ctx, artifacts, manifest.Artifacts)

	return manifest, errors.Join(verifyErr, artifacts.Close())
}

func verifyArtifacts(ctx context.Context, dir *privatefs.Dir, descriptors []model.Artifact) error {
	errs := make([]error, len(descriptors))

	var verifiers errgroup.Group
	verifiers.SetLimit(runtime.GOMAXPROCS(0))

	for index, descriptor := range descriptors {
		verifiers.Go(func() error {
			errs[index] = verifyArtifact(ctx, dir, descriptor, io.Discard)

			return nil
		})
	}

	_ = verifiers.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func (c Catalog) latestSaveAttempt() Attempt {
	replacements := c.rewriteReplacements()

	for _, attempt := range slices.Backward(c.Attempts) {
		if !replacements[attempt.ID] {
			return attempt
		}
	}

	var absent Attempt

	return absent
}

func (c Catalog) rewriteReplacements() map[string]bool {
	replacements := make(map[string]bool, len(c.Rewrites))
	for _, rewrite := range c.Rewrites {
		if rewrite.Replacement != "" {
			replacements[rewrite.Replacement] = true
		}
	}

	return replacements
}
