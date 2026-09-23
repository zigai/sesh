//go:build linux || darwin

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func (r *Repository) Commit(ctx context.Context, id string, manifest model.Manifest, artifacts map[string]io.Reader) (Generation, error) {
	data, err := model.Encode(manifest)
	if err != nil {
		return Generation{}, repositoryError("commit", oops.In("snapshot.store").With("operation", "encode_snapshot").Wrapf(err, "encode snapshot"))
	}

	if len(artifacts) != len(manifest.Artifacts) {
		return Generation{}, repositoryError("commit", ErrCorrupt)
	}

	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return Generation{}, repositoryError("commit", oops.In("snapshot.store").With("operation", "lock_snapshot_commit").Wrapf(err, "lock snapshot commit"))
	}

	generation, commitErr := r.commitLocked(ctx, id, manifest, data, artifacts)

	return generation, repositoryError("commit", errors.Join(commitErr, lock.Close()))
}

func (r *Repository) commitLocked(ctx context.Context, id string, manifest model.Manifest, data []byte, artifacts map[string]io.Reader) (Generation, error) {
	catalog, err := r.catalog()
	if err != nil {
		return Generation{}, err
	}

	index := catalog.attemptIndex(id)
	if index < 0 {
		return Generation{}, ErrUnavailable
	}

	generation := Generation{ID: id, Sequence: catalog.Attempts[index].Sequence, ManifestSHA256: digest(data), Complete: manifest.Complete()}
	if existing := catalog.generationIndex(id); existing >= 0 {
		if catalog.Generations[existing] != generation {
			return Generation{}, ErrConflict
		}

		return r.confirmCommit(ctx, generation)
	}

	if catalog.Attempts[index].State != "pending" {
		return Generation{}, ErrConflict
	}

	if err := r.stage(ctx, id, manifest, data, artifacts); err != nil {
		return Generation{}, err
	}

	if err := ctx.Err(); err != nil {
		return Generation{}, oops.In("snapshot.store").With("operation", "commit_snapshot").Wrapf(err, "commit snapshot")
	}

	catalog.Generations = append(catalog.Generations, generation)
	catalog.Attempts[index].ManifestSHA256 = generation.ManifestSHA256

	catalog.Attempts[index].State, catalog.Attempts[index].Reason = "incomplete", "parity_incomplete"
	if generation.Complete {
		catalog.Attempts[index].State, catalog.Attempts[index].Reason = "committed", ""
	}

	if generation.Sequence == catalog.latestPublishedSaveSequence() {
		catalog.Current = id
	}

	if err := r.publish(catalog); err != nil {
		return generation, err
	}

	return generation, nil
}

func (r *Repository) stage(ctx context.Context, id string, manifest model.Manifest, data []byte, artifacts map[string]io.Reader) error {
	staging, err := r.root.Child("staging", false)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_staging_directory").Wrapf(err, "open staging directory")
	}

	err = r.stageInto(ctx, staging, id, manifest, data, artifacts)

	return errors.Join(err, staging.Close())
}

func (r *Repository) stageInto(ctx context.Context, staging *privatefs.Dir, id string, manifest model.Manifest, data []byte, artifacts map[string]io.Reader) error {
	dir, err := staging.CreateDir(id)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "create_generation_staging").Wrapf(err, "create generation staging")
	}

	stageErr := r.writeGeneration(ctx, dir, manifest, data, artifacts)
	if err := errors.Join(stageErr, dir.Close()); err != nil {
		return err
	}

	if err := r.boundary("stage_synced"); err != nil {
		return err
	}

	generations, err := r.root.Child("generations", false)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_generations").Wrapf(err, "open generations")
	}

	renameErr := staging.Rename(id, generations, id)
	if err := errors.Join(renameErr, generations.Sync(), staging.Sync(), generations.Close()); err != nil {
		return err
	}

	return r.boundary("generation_published")
}

func (r *Repository) writeGeneration(ctx context.Context, dir *privatefs.Dir, manifest model.Manifest, data []byte, artifacts map[string]io.Reader) error {
	artifactDir, err := dir.CreateDir("artifacts")
	if err != nil {
		return oops.In("snapshot.store").With("operation", "create_artifact_directory").Wrapf(err, "create artifact directory")
	}

	writeErr := writeArtifacts(ctx, artifactDir, manifest.Artifacts, artifacts)
	if err := errors.Join(writeErr, artifactDir.Sync(), artifactDir.Close()); err != nil {
		return err
	}

	if err := writeFile(dir, "manifest.json", data); err != nil {
		return err
	}

	if err := dir.Sync(); err != nil {
		return oops.In("snapshot.store").With("operation", "sync_generation").Wrapf(err, "sync generation")
	}

	_, err = readManifest(ctx, dir, digest(data))

	return err
}

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func (r *Repository) confirmCommit(ctx context.Context, generation Generation) (Generation, error) {
	_, err := r.readGeneration(ctx, generation)
	if err != nil {
		return generation, err
	}

	if err := r.root.Sync(); err != nil {
		return generation, errors.Join(ErrUncertain, err)
	}

	return generation, nil
}

func (c Catalog) latestPublishedSaveSequence() uint64 {
	replacements := c.rewriteReplacements()
	for _, attempt := range slices.Backward(c.Attempts) {
		if !replacements[attempt.ID] && collectionRetiredState(attempt.State) {
			return attempt.Sequence
		}
	}

	return 0
}
