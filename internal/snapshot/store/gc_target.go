//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const collectionGenerationEntries = 2

var (
	errCollectionUnsafe        = errors.New("unsafe retired generation entry")
	errCollectionStructure     = errors.New("unexpected retired generation contents")
	errCollectionContent       = errors.New("invalid retired generation content")
	errCollectionContradiction = errors.New("retired generation contradicts its deletion intent")
)

type manifestArtifacts struct {
	descriptors []model.Artifact
	otherSchema bool
}

type collectionTarget struct {
	dir       *privatefs.Dir
	artifacts *privatefs.Dir
	manifest  bool
	files     []CollectionFile
	bytes     int64
}

func collectionRefusal(err error) (string, bool) {
	switch {
	case unsafeCollectionEntry(err):
		return CollectionReasonUnsafe, true
	case errors.Is(err, errCollectionStructure):
		return CollectionReasonUnknownContent, true
	case errors.Is(err, errCollectionContent):
		return CollectionReasonInvalidContent, true
	default:
		return "", false
	}
}

func unsafeCollectionEntry(err error) bool {
	return errors.Is(err, errCollectionUnsafe) || errors.Is(err, privatefs.ErrUnsafe) ||
		errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR)
}

func collectionContradiction(err error) (string, bool) {
	switch {
	case errors.Is(err, errCollectionUnsafe),
		errors.Is(err, errCollectionStructure),
		errors.Is(err, errCollectionContent),
		errors.Is(err, errCollectionContradiction):
		return CollectionQuarantineContent, true
	default:
		return "", false
	}
}

func interruption(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return errors.Join(ctxErr, err)
	}

	return nil
}

func (target *collectionTarget) close() error {
	artifacts, dir := target.artifacts, target.dir
	target.artifacts, target.dir = nil, nil

	return errors.Join(closeCollectionDirectory(artifacts), closeCollectionDirectory(dir))
}

func closeCollectionDirectory(dir *privatefs.Dir) error {
	if dir == nil {
		return nil
	}

	if err := dir.Close(); err != nil {
		return oops.In("snapshot.store").With("operation", "close_collection_directory").Wrapf(err, "close collection directory")
	}

	return nil
}

func (r *Repository) openGenerationDirectory(id string) (*privatefs.Dir, error) {
	parent, err := r.root.Child(generationsDirectory, false)
	if err != nil {
		return nil, errors.Join(ErrCorrupt, err)
	}

	dir, openErr := parent.Child(id, false)
	if err := errors.Join(openErr, parent.Close()); err != nil {
		if dir != nil {
			err = errors.Join(err, dir.Close())
		}

		return nil, err
	}

	return dir, nil
}

func (run *collectionRun) inspectCandidate(ctx context.Context, candidate collectionCandidate) (CollectionIntent, error) {
	dir, err := run.repository.openGenerationDirectory(candidate.id)
	if err != nil {
		return CollectionIntent{}, errors.Join(errCollectionUnsafe, err, ctx.Err())
	}

	intent, inspectErr := inspectGenerationContent(ctx, dir, candidate)

	return intent, errors.Join(inspectErr, dir.Close())
}

func inspectGenerationContent(ctx context.Context, dir *privatefs.Dir, candidate collectionCandidate) (CollectionIntent, error) {
	names, err := collectionNames(dir, collectionGenerationEntries)
	if err != nil {
		return CollectionIntent{}, err
	}

	if !slices.Equal(slices.Sorted(slices.Values(names)), []string{collectionArtifactsName, collectionManifestName}) {
		return CollectionIntent{}, errCollectionStructure
	}

	return readGenerationInventory(ctx, dir, candidate)
}

