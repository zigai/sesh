//go:build linux || darwin

package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"

	"github.com/samber/oops"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

type prefixWriter struct {
	file      *os.File
	remaining int64
	append    bool
}

func (r *Repository) completeRewriteStage(ctx context.Context, stage *privatefs.Dir, source string, manifest model.Manifest, data []byte) error {
	if err := validateStageNames(stage, manifest); err != nil {
		return err
	}

	generations, err := r.root.Child("generations", false)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_generations").Wrap(err)
	}

	sourceDir, err := generations.Child(source, false)
	if err != nil {
		return errors.Join(err, generations.Close())
	}

	sourceArtifacts, err := sourceDir.Child("artifacts", false)
	if err != nil {
		return errors.Join(err, sourceDir.Close(), generations.Close())
	}

	repairErr := r.completeStageFiles(ctx, stage, sourceArtifacts, manifest, data)

	return errors.Join(repairErr, sourceArtifacts.Close(), sourceDir.Close(), generations.Close())
}

func validateStageNames(stage *privatefs.Dir, manifest model.Manifest) error {
	names, err := stage.NamesLimit(expectedReplacementDirectoryEntries)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "inspect_rewrite_stage").Wrap(err)
	}

	for _, name := range names {
		if name != "manifest.json" && name != "artifacts" {
			return ErrCorrupt
		}
	}

	artifacts, err := stage.Child("artifacts", false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_stage_artifacts").Wrap(err)
	}

	names, listErr := artifacts.NamesLimit(len(manifest.Artifacts))
	if err := errors.Join(listErr, artifacts.Close()); err != nil {
		return err
	}

	allowed := make(map[string]bool, len(manifest.Artifacts))
	for _, descriptor := range manifest.Artifacts {
		allowed[descriptor.ID+".bin.gz"] = true
	}

	for _, name := range names {
		if !allowed[name] {
			return ErrCorrupt
		}
	}

	return nil
}

func (r *Repository) completeStageFiles(ctx context.Context, stage, source *privatefs.Dir, manifest model.Manifest, data []byte) error {
	emitManifest := func(dst io.Writer) error {
		_, err := io.Copy(dst, bytes.NewReader(data))
		return oops.In("snapshot.store").With("operation", "complete_rewrite_stage").Wrap(err)
	}
	if err := recoverPrefixFile(ctx, stage, "manifest.json", model.MaxManifestBytes, false, emitManifest); err != nil {
		return err
	}

	artifacts, err := stage.Child("artifacts", false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return oops.In("snapshot.store").With("operation", "open_stage_artifacts").Wrap(err)
	}

	if artifacts != nil {
		preflightErr := r.recoverStageArtifacts(ctx, source, artifacts, manifest.Artifacts, false)
		if err := errors.Join(preflightErr, artifacts.Close()); err != nil {
			return err
		}
	}

	if err := errors.Join(r.boundary("rewrite_resume_stage_validated"), ctx.Err()); err != nil {
		return err
	}

	artifacts, err = stage.Child("artifacts", false)
	if errors.Is(err, os.ErrNotExist) {
		artifacts, err = stage.CreateDir("artifacts")
	}

	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_stage_artifacts").Wrap(err)
	}

	repairErr := r.recoverStageArtifacts(ctx, source, artifacts, manifest.Artifacts, true)
	if err := errors.Join(repairErr, artifacts.Sync(), artifacts.Close()); err != nil {
		return err
	}

	if err := recoverPrefixFile(ctx, stage, "manifest.json", model.MaxManifestBytes, true, emitManifest); err != nil {
		return err
	}

	if err := stage.Sync(); err != nil {
		return oops.In("snapshot.store").With("operation", "sync_rewrite_stage").Wrap(err)
	}

	return validateReplacementDirectory(ctx, stage, manifest, digest(data))
}

func (r *Repository) recoverStageArtifacts(ctx context.Context, source, stage *privatefs.Dir, descriptors []model.Artifact, write bool) error {
	for _, descriptor := range descriptors {
		emit := func(dst io.Writer) error {
			compressed := gzip.NewWriter(dst)
			if err := verifyArtifact(ctx, source, descriptor, compressed); err != nil {
				return err
			}

			return oops.In("snapshot.store").With("operation", "complete_stage_artifact").Wrap(compressed.Close())
		}
		if err := recoverPrefixFile(ctx, stage, descriptor.ID+".bin.gz", maxCompressedBytes, write, emit); err != nil {
			return err
		}

		if write {
			if err := errors.Join(r.boundary("rewrite_resume_artifact_synced"), ctx.Err()); err != nil {
				return err
			}
		}
	}

	return nil
}

