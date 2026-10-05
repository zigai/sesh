//go:build linux || darwin

package store

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

type writerFunc func([]byte) (int, error)

type RewriteResult struct {
	ID          string
	Source      string
	Replacement Generation
	Removed     []string
	Artifacts   []string
	State       string
	Empty       bool
}

func (r *Repository) RemoveSessions(ctx context.Context, id, source string, sessionIDs []string) (RewriteResult, error) {
	if !validID(id) || !validID(source) || len(sessionIDs) > model.MaxEntities {
		return emptyRewriteResult(id, source, ""), repositoryError("remove_sessions", ErrCorrupt)
	}

	canonicalSessions, err := canonicalizeSessionIDs(sessionIDs)
	if err != nil {
		return emptyRewriteResult(id, source, ""), repositoryError("remove_sessions", err)
	}

	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return emptyRewriteResult(id, source, ""), repositoryError("remove_sessions", oops.In("snapshot.store").With("operation", "lock_repository").Wrapf(err, "lock repository"))
	}

	result, removeErr := r.removeSessionsLocked(ctx, id, source, sessionIDs, canonicalSessions)

	return result, repositoryError("remove_sessions", errors.Join(removeErr, lock.Close()))
}

func (r *Repository) ReconcileRewrite(ctx context.Context, id string) (RewriteResult, error) {
	if !validID(id) {
		return emptyRewriteResult(id, "", ""), repositoryError("reconcile_rewrite", ErrCorrupt)
	}

	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return emptyRewriteResult(id, "", ""), repositoryError("reconcile_rewrite", oops.In("snapshot.store").With("operation", "lock_repository").Wrapf(err, "lock repository"))
	}

	result, reconcileErr := r.reconcileRewriteLocked(ctx, id)

	return result, repositoryError("reconcile_rewrite", errors.Join(reconcileErr, lock.Close()))
}

func (r *Repository) ResumeRewrite(ctx context.Context, id string) (RewriteResult, error) {
	if !validID(id) {
		return emptyRewriteResult(id, "", ""), repositoryError("resume_rewrite", ErrCorrupt)
	}

	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return emptyRewriteResult(id, "", ""), repositoryError("resume_rewrite", oops.In("snapshot.store").With("operation", "lock_repository").Wrapf(err, "lock repository"))
	}

	result, resumeErr := r.resumeRewriteLocked(ctx, id)

	return result, repositoryError("resume_rewrite", errors.Join(resumeErr, lock.Close()))
}

func (r *Repository) reconcileRewriteLocked(ctx context.Context, id string) (RewriteResult, error) {
	catalog, err := r.catalog()
	if err != nil {
		return emptyRewriteResult(id, "", ""), err
	}

	if err := ctx.Err(); err != nil {
		return emptyRewriteResult(id, "", ""), oops.In("snapshot.store").With("operation", "reconcile_rewrite").Wrap(err)
	}

	if err := r.confirmRewriteCatalog(ctx); err != nil {
		return emptyRewriteResult(id, "", ""), err
	}

	index := catalog.rewriteIndex(id)
	if index < 0 {
		return emptyRewriteResult(id, "", ""), ErrUnavailable
	}

	existing := catalog.Rewrites[index]
	if existing.State == rewriteStatePending {
		return r.buildRewriteResult(catalog, existing), ErrRecoveryRequired
	}

	return r.buildRewriteResult(catalog, existing), nil
}

func (r *Repository) removeSessionsLocked(ctx context.Context, id, source string, sessionIDs, canonicalSessions []string) (RewriteResult, error) {
	catalog, err := r.catalog()
	if err != nil {
		return emptyRewriteResult(id, source, ""), err
	}

	if err := ctx.Err(); err != nil {
		return emptyRewriteResult(id, source, ""), oops.In("snapshot.store").With("operation", "remove_sessions").Wrap(err)
	}

	index := catalog.rewriteIndex(id)
	if index >= 0 {
		return r.confirmExistingRewriteLocked(ctx, catalog, catalog.Rewrites[index], source, canonicalSessions)
	}

	sourceGen, err := r.validateRewritePreconditions(catalog, source)
	if err != nil {
		return emptyRewriteResult(id, source, ""), err
	}

	sourceManifest, err := r.readGeneration(ctx, sourceGen)
	if err != nil {
		return emptyRewriteResult(id, source, ""), err
	}

	removal, err := model.RemoveSessions(sourceManifest, sessionIDs)
	if err != nil {
		return emptyRewriteResult(id, source, ""), err
	}

	if err := ctx.Err(); err != nil {
		return emptyRewriteResult(id, source, ""), oops.In("snapshot.store").With("operation", "remove_sessions").Wrap(err)
	}

	return r.executeRewrite(ctx, catalog, id, source, removal)
}

