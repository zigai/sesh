//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	expectedReplacementDirectoryEntries = 2
	maxArtifactListingPadding           = 10
)

func (r *Repository) resumeRewriteLocked(ctx context.Context, id string) (RewriteResult, error) {
	catalog, err := r.catalog()
	if err != nil {
		return emptyRewriteResult(id, "", ""), err
	}

	if err := ctx.Err(); err != nil {
		return emptyRewriteResult(id, "", ""), oops.In("snapshot.store").With("operation", "resume_rewrite").Wrap(err)
	}

	if err := r.confirmRewriteCatalog(ctx); err != nil {
		return emptyRewriteResult(id, "", ""), err
	}

	index := catalog.rewriteIndex(id)
	if index < 0 {
		return emptyRewriteResult(id, "", ""), ErrUnavailable
	}

	existing := catalog.Rewrites[index]
	if existing.State != rewriteStatePending {
		return r.resolveNonPendingRewrite(catalog, existing)
	}

	sourceManifest, err := r.loadAndValidatePendingSource(ctx, catalog, existing)
	if err != nil {
		return emptyRewriteResult(id, existing.Source, existing.State), errors.Join(ErrRecoveryRequired, err)
	}

	removal, err := validatePendingRemoval(sourceManifest, existing)
	if err != nil {
		return emptyRewriteResult(id, existing.Source, existing.State), errors.Join(ErrRecoveryRequired, err)
	}

	if removal.Empty() {
		return r.resumeRetiredRewriteLocked(ctx, catalog, existing, removal)
	}

	return r.resumeReplacementRewriteLocked(ctx, catalog, existing, removal)
}

func (r *Repository) resolveNonPendingRewrite(catalog Catalog, existing Rewrite) (RewriteResult, error) {
	if existing.State == rewriteStateCompleted || existing.State == rewriteStateRetired {
		return r.buildRewriteResult(catalog, existing), nil
	}

	return emptyRewriteResult(existing.ID, existing.Source, existing.State), ErrCorrupt
}

func (r *Repository) loadAndValidatePendingSource(ctx context.Context, catalog Catalog, existing Rewrite) (model.Manifest, error) {
	sourceGenIdx := catalog.generationIndex(existing.Source)
	if sourceGenIdx < 0 {
		return model.Manifest{}, ErrCorrupt
	}

	for _, op := range catalog.Operations {
		if op.State == "active" && op.Source == existing.Source {
			return model.Manifest{}, ErrCorrupt
		}
	}

	sourceGen := catalog.Generations[sourceGenIdx]

	sourceManifest, err := r.readGeneration(ctx, sourceGen)
	if err != nil {
		return model.Manifest{}, err
	}

	if err := ctx.Err(); err != nil {
		return model.Manifest{}, oops.In("snapshot.store").With("operation", "resume_rewrite").Wrap(err)
	}

	return sourceManifest, nil
}

func validatePendingRemoval(sourceManifest model.Manifest, existing Rewrite) (model.Removal, error) {
	removal, err := model.RemoveSessions(sourceManifest, existing.Sessions)
	if err != nil {
		return model.Removal{}, errors.Join(ErrCorrupt, err)
	}

	if !slices.Equal(removal.Removed, existing.Sessions) ||
		!slices.Equal(removal.Artifacts, existing.Artifacts) ||
		removal.Empty() != (existing.Replacement == "") {
		return model.Removal{}, ErrCorrupt
	}

	return removal, nil
}

func (r *Repository) resumeRetiredRewriteLocked(ctx context.Context, catalog Catalog, existing Rewrite, removal model.Removal) (RewriteResult, error) {
	committed, err := completedRewriteCatalog(catalog, existing, removal, "")
	if err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, existing.State), err
	}

	if _, err := encodeCatalog(committed); err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, existing.State), err
	}

	if err := ctx.Err(); err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, existing.State), oops.In("snapshot.store").With("operation", "resume_rewrite").Wrap(err)
	}

	if err := errors.Join(r.boundary("rewrite_resume_pre_publish"), ctx.Err()); err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, existing.State), errors.Join(ErrRecoveryRequired, err)
	}

	if err := r.publish(ctx, committed, nil); err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, ""), errors.Join(ErrRecoveryRequired, err)
	}

	return r.buildRewriteResult(committed, committed.Rewrites[committed.rewriteIndex(existing.ID)]), nil
}