func recoverPrefixFile(ctx context.Context, dir *privatefs.Dir, name string, limit int64, write bool, emit func(io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return oops.In("snapshot.store").With("operation", "recover_rewrite_file").Wrap(err)
	}

	flags := unix.O_RDONLY
	if write {
		flags = unix.O_RDWR | unix.O_APPEND
	}

	file, err := dir.File(name, flags)
	if errors.Is(err, os.ErrNotExist) {
		if !write {
			return nil
		}

		file, err = dir.File(name, unix.O_RDWR|unix.O_APPEND|unix.O_CREAT|unix.O_EXCL)
	}

	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_rewrite_file").Wrap(err)
	}

	completeErr := completePrefixFile(ctx, file, limit, write, emit)

	return errors.Join(completeErr, file.Close())
}

func completePrefixFile(ctx context.Context, file *os.File, limit int64, write bool, emit func(io.Writer) error) error {
	info, err := file.Stat()
	if err != nil {
		return oops.In("snapshot.store").With("operation", "inspect_rewrite_file").Wrap(err)
	}

	if info.Size() > limit {
		return ErrCorrupt
	}

	reader := &prefixWriter{file: file, remaining: info.Size(), append: false}
	if err := emit(reader); err != nil {
		return err
	}

	if reader.remaining != 0 {
		return ErrCorrupt
	}

	if !write {
		return oops.In("snapshot.store").With("operation", "validate_rewrite_file").Wrap(ctx.Err())
	}

	if err := ctx.Err(); err != nil {
		return oops.In("snapshot.store").With("operation", "recover_rewrite_file").Wrap(err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return oops.In("snapshot.store").With("operation", "rewind_rewrite_file").Wrap(err)
	}

	reader.remaining, reader.append = info.Size(), true
	if err := emit(reader); err != nil {
		return err
	}

	if reader.remaining != 0 {
		return ErrCorrupt
	}

	return errors.Join(file.Sync(), ctx.Err())
}

func (w *prefixWriter) Write(data []byte) (int, error) {
	count := len(data)

	matched := int(min(int64(count), w.remaining))
	if matched != 0 {
		existing := make([]byte, matched)
		if _, err := io.ReadFull(w.file, existing); err != nil {
			return 0, oops.In("snapshot.store").With("operation", "read_rewrite_prefix").Wrap(err)
		}

		if !bytes.Equal(existing, data[:matched]) {
			return 0, ErrCorrupt
		}

		w.remaining -= int64(matched)
	}

	if !w.append || matched == count {
		return count, nil
	}

	written, err := w.file.Write(data[matched:])

	return matched + written, oops.In("snapshot.store").With("operation", "append_rewrite_suffix").Wrap(err)
}

func readSyncedManifest(ctx context.Context, dir *privatefs.Dir) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, oops.In("snapshot.store").With("operation", "verify_rewrite_manifest").Wrap(err)
	}

	file, err := dir.File("manifest.json", unix.O_RDONLY)
	if err != nil {
		return nil, oops.In("snapshot.store").With("operation", "open_rewrite_manifest").Wrap(err)
	}

	if err := file.Sync(); err != nil {
		return nil, errors.Join(ErrUncertain, err, file.Close())
	}

	data, readErr := io.ReadAll(io.LimitReader(cancellableReader(ctx, file), model.MaxManifestBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}

	if len(data) > model.MaxManifestBytes {
		return nil, ErrCorrupt
	}

	return data, nil
}

func verifySyncedArtifact(ctx context.Context, dir *privatefs.Dir, descriptor model.Artifact) error {
	if err := ctx.Err(); err != nil {
		return oops.In("snapshot.store").With("operation", "recover_rewrite_file").Wrap(err)
	}

	file, err := dir.File(descriptor.ID+".bin.gz", unix.O_RDONLY)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_rewrite_artifact").Wrap(err)
	}

	if err := file.Sync(); err != nil {
		return errors.Join(ErrUncertain, err, file.Close())
	}

	_, err = verifyArtifactFile(ctx, file, descriptor, io.Discard)

	return err
}
