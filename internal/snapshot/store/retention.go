//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"io"
	"math"
	"slices"
	"time"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

var errByteAccountingOverflow = errors.New("byte accounting integer overflow")

type Usage struct {
	TotalBytes int64

	ProtectedBytes int64

	GenerationCount int

	GenerationBytes map[string]int64
}

func (r *Repository) Retain(ctx context.Context, count int) ([]string, error) {
	if count < minimumRetained {
		return nil, repositoryError("retain", ErrConflict)
	}

	result, err := r.RetainPolicy(ctx, time.Time{}, RetentionPolicy{MaxAge: 0, MaxCount: count, MaxBytes: 0})
	if err != nil {
		return nil, err
	}

	return result.Removed, nil
}

func (r *Repository) RetainBytes(ctx context.Context, maxBytes int64) (RetentionResult, error) {
	return r.RetainPolicy(ctx, time.Time{}, RetentionPolicy{MaxAge: 0, MaxCount: 0, MaxBytes: maxBytes})
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	err := r.update(ctx, func(c *Catalog) error {
		if c.generationIndex(id) < 0 {
			return ErrUnavailable
		}

		if c.protected()[id] {
			return ErrConflict
		}

		c.Generations = slices.DeleteFunc(c.Generations, func(g Generation) bool { return g.ID == id })

		return nil
	})

	return repositoryError("delete", err)
}

func (c Catalog) protected() map[string]bool {
	protected := map[string]bool{c.Current: true}
	for _, operation := range c.Operations {
		if operation.State == "active" {
			protected[operation.Source] = true
		}
	}

	for _, rewrite := range c.Rewrites {
		if rewrite.State == rewriteStatePending {
			protected[rewrite.Source] = true
			if rewrite.Replacement != "" {
				protected[rewrite.Replacement] = true
			}
		}
	}

	return protected
}

func (r *Repository) loadRetentionBytes(ctx context.Context, gens []Generation) (map[string]int64, map[string]int64, error) {
	sizes := make(map[string]int64, len(gens))
	timestamps := make(map[string]int64, len(gens))

	for _, gen := range gens {
		if err := ctx.Err(); err != nil {
			return nil, nil, oops.In("snapshot.store").With("operation", "load_retention_bytes").Wrap(err)
		}

		size, finished, err := r.generationUsage(ctx, gen)
		if err != nil {
			return nil, nil, err
		}

		sizes[gen.ID] = size
		timestamps[gen.ID] = finished
	}

	return sizes, timestamps, nil
}

func (r *Repository) Usage(ctx context.Context) (Usage, error) {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return Usage{}, repositoryError("usage", oops.In("snapshot.store").With("operation", "lock_repository_usage").Wrapf(err, "lock repository usage"))
	}

	usage, usageErr := r.usageLocked(ctx)
	closeErr := lock.Close()

	if err := errors.Join(usageErr, closeErr); err != nil {
		return Usage{}, repositoryError("usage", err)
	}

	return usage, nil
}

func (r *Repository) GenerationUsage(ctx context.Context, id string) (int64, error) {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return 0, repositoryError("usage", oops.In("snapshot.store").With("operation", "lock_repository_usage").Wrapf(err, "lock repository usage"))
	}

	size, usageErr := r.generationUsageByIDLocked(ctx, id)
	closeErr := lock.Close()

	if err := errors.Join(usageErr, closeErr); err != nil {
		return 0, repositoryError("usage", err)
	}

	return size, nil
}