func (r *Repository) validateRewritePreconditions(catalog Catalog, source string) (Generation, error) {
	if len(catalog.Rewrites)+1 > maxEntries {
		return Generation{}, ErrCorrupt
	}

	genIndex := catalog.generationIndex(source)
	if genIndex < 0 {
		return Generation{}, ErrUnavailable
	}

	for _, op := range catalog.Operations {
		if op.State == "active" && op.Source == source {
			return Generation{}, ErrConflict
		}
	}

	for _, rw := range catalog.Rewrites {
		if rw.State == rewriteStatePending && rw.Source == source {
			return Generation{}, ErrConflict
		}
	}

	return catalog.Generations[genIndex], nil
}

func (r *Repository) executeRewrite(ctx context.Context, catalog Catalog, id, source string, removal model.Removal) (RewriteResult, error) {
	pending, replacementData, err := prepareRewrite(id, source, removal)
	if err != nil {
		return emptyRewriteResult(id, source, ""), err
	}

	catalog.Rewrites = append(catalog.Rewrites, pending)

	committed, err := completedRewriteCatalog(catalog, pending, removal, digest(replacementData))
	if err != nil {
		return emptyRewriteResult(id, source, ""), err
	}

	if _, err := encodeCatalog(committed); err != nil {
		return emptyRewriteResult(id, source, ""), err
	}

	if err := ctx.Err(); err != nil {
		return emptyRewriteResult(id, source, ""), err
	}

	if err := r.publish(ctx, catalog, nil); err != nil {
		return emptyRewriteResult(id, source, ""), err
	}

	if err := r.boundary("rewrite_registered"); err != nil {
		return emptyRewriteResult(id, source, rewriteStatePending), errors.Join(ErrRecoveryRequired, err)
	}

	if !removal.Empty() {
		if err := r.stageReplacement(ctx, source, pending.Replacement, removal.Retained, replacementData, digest(replacementData)); err != nil {
			return emptyRewriteResult(id, source, rewriteStatePending), errors.Join(ErrRecoveryRequired, err)
		}
	}

	if err := ctx.Err(); err != nil {
		return emptyRewriteResult(id, source, rewriteStatePending), errors.Join(ErrRecoveryRequired, err)
	}

	if err := r.publish(ctx, committed, nil); err != nil {
		return emptyRewriteResult(id, source, ""), errors.Join(ErrRecoveryRequired, err)
	}

	return r.buildRewriteResult(committed, committed.Rewrites[len(committed.Rewrites)-1]), nil
}

func prepareRewrite(id, source string, removal model.Removal) (Rewrite, []byte, error) {
	pending := Rewrite{
		ID: id, Source: source, Replacement: "", State: rewriteStatePending,
		Sessions:  append([]string{}, removal.Removed...),
		Artifacts: append([]string{}, removal.Artifacts...),
	}
	if removal.Empty() {
		return pending, nil, nil
	}

	data, err := model.Encode(removal.Retained)
	if err != nil {
		return pending, nil, oops.In("snapshot.store").With("operation", "encode_rewrite").Wrap(err)
	}

	pending.Replacement = newID()

	return pending, data, nil
}