func (r *Repository) resumeReplacementRewriteLocked(ctx context.Context, catalog Catalog, existing Rewrite, removal model.Removal) (RewriteResult, error) {
	committed, expectedData, expectedDigest, err := prepareReplacementCatalog(catalog, existing, removal)
	if err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, existing.State), err
	}

	if err := ctx.Err(); err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, existing.State), oops.In("snapshot.store").With("operation", "resume_rewrite").Wrap(err)
	}

	generations, staging, err := r.openGenerationsAndStaging()
	if err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, existing.State), err
	}

	storageErr := r.reconcileCandidateStorage(ctx, generations, staging, existing, removal, expectedData, expectedDigest)
	if err := errors.Join(storageErr, generations.Close(), staging.Close()); err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, rewriteStatePending), errors.Join(ErrRecoveryRequired, err)
	}

	if err := ctx.Err(); err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, rewriteStatePending), oops.In("snapshot.store").With("operation", "resume_rewrite").Wrap(err)
	}

	if err := errors.Join(r.boundary("rewrite_resume_pre_publish"), ctx.Err()); err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, rewriteStatePending), errors.Join(ErrRecoveryRequired, err)
	}

	if err := r.publish(ctx, committed, nil); err != nil {
		return emptyRewriteResult(existing.ID, existing.Source, ""), errors.Join(ErrRecoveryRequired, err)
	}

	return r.buildRewriteResult(committed, committed.Rewrites[committed.rewriteIndex(existing.ID)]), nil
}

func prepareReplacementCatalog(catalog Catalog, existing Rewrite, removal model.Removal) (Catalog, []byte, string, error) {
	expectedManifest := removal.Retained

	expectedData, err := model.Encode(expectedManifest)
	if err != nil {
		return Catalog{}, nil, "", oops.In("snapshot.store").With("operation", "encode_rewrite").Wrap(err)
	}

	expectedDigest := digest(expectedData)

	committed, err := completedRewriteCatalog(catalog, existing, removal, expectedDigest)
	if err != nil {
		return Catalog{}, nil, "", err
	}

	if _, err := encodeCatalog(committed); err != nil {
		return Catalog{}, nil, "", err
	}

	return committed, expectedData, expectedDigest, nil
}

func (r *Repository) reconcileCandidateStorage(ctx context.Context, generations, staging *privatefs.Dir, existing Rewrite, removal model.Removal, expectedData []byte, expectedDigest string) error {
	genDir, genExists, stageDir, stageExists, err := openCandidateDirs(generations, staging, existing.Replacement)
	if err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}

	switch {
	case genExists:
		if err := r.validateRenamedGeneration(ctx, genDir, removal.Retained, expectedDigest); err != nil {
			return err
		}

		return r.confirmRewriteGeneration(ctx, generations, staging)
	case stageExists:
		return r.publishStagedReplacement(ctx, staging, generations, stageDir, existing, removal, expectedData)
	default:
		if err := r.stageReplacement(ctx, existing.Source, existing.Replacement, removal.Retained, expectedData, expectedDigest); err != nil {
			return errors.Join(ErrRecoveryRequired, err)
		}

		return nil
	}
}

func (r *Repository) openGenerationsAndStaging() (*privatefs.Dir, *privatefs.Dir, error) {
	generations, err := r.root.Child("generations", false)
	if err != nil {
		return nil, nil, oops.In("snapshot.store").With("operation", "open_generations").Wrapf(err, "open generations")
	}

	staging, err := r.root.Child("staging", false)
	if err != nil {
		return nil, nil, oops.In("snapshot.store").With("operation", "open_staging").Wrapf(errors.Join(err, generations.Close()), "open staging")
	}

	return generations, staging, nil
}

func openCandidateDirs(generations, staging *privatefs.Dir, replacementID string) (*privatefs.Dir, bool, *privatefs.Dir, bool, error) {
	genDir, genErr := generations.Child(replacementID, false)
	if genErr != nil && !errors.Is(genErr, os.ErrNotExist) {
		return nil, false, nil, false, oops.In("snapshot.store").With("operation", "open_candidate_gen_dir").Wrap(genErr)
	}

	genExists := genErr == nil

	stageDir, stageErr := staging.Child(replacementID, false)
	if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) {
		if genExists {
			stageErr = errors.Join(stageErr, genDir.Close())
		}

		return nil, false, nil, false, oops.In("snapshot.store").With("operation", "open_candidate_stage_dir").Wrap(stageErr)
	}

	stageExists := stageErr == nil

	if genExists && stageExists {
		return nil, false, nil, false, errors.Join(ErrCorrupt, genDir.Close(), stageDir.Close())
	}

	return genDir, genExists, stageDir, stageExists, nil
}

