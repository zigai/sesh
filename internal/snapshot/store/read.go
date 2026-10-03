//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"io"
	"slices"

	"github.com/samber/oops"

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

// ReadPracticalCurrent reads the generation used for practical recovery: everything ReadCurrent
// admits plus what [Catalog.PracticalCurrentAdmissible] allows.
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
	generation, manifest, err := r.currentLocked(ctx)
	if !errors.Is(err, ErrRecoveryRequired) {
		return generation, manifest, err
	}

	catalog, catalogErr := r.catalog()
	if catalogErr != nil {
		return Generation{}, model.Manifest{}, catalogErr
	}

	if !catalog.PracticalCurrentAdmissible() {
		return Generation{}, model.Manifest{}, ErrRecoveryRequired
	}

	current := catalog.Generations[catalog.generationIndex(catalog.Current)]
	manifest, err = r.readGeneration(ctx, current)

	return current, manifest, err
}

// PracticalCurrentAdmissible reports whether practical recovery may read the current generation:
// the latest save's observation-only result, or the newest saved generation when the latest save
// produced none because it failed or never finished. A pending rewrite of the current generation
// blocks either way, so a saved-session deletion in progress is never undone.
func (c Catalog) PracticalCurrentAdmissible() bool {
	if c.Current == "" || len(c.Attempts) == 0 {
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

	latest := c.latestAttempt()

	switch latest.State {
	case attemptStateIncomplete:
		return latest.ID == c.Current && !c.Generations[index].Complete
	case attemptStatePending, attemptStateFailed:
		return true
	default:
		return false
	}
}

// LatestSaveUnfinished reports whether the latest save attempt failed or never finished, so the
// current generation, if any, predates it.
func (c Catalog) LatestSaveUnfinished() bool {
	if len(c.Attempts) == 0 {
		return false
	}

	state := c.latestAttempt().State

	return state == attemptStatePending || state == attemptStateFailed
}

func (c Catalog) latestAttempt() Attempt {
	latest := c.Attempts[0]
	for _, attempt := range c.Attempts[1:] {
		if attempt.Sequence > latest.Sequence {
			latest = attempt
		}
	}

	return latest
}

func (r *Repository) currentLocked(ctx context.Context) (Generation, model.Manifest, error) {
	catalog, err := r.catalog()
	if err != nil {
		return Generation{}, model.Manifest{}, err
	}

	for _, rewrite := range catalog.Rewrites {
		if rewrite.State == rewriteStatePending && rewrite.Source == catalog.Current {
			return Generation{}, model.Manifest{}, ErrRecoveryRequired
		}
	}

	if len(catalog.Attempts) == 0 {
		return Generation{}, model.Manifest{}, ErrUnavailable
	}

	latest := catalog.latestSaveAttempt()
	if catalog.Current == "" && collectionRetiredState(latest.State) {
		return Generation{}, model.Manifest{}, ErrUnavailable
	}

	if latest.State != "committed" {
		return Generation{}, model.Manifest{}, ErrRecoveryRequired
	}

	index := catalog.generationIndex(catalog.Current)
	if index < 0 {
		return Generation{}, model.Manifest{}, ErrCorrupt
	}

	if !catalog.Generations[index].Complete {
		return Generation{}, model.Manifest{}, ErrRecoveryRequired
	}

	generation := catalog.Generations[index]
	manifest, err := r.readGeneration(ctx, generation)

	return generation, manifest, err
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
	parent, err := r.root.Child("generations", false)
	if err != nil {
		return model.Manifest{}, oops.In("snapshot.store").With("operation", "open_generations").Wrapf(err, "open generations")
	}

	dir, openErr := parent.Child(generation.ID, false)
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
	for _, descriptor := range descriptors {
		if err := verifyArtifact(ctx, dir, descriptor, io.Discard); err != nil {
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