func completedRewriteCatalog(catalog Catalog, pending Rewrite, removal model.Removal, manifestDigest string) (Catalog, error) {
	if !removal.Empty() && catalog.Sequence == ^uint64(0) {
		return Catalog{}, ErrCorrupt
	}

	catalog.Rewrites = slices.Clone(catalog.Rewrites)
	catalog.Generations = slices.DeleteFunc(slices.Clone(catalog.Generations), func(g Generation) bool { return g.ID == pending.Source })

	index := catalog.rewriteIndex(pending.ID)
	if index < 0 {
		return Catalog{}, ErrCorrupt
	}

	catalog.Rewrites[index].State = rewriteStateRetired
	if catalog.Current == pending.Source {
		sourceAttemptIdx := catalog.attemptIndex(pending.Source)
		latestSaveSeq := catalog.latestPublishedSaveSequence()

		if sourceAttemptIdx >= 0 && (latestSaveSeq == 0 || catalog.Attempts[sourceAttemptIdx].Sequence >= latestSaveSeq) {
			catalog.Current = pending.Replacement
		} else if removal.Empty() {
			catalog.Current = ""
		}
	}

	if removal.Empty() {
		return catalog, nil
	}

	catalog.Sequence++

	attempt := Attempt{
		ID: pending.Replacement, Sequence: catalog.Sequence,
		State: "incomplete", Reason: "parity_incomplete", ManifestSHA256: manifestDigest,
	}
	if removal.Retained.Complete() {
		attempt.State, attempt.Reason = "committed", ""
	}

	catalog.Attempts = append(slices.Clone(catalog.Attempts), attempt)
	catalog.Generations = append(catalog.Generations, Generation{
		ID: attempt.ID, Sequence: attempt.Sequence,
		ManifestSHA256: manifestDigest, Complete: removal.Retained.Complete(),
	})
	catalog.Rewrites[index].State = rewriteStateCompleted

	return catalog, nil
}

func (r *Repository) stageReplacement(ctx context.Context, source, replacementID string, manifest model.Manifest, data []byte, expectedDigest string) error {
	staging, err := r.root.Child("staging", false)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_staging").Wrapf(err, "open staging")
	}

	dir, err := staging.CreateDir(replacementID)
	if err != nil {
		return errors.Join(err, staging.Close())
	}

	stageErr := r.writeReplacementGeneration(ctx, source, dir, manifest, data, expectedDigest)
	if err := errors.Join(stageErr, dir.Close()); err != nil {
		return errors.Join(err, staging.Close())
	}

	if err := r.boundary("rewrite_stage_synced"); err != nil {
		return errors.Join(err, staging.Close())
	}

	generations, err := r.root.Child("generations", false)
	if err != nil {
		return errors.Join(err, staging.Close())
	}

	renameErr := staging.Rename(replacementID, generations, replacementID)
	if err := errors.Join(renameErr, generations.Sync(), staging.Sync(), generations.Close(), staging.Close()); err != nil {
		if renameErr == nil {
			return errors.Join(ErrUncertain, err)
		}

		return err
	}

	return r.boundary("rewrite_generation_published")
}

func (r *Repository) writeReplacementGeneration(ctx context.Context, source string, dstDir *privatefs.Dir, manifest model.Manifest, data []byte, expectedDigest string) error {
	artifactDir, err := dstDir.CreateDir("artifacts")
	if err != nil {
		return oops.In("snapshot.store").With("operation", "create_artifact_directory").Wrapf(err, "create artifact directory")
	}

	if len(manifest.Artifacts) > 0 {
		generations, err := r.root.Child("generations", false)
		if err != nil {
			return errors.Join(err, artifactDir.Close())
		}

		sourceDir, err := generations.Child(source, false)
		if err != nil {
			return errors.Join(err, generations.Close(), artifactDir.Close())
		}

		sourceArtifactDir, err := sourceDir.Child("artifacts", false)
		if err != nil {
			return errors.Join(err, sourceDir.Close(), generations.Close(), artifactDir.Close())
		}

		copyErr := r.copyArtifacts(ctx, sourceArtifactDir, artifactDir, manifest.Artifacts)
		closeErr := errors.Join(sourceArtifactDir.Close(), sourceDir.Close(), generations.Close())

		if err := errors.Join(copyErr, closeErr); err != nil {
			return errors.Join(err, artifactDir.Close())
		}
	}

	if err := errors.Join(artifactDir.Sync(), artifactDir.Close()); err != nil {
		return err
	}

	if err := writeFile(dstDir, "manifest.json", data); err != nil {
		return err
	}

	if err := dstDir.Sync(); err != nil {
		return oops.In("snapshot.store").With("operation", "sync_generation").Wrapf(err, "sync generation")
	}

	_, err = readManifest(ctx, dstDir, expectedDigest)

	return err
}

func (r *Repository) copyArtifacts(ctx context.Context, srcDir, dstDir *privatefs.Dir, descriptors []model.Artifact) error {
	for _, descriptor := range descriptors {
		if err := r.copyArtifact(ctx, srcDir, dstDir, descriptor); err != nil {
			return err
		}
	}

	return nil
}