func readGenerationInventory(ctx context.Context, dir *privatefs.Dir, candidate collectionCandidate) (CollectionIntent, error) {
	data, err := dir.Read(collectionManifestName, model.MaxManifestBytes)
	if err != nil {
		return CollectionIntent{}, errors.Join(errCollectionUnsafe, err)
	}

	if digest(data) != candidate.manifestSHA256 {
		return CollectionIntent{}, errCollectionContent
	}

	owned, err := decodeManifestArtifacts(data)
	if err != nil {
		return CollectionIntent{}, errors.Join(errCollectionContent, err)
	}

	artifacts, err := dir.Child(collectionArtifactsName, false)
	if err != nil {
		return CollectionIntent{}, errors.Join(errCollectionUnsafe, err)
	}

	var (
		files         []CollectionFile
		artifactBytes int64
		artifactErr   error
	)

	if owned.otherSchema {
		files, artifactBytes, artifactErr = inventoryListedArtifacts(ctx, artifacts)
	} else {
		files, artifactBytes, artifactErr = inventoryArtifacts(ctx, artifacts, owned.descriptors)
	}

	closeErr := artifacts.Close()

	if err := errors.Join(artifactErr, closeErr); err != nil {
		return CollectionIntent{}, err
	}

	total, err := safeAdd(artifactBytes, int64(len(data)))
	if err != nil {
		return CollectionIntent{}, errors.Join(errCollectionContent, err)
	}

	return CollectionIntent{
		Version:        collectionVersion,
		Generation:     candidate.id,
		Sequence:       candidate.sequence,
		State:          collectionStateDeleting,
		Reason:         "",
		ManifestSHA256: digest(data),
		ManifestBytes:  int64(len(data)),
		Files:          files,
		Bytes:          total,
	}, nil
}

func decodeManifestArtifacts(data []byte) (manifestArtifacts, error) {
	manifest, err := model.Decode(data)
	if errors.Is(err, model.ErrVersion) {
		return manifestArtifacts{descriptors: nil, otherSchema: true}, nil
	}

	if err != nil {
		return manifestArtifacts{}, oops.In("snapshot.store").With("operation", "decode_retired_manifest").Wrap(err)
	}

	return manifestArtifacts{descriptors: manifest.Artifacts, otherSchema: false}, nil
}

func (a manifestArtifacts) size(ctx context.Context, dir *privatefs.Dir, index int, name string) (int64, error) {
	if a.otherSchema {
		return listedArtifactSize(dir, name)
	}

	return readValidatedArtifact(ctx, dir, a.descriptors[index])
}

func listedArtifactSize(dir *privatefs.Dir, name string) (int64, error) {
	file, err := dir.File(name, unix.O_RDONLY)
	if err != nil {
		return 0, oops.In("snapshot.store").With("operation", "open_listed_artifact", "artifact", name).Wrap(err)
	}

	info, statErr := file.Stat()
	if err := errors.Join(statErr, file.Close()); err != nil {
		return 0, err
	}

	if info.Size() <= 0 || info.Size() > maxCompressedBytes {
		return 0, ErrCorrupt
	}

	return info.Size(), nil
}

func inventoryListedArtifacts(ctx context.Context, dir *privatefs.Dir) ([]CollectionFile, int64, error) {
	names, err := collectionNames(dir, model.MaxEntities)
	if err != nil {
		return nil, 0, err
	}

	files := make([]CollectionFile, 0, len(names))

	var total int64

	for _, name := range slices.Sorted(slices.Values(names)) {
		if err := ctx.Err(); err != nil {
			return nil, 0, oops.In("snapshot.store").With("operation", "inventory_generation_artifacts").Wrap(err)
		}

		if !validCollectionFileName(name) {
			return nil, 0, errCollectionStructure
		}

		size, err := listedArtifactSize(dir, name)
		if err != nil {
			return nil, 0, errors.Join(errCollectionContent, err)
		}

		total, err = safeAdd(total, size)
		if err != nil {
			return nil, 0, errors.Join(errCollectionContent, err)
		}

		files = append(files, CollectionFile{Name: name, Bytes: size})
	}

	return files, total, nil
}

func inventoryArtifacts(ctx context.Context, dir *privatefs.Dir, descriptors []model.Artifact) ([]CollectionFile, int64, error) {
	names, err := collectionNames(dir, len(descriptors))
	if err != nil {
		return nil, 0, err
	}

	owned := make(map[string]bool, len(descriptors))
	for _, descriptor := range descriptors {
		owned[descriptor.ID+collectionArtifactSuffix] = true
	}

	if len(names) != len(owned) {
		return nil, 0, errCollectionStructure
	}

	for _, name := range names {
		if !owned[name] {
			return nil, 0, errCollectionStructure
		}
	}

	files := make([]CollectionFile, 0, len(descriptors))

	var total int64

	for _, descriptor := range descriptors {
		if err := ctx.Err(); err != nil {
			return nil, 0, oops.In("snapshot.store").With("operation", "inventory_generation_artifacts").Wrap(err)
		}

		size, err := readValidatedArtifact(ctx, dir, descriptor)
		if err != nil {
			return nil, 0, errors.Join(errCollectionContent, err)
		}

		total, err = safeAdd(total, size)
		if err != nil {
			return nil, 0, errors.Join(errCollectionContent, err)
		}

		files = append(files, CollectionFile{Name: descriptor.ID + collectionArtifactSuffix, Bytes: size})
	}

	return files, total, nil
}