func (r *Repository) validateRenamedGeneration(ctx context.Context, genDir *privatefs.Dir, expectedManifest model.Manifest, expectedDigest string) error {
	valErr := validateReplacementDirectory(ctx, genDir, expectedManifest, expectedDigest)
	closeErr := genDir.Close()

	if err := errors.Join(valErr, closeErr); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}

	if err := r.boundary("rewrite_generation_published"); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}

	return nil
}

func (r *Repository) publishStagedReplacement(ctx context.Context, staging, generations, stageDir *privatefs.Dir, existing Rewrite, removal model.Removal, expectedData []byte) error {
	repairErr := r.completeRewriteStage(ctx, stageDir, existing.Source, removal.Retained, expectedData)
	if err := errors.Join(repairErr, stageDir.Close()); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}

	if err := r.boundary("rewrite_stage_synced"); err != nil {
		return errors.Join(ErrRecoveryRequired, err, ctx.Err())
	}

	if err := ctx.Err(); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}

	if err := staging.Rename(existing.Replacement, generations, existing.Replacement); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}

	if err := r.confirmRewriteGeneration(ctx, generations, staging); err != nil {
		return err
	}

	return r.boundary("rewrite_generation_published")
}

func (r *Repository) confirmRewriteGeneration(ctx context.Context, generations, staging *privatefs.Dir) error {
	if err := r.boundary("rewrite_resume_generation_sync"); err != nil {
		return errors.Join(ErrRecoveryRequired, ErrUncertain, err, ctx.Err())
	}

	if err := errors.Join(generations.Sync(), staging.Sync(), ctx.Err()); err != nil {
		return errors.Join(ErrRecoveryRequired, ErrUncertain, err)
	}

	return nil
}

func validateReplacementDirectory(ctx context.Context, dir *privatefs.Dir, manifest model.Manifest, expectedDigest string) error {
	names, err := dir.NamesLimit(model.MaxEntities + expectedReplacementDirectoryEntries)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "list_replacement_directory").Wrap(err)
	}

	if len(names) != expectedReplacementDirectoryEntries || !slices.Contains(names, "manifest.json") || !slices.Contains(names, "artifacts") {
		return ErrCorrupt
	}

	data, err := readSyncedManifest(ctx, dir)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "read_replacement_manifest").Wrap(err)
	}

	if digest(data) != expectedDigest {
		return ErrCorrupt
	}

	readManifest, err := model.Decode(data)
	if err != nil {
		return errors.Join(ErrCorrupt, err)
	}

	if readManifest.Complete() != manifest.Complete() {
		return ErrCorrupt
	}

	return validateReplacementArtifacts(ctx, dir, manifest.Artifacts)
}

func validateReplacementArtifacts(ctx context.Context, dir *privatefs.Dir, descriptors []model.Artifact) error {
	artDir, err := dir.Child("artifacts", false)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "open_replacement_artifacts").Wrap(err)
	}

	artNames, err := artDir.NamesLimit(len(descriptors) + maxArtifactListingPadding)
	if err != nil {
		return oops.In("snapshot.store").With("operation", "list_replacement_artifacts").Wrap(errors.Join(err, artDir.Close()))
	}

	if len(artNames) != len(descriptors) {
		return errors.Join(ErrCorrupt, artDir.Close())
	}

	expectedArtifacts := make(map[string]model.Artifact, len(descriptors))
	for _, descriptor := range descriptors {
		expectedArtifacts[descriptor.ID+".bin.gz"] = descriptor
	}

	for _, name := range artNames {
		descriptor, exists := expectedArtifacts[name]
		if !exists {
			return errors.Join(ErrCorrupt, artDir.Close())
		}

		if err := verifySyncedArtifact(ctx, artDir, descriptor); err != nil {
			return errors.Join(err, artDir.Close())
		}
	}

	if err := errors.Join(artDir.Sync(), artDir.Close(), dir.Sync(), ctx.Err()); err != nil {
		return oops.In("snapshot.store").With("operation", "close_replacement_artifacts").Wrap(err)
	}

	return nil
}