func (r *Repository) copyArtifact(ctx context.Context, srcDir, dstDir *privatefs.Dir, descriptor model.Artifact) error {
	if err := ctx.Err(); err != nil {
		return oops.In("snapshot.store").With("operation", "copy_artifact").Wrap(err)
	}

	srcFile, err := srcDir.File(descriptor.ID+".bin.gz", unix.O_RDONLY)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_source_artifact").Wrapf(err, "open source artifact")
	}

	dstFile, err := dstDir.File(descriptor.ID+".bin.gz", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return errors.Join(err, srcFile.Close())
	}

	dstGz := gzip.NewWriter(writerFunc(func(data []byte) (int, error) {
		n, err := dstFile.Write(data)
		if err != nil {
			return n, oops.In("snapshot.store").With("operation", "write_rewrite_artifact").Wrap(err)
		}

		return n, r.boundary("rewrite_artifact_copy")
	}))
	dstHash := sha256.New()
	dstWriter := io.MultiWriter(dstGz, dstHash)

	_, verifyErr := verifyArtifactFile(ctx, srcFile, descriptor, dstWriter)

	var compressionErr error
	if verifyErr == nil {
		compressionErr = dstGz.Close()
	}

	closeErr := errors.Join(compressionErr, dstFile.Sync(), dstFile.Close())

	if err := errors.Join(verifyErr, closeErr); err != nil {
		return oops.In("snapshot.store").With("operation", "copy_artifact").Wrapf(err, "copy artifact")
	}

	if hex.EncodeToString(dstHash.Sum(nil)) != descriptor.SHA256 {
		return ErrCorrupt
	}

	return nil
}

func (f writerFunc) Write(data []byte) (int, error) { return f(data) }

func (r *Repository) confirmExistingRewriteLocked(ctx context.Context, catalog Catalog, existing Rewrite, source string, canonicalSessions []string) (RewriteResult, error) {
	if existing.Source != source || !slices.Equal(existing.Sessions, canonicalSessions) {
		return emptyRewriteResult(existing.ID, source, ""), ErrConflict
	}

	if err := r.confirmRewriteCatalog(ctx); err != nil {
		return emptyRewriteResult(existing.ID, source, ""), err
	}

	if existing.State == rewriteStatePending {
		return r.buildRewriteResult(catalog, existing), ErrRecoveryRequired
	}

	return r.buildRewriteResult(catalog, existing), nil
}

func (r *Repository) buildRewriteResult(catalog Catalog, rw Rewrite) RewriteResult {
	var replacementGen Generation

	replacementGen.ID = rw.Replacement

	if rw.Replacement != "" {
		if idx := catalog.generationIndex(rw.Replacement); idx >= 0 {
			replacementGen = catalog.Generations[idx]
		} else if idx := catalog.attemptIndex(rw.Replacement); idx >= 0 {
			attempt := catalog.Attempts[idx]
			replacementGen = Generation{
				ID:             attempt.ID,
				Sequence:       attempt.Sequence,
				ManifestSHA256: attempt.ManifestSHA256,
				Complete:       attempt.State == "committed",
			}
		}
	}

	return RewriteResult{
		ID:          rw.ID,
		Source:      rw.Source,
		Replacement: replacementGen,
		Removed:     cloneStringSlice(rw.Sessions),
		Artifacts:   cloneStringSlice(rw.Artifacts),
		State:       rw.State,
		Empty:       rw.State == "retired",
	}
}

func canonicalizeSessionIDs(sessionIDs []string) ([]string, error) {
	if len(sessionIDs) == 0 {
		return []string{}, nil
	}

	if len(sessionIDs) > model.MaxEntities {
		return nil, model.ErrInvalid
	}

	for _, id := range sessionIDs {
		if !model.ValidID(id) {
			return nil, model.ErrInvalid
		}
	}

	sorted := slices.Clone(sessionIDs)
	slices.Sort(sorted)

	return slices.Compact(sorted), nil
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}

	return slices.Clone(values)
}

func emptyRewriteResult(id, source, state string) RewriteResult {
	return RewriteResult{
		ID:          id,
		Source:      source,
		Replacement: Generation{ID: "", Sequence: 0, ManifestSHA256: "", Complete: false},
		Removed:     nil,
		Artifacts:   nil,
		State:       state,
		Empty:       false,
	}
}

func (r *Repository) confirmRewriteCatalog(ctx context.Context) error {
	if err := r.boundary("rewrite_reconcile_sync"); err != nil {
		return errors.Join(ErrUncertain, err, ctx.Err())
	}

	if err := ctx.Err(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	if err := errors.Join(r.root.Sync(), ctx.Err()); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	return nil
}