func (run *collectionRun) verifyTarget(ctx context.Context, intent CollectionIntent) (collectionTarget, error) {
	dir, err := run.repository.openGenerationDirectory(intent.Generation)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrCorrupt) {
			var removed collectionTarget

			return removed, nil
		}

		return collectionTarget{}, errors.Join(errCollectionUnsafe, err)
	}

	return verifyGenerationContent(ctx, dir, intent)
}

func verifyGenerationContent(ctx context.Context, dir *privatefs.Dir, intent CollectionIntent) (collectionTarget, error) {
	if err := ctx.Err(); err != nil {
		return collectionTarget{}, errors.Join(err, dir.Close())
	}

	names, err := collectionNames(dir, collectionGenerationEntries)
	if err != nil {
		return collectionTarget{}, errors.Join(err, dir.Close())
	}

	target := collectionTarget{
		dir:       dir,
		artifacts: nil,
		manifest:  slices.Contains(names, collectionManifestName),
		files:     nil,
		bytes:     0,
	}

	if err := verifyGenerationEntries(names); err != nil {
		return collectionTarget{}, errors.Join(err, dir.Close())
	}

	var owned manifestArtifacts

	if target.manifest {
		var err error

		owned, err = verifyGenerationManifest(dir, intent)
		if err != nil {
			return collectionTarget{}, errors.Join(err, dir.Close())
		}

		target.bytes = intent.ManifestBytes
	}

	if !slices.Contains(names, collectionArtifactsName) {
		return target, nil
	}

	return verifyTargetArtifacts(ctx, dir, target, intent, owned)
}

func verifyGenerationEntries(names []string) error {
	for _, name := range names {
		if name != collectionManifestName && name != collectionArtifactsName {
			return errCollectionStructure
		}
	}

	return nil
}

func verifyTargetArtifacts(ctx context.Context, dir *privatefs.Dir, target collectionTarget, intent CollectionIntent, owned manifestArtifacts) (collectionTarget, error) {
	artifacts, err := dir.Child(collectionArtifactsName, false)
	if err != nil {
		return collectionTarget{}, errors.Join(errCollectionUnsafe, err, dir.Close())
	}

	files, bytes, verifyErr := verifyArtifactFiles(ctx, artifacts, intent, owned)
	if verifyErr != nil {
		return collectionTarget{}, errors.Join(verifyErr, artifacts.Close(), dir.Close())
	}

	if !target.manifest && len(files) > 0 {
		return collectionTarget{}, errors.Join(errCollectionContradiction, artifacts.Close(), dir.Close())
	}

	target.artifacts, target.files = artifacts, files
	target.bytes += bytes

	return target, nil
}

func verifyGenerationManifest(dir *privatefs.Dir, intent CollectionIntent) (manifestArtifacts, error) {
	data, err := dir.Read(collectionManifestName, model.MaxManifestBytes)
	if err != nil {
		return manifestArtifacts{}, errors.Join(errCollectionUnsafe, err)
	}

	if int64(len(data)) != intent.ManifestBytes || digest(data) != intent.ManifestSHA256 {
		return manifestArtifacts{}, errCollectionContradiction
	}

	owned, err := decodeManifestArtifacts(data)
	if err != nil {
		return manifestArtifacts{}, errors.Join(errCollectionContradiction, err)
	}

	if owned.otherSchema {
		return owned, nil
	}

	if len(owned.descriptors) != len(intent.Files) {
		return manifestArtifacts{}, errCollectionContradiction
	}

	for index, descriptor := range owned.descriptors {
		if descriptor.ID+collectionArtifactSuffix != intent.Files[index].Name {
			return manifestArtifacts{}, errCollectionContradiction
		}
	}

	return owned, nil
}