func (r *Repository) usageLocked(ctx context.Context) (Usage, error) {
	if err := ctx.Err(); err != nil {
		return Usage{}, oops.In("snapshot.store").With("operation", "calculate_usage").Wrap(err)
	}

	catalog, err := r.catalog()
	if err != nil {
		return Usage{}, err
	}

	if err := ctx.Err(); err != nil {
		return Usage{}, oops.In("snapshot.store").With("operation", "calculate_usage").Wrap(err)
	}

	if len(catalog.Generations) == 0 {
		return Usage{
			TotalBytes:      0,
			ProtectedBytes:  0,
			GenerationCount: 0,
			GenerationBytes: make(map[string]int64),
		}, nil
	}

	_, protectedMap := catalog.orderedWithProtection()

	genBytes := make(map[string]int64, len(catalog.Generations))

	var (
		totalBytes     int64
		protectedBytes int64
	)

	for _, gen := range catalog.Generations {
		if err := ctx.Err(); err != nil {
			return Usage{}, oops.In("snapshot.store").With("operation", "calculate_usage").Wrap(err)
		}

		size, _, err := r.generationUsage(ctx, gen)
		if err != nil {
			return Usage{}, err
		}

		genBytes[gen.ID] = size

		var addErr error

		totalBytes, addErr = safeAdd(totalBytes, size)
		if addErr != nil {
			return Usage{}, addErr
		}

		if protectedMap[gen.ID] {
			protectedBytes, addErr = safeAdd(protectedBytes, size)
			if addErr != nil {
				return Usage{}, addErr
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return Usage{}, oops.In("snapshot.store").With("operation", "calculate_usage").Wrap(err)
	}

	return Usage{
		TotalBytes:      totalBytes,
		ProtectedBytes:  protectedBytes,
		GenerationCount: len(catalog.Generations),
		GenerationBytes: genBytes,
	}, nil
}

func (r *Repository) generationUsageByIDLocked(ctx context.Context, id string) (int64, error) {
	catalog, err := r.catalog()
	if err != nil {
		return 0, err
	}

	index := catalog.generationIndex(id)
	if index < 0 {
		return 0, ErrUnavailable
	}

	size, _, err := r.generationUsage(ctx, catalog.Generations[index])

	return size, err
}

func (r *Repository) generationUsage(ctx context.Context, gen Generation) (int64, int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, oops.In("snapshot.store").With("operation", "generation_usage").Wrap(err)
	}

	parent, err := r.root.Child("generations", false)
	if err != nil {
		return 0, 0, errors.Join(ErrCorrupt, err)
	}

	dir, openErr := parent.Child(gen.ID, false)
	closeParentErr := parent.Close()

	if openErr != nil {
		return 0, 0, errors.Join(ErrCorrupt, openErr, closeParentErr)
	}

	manifest, manifestSize, readErr := readValidatedManifest(ctx, dir, gen)
	if readErr != nil {
		closeDirErr := dir.Close()
		return 0, 0, errors.Join(readErr, closeDirErr, closeParentErr)
	}

	artifactBytes, artErr := readValidatedArtifacts(ctx, dir, manifest.Artifacts)
	closeDirErr := dir.Close()

	if err := errors.Join(artErr, closeDirErr, closeParentErr); err != nil {
		return 0, 0, err
	}

	if err := ctx.Err(); err != nil {
		return 0, 0, oops.In("snapshot.store").With("operation", "generation_usage").Wrap(err)
	}

	totalGenBytes, addErr := safeAdd(manifestSize, artifactBytes)
	if addErr != nil {
		return 0, 0, addErr
	}

	return totalGenBytes, manifest.Finished, nil
}

func readValidatedManifest(ctx context.Context, dir *privatefs.Dir, gen Generation) (model.Manifest, int64, error) {
	if err := ctx.Err(); err != nil {
		return model.Manifest{}, 0, oops.In("snapshot.store").With("operation", "read_manifest").Wrap(err)
	}

	manifestFile, fileErr := dir.File("manifest.json", unix.O_RDONLY)
	if fileErr != nil {
		return model.Manifest{}, 0, errors.Join(ErrCorrupt, fileErr)
	}

	stat, statErr := manifestFile.Stat()
	if statErr != nil {
		closeFileErr := manifestFile.Close()
		return model.Manifest{}, 0, errors.Join(ErrCorrupt, statErr, closeFileErr)
	}

	manifestSize := stat.Size()
	if manifestSize < 0 || manifestSize > model.MaxManifestBytes {
		closeFileErr := manifestFile.Close()
		return model.Manifest{}, 0, errors.Join(ErrCorrupt, closeFileErr)
	}

	if err := ctx.Err(); err != nil {
		closeFileErr := manifestFile.Close()
		return model.Manifest{}, 0, errors.Join(err, closeFileErr)
	}

	data, readErr := io.ReadAll(io.LimitReader(manifestFile, model.MaxManifestBytes+1))
	closeFileErr := manifestFile.Close()

	if err := errors.Join(readErr, closeFileErr); err != nil {
		return model.Manifest{}, 0, errors.Join(ErrCorrupt, err)
	}

	if int64(len(data)) != manifestSize || digest(data) != gen.ManifestSHA256 {
		return model.Manifest{}, 0, ErrCorrupt
	}

	manifest, decodeErr := model.Decode(data)
	if decodeErr != nil {
		return model.Manifest{}, 0, errors.Join(ErrCorrupt, decodeErr)
	}

	if err := validateManifestMetadata(manifest, gen); err != nil {
		return model.Manifest{}, 0, err
	}

	return manifest, manifestSize, nil
}

func readValidatedArtifacts(ctx context.Context, dir *privatefs.Dir, descriptors []model.Artifact) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, oops.In("snapshot.store").With("operation", "read_artifacts").Wrap(err)
	}

	artifactsDir, childErr := dir.Child("artifacts", false)
	if childErr != nil {
		return 0, errors.Join(ErrCorrupt, childErr)
	}

	var totalBytes int64

	for _, descriptor := range descriptors {
		if err := ctx.Err(); err != nil {
			closeArtifactsErr := artifactsDir.Close()
			return 0, errors.Join(err, closeArtifactsErr)
		}

		size, err := readValidatedArtifact(ctx, artifactsDir, descriptor)
		if err != nil {
			closeArtifactsErr := artifactsDir.Close()
			return 0, errors.Join(err, closeArtifactsErr)
		}

		var addErr error

		totalBytes, addErr = safeAdd(totalBytes, size)
		if addErr != nil {
			closeArtifactsErr := artifactsDir.Close()
			return 0, errors.Join(ErrCorrupt, addErr, closeArtifactsErr)
		}
	}

	closeArtifactsErr := artifactsDir.Close()
	if closeArtifactsErr != nil {
		return 0, errors.Join(ErrCorrupt, closeArtifactsErr)
	}

	if err := ctx.Err(); err != nil {
		return 0, oops.In("snapshot.store").With("operation", "read_artifacts").Wrap(err)
	}

	return totalBytes, nil
}

func readValidatedArtifact(ctx context.Context, dir *privatefs.Dir, descriptor model.Artifact) (int64, error) {
	name := descriptor.ID + ".bin.gz"

	file, err := dir.File(name, unix.O_RDONLY)
	if err != nil {
		return 0, errors.Join(ErrCorrupt, err)
	}

	size, verifyErr := verifyArtifactFile(ctx, file, descriptor, io.Discard)
	if verifyErr != nil {
		if ctx.Err() != nil {
			return 0, errors.Join(ctx.Err(), verifyErr)
		}

		return 0, errors.Join(ErrCorrupt, verifyErr)
	}

	return size, nil
}

func safeAdd(a, b int64) (int64, error) {
	if a < 0 || b < 0 {
		return 0, ErrCorrupt
	}

	if a > math.MaxInt64-b {
		return 0, errors.Join(ErrCorrupt, errByteAccountingOverflow)
	}

	return a + b, nil
}