func verifyArtifactFiles(ctx context.Context, dir *privatefs.Dir, intent CollectionIntent, manifest manifestArtifacts) ([]CollectionFile, int64, error) {
	names, err := collectionNames(dir, len(intent.Files))
	if err != nil {
		return nil, 0, err
	}

	if len(names) > 0 && !manifest.otherSchema && len(manifest.descriptors) != len(intent.Files) {
		return nil, 0, errCollectionContradiction
	}

	owned := make(map[string]bool, len(intent.Files))
	for _, file := range intent.Files {
		owned[file.Name] = true
	}

	present := make(map[string]bool, len(names))
	for _, name := range names {
		if !owned[name] {
			return nil, 0, errCollectionStructure
		}

		present[name] = true
	}

	files := make([]CollectionFile, 0, len(names))

	var total int64

	for index, file := range intent.Files {
		if !present[file.Name] {
			continue
		}

		size, err := manifest.size(ctx, dir, index, file.Name)
		if err != nil {
			return nil, 0, errors.Join(errCollectionContent, err)
		}

		if size != file.Bytes {
			return nil, 0, errCollectionContradiction
		}

		files = append(files, file)
		total += size
	}

	return files, total, nil
}

func (run *collectionRun) remove(ctx context.Context, intent CollectionIntent, target *collectionTarget) (int64, error) {
	removed, err := run.removeArtifacts(ctx, target)
	if err != nil {
		return removed, err
	}

	manifestBytes, err := run.removeManifest(ctx, intent, target)

	removed += manifestBytes
	if err != nil {
		return removed, err
	}

	if err := target.close(); err != nil {
		return removed, err
	}

	if err := run.removeGenerationDirectory(ctx, intent.Generation); err != nil {
		return removed, err
	}

	return removed, run.repository.boundary(collectionPhaseGenerationUnlinked)
}

func (run *collectionRun) removeArtifacts(ctx context.Context, target *collectionTarget) (int64, error) {
	if target.artifacts == nil {
		return 0, nil
	}

	var removed int64

	for _, file := range target.files {
		if err := ctx.Err(); err != nil {
			return removed, errors.Join(err, target.close())
		}

		if err := target.artifacts.Remove(file.Name); err != nil {
			return removed, errors.Join(collectionOperationError("remove_generation_artifact", err), target.close())
		}

		removed += file.Bytes

		if err := run.repository.boundary(collectionPhaseArtifactUnlinked); err != nil {
			return removed, errors.Join(err, target.close())
		}
	}

	if err := errors.Join(target.artifacts.Sync(), target.artifacts.Close()); err != nil {
		target.artifacts = nil

		return removed, errors.Join(err, target.close())
	}

	target.artifacts = nil

	if err := ctx.Err(); err != nil {
		return removed, errors.Join(err, target.close())
	}

	if err := target.dir.RemoveDir(collectionArtifactsName); err != nil {
		return removed, errors.Join(collectionOperationError("remove_artifact_directory", err), target.close())
	}

	if err := run.repository.boundary(collectionPhaseArtifactsUnlinked); err != nil {
		return removed, errors.Join(err, target.close())
	}

	return removed, nil
}

func (run *collectionRun) removeManifest(ctx context.Context, intent CollectionIntent, target *collectionTarget) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, errors.Join(err, target.close())
	}

	if !target.manifest {
		return 0, nil
	}

	if err := target.dir.Remove(collectionManifestName); err != nil {
		return 0, errors.Join(collectionOperationError("remove_generation_manifest", err), target.close())
	}

	if err := target.dir.Sync(); err != nil {
		return intent.ManifestBytes, errors.Join(ErrUncertain, err, target.close())
	}

	if err := run.repository.boundary(collectionPhaseManifestUnlinked); err != nil {
		return intent.ManifestBytes, errors.Join(err, target.close())
	}

	return intent.ManifestBytes, nil
}

func (run *collectionRun) removeGenerationDirectory(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return oops.In("snapshot.store").With("operation", "remove_generation_directory").Wrap(err)
	}

	dir, err := run.repository.root.Child(generationsDirectory, false)
	if err != nil {
		return errors.Join(ErrCorrupt, err)
	}

	if err := dir.RemoveDir(id); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(collectionOperationError("remove_generation_directory", err), dir.Close())
	}

	if err := dir.Sync(); err != nil {
		return errors.Join(ErrUncertain, err, dir.Close())
	}

	return closeCollectionDirectory(dir)
}

func collectionOperationError(operation string, err error) error {
	return oops.In("snapshot.store").With("operation", operation).Wrapf(err, "collect retired generation")
}

func collectionNames(dir *privatefs.Dir, limit int) ([]string, error) {
	names, err := dir.NamesLimit(limit)
	if errors.Is(err, privatefs.ErrLimit) {
		return nil, errors.Join(errCollectionStructure, err)
	}

	if err != nil {
		return nil, errors.Join(errCollectionUnsafe, err)
	}

	return names, nil
}
