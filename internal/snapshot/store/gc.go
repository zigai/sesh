//go:build linux || darwin

package store

import (
	"cmp"
	"context"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	collectionVersion        = 1
	collectionDirectory      = "gc"
	collectionRecordSuffix   = ".json"
	collectionScratchPrefix  = ".gc-scratch-"
	generationsDirectory     = "generations"
	collectionManifestName   = "manifest.json"
	collectionArtifactsName  = "artifacts"
	collectionArtifactSuffix = ".bin.gz"
	maxCollectionEntries     = 2 * maxEntries
	maxCollectionRecord      = 4 << 20
)

const (
	collectionStateDeleting    = "deleting"
	collectionStateRemoved     = "removed"
	collectionStateQuarantined = "quarantined"
)

const (
	attemptStateCommitted  = "committed"
	attemptStateIncomplete = "incomplete"
	attemptStatePending    = "pending"
	attemptStateFailed     = "failed"
)

const (
	collectionPhaseCatalogConfirmed   = "gc_catalog_confirmed"
	collectionPhaseIntentSynced       = "gc_intent_synced"
	collectionPhaseArtifactUnlinked   = "gc_artifact_unlinked"
	collectionPhaseArtifactsUnlinked  = "gc_artifacts_unlinked"
	collectionPhaseManifestUnlinked   = "gc_manifest_unlinked"
	collectionPhaseGenerationUnlinked = "gc_generation_unlinked"
	collectionPhaseOutcomeSynced      = "gc_outcome_synced"
)

const (
	CollectionReasonMember           = "catalog_member"
	CollectionReasonPending          = "pending_attempt"
	CollectionReasonFailed           = "failed_attempt"
	CollectionReasonNoEvidence       = "no_attempt_evidence"
	CollectionReasonUnrecognized     = "unrecognized_entry"
	CollectionReasonUnsafe           = "unsafe_entry"
	CollectionReasonUnknownContent   = "unknown_content"
	CollectionReasonInvalidContent   = "invalid_content"
	CollectionReasonCollected        = "collected"
	CollectionReasonQuarantined      = "quarantined"
	CollectionReasonUnreadableIntent = "unreadable_intent"
	CollectionQuarantineEvidence     = "evidence_changed"
	CollectionQuarantineContent      = "content_changed"
)

type CollectionBudget struct {
	Generations int
	Bytes       int64
}

type CollectionSkip struct {
	Generation string
	Reason     string
}

type CollectionResult struct {
	Intents     []string
	Resumed     bool
	Removed     []string
	Deferred    []string
	Preserved   []CollectionSkip
	Quarantined []CollectionSkip
	Bytes       int64
}

type CollectionFile struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type CollectionIntent struct {
	Version        int              `json:"version"`
	Generation     string           `json:"generation"`
	Sequence       uint64           `json:"sequence"`
	State          string           `json:"state"`
	Reason         string           `json:"reason"`
	ManifestSHA256 string           `json:"manifestSha256"`
	ManifestBytes  int64            `json:"manifestBytes"`
	Files          []CollectionFile `json:"files"`
	Bytes          int64            `json:"bytes"`
}

type collectionCandidate struct {
	id             string
	sequence       uint64
	manifestSHA256 string
	present        bool
}

type collectionBudget struct {
	generations int
	bytes       int64
}

type collectionRun struct {
	repository *Repository
	records    *privatefs.Dir
	budget     collectionBudget
	result     CollectionResult
}

func (b CollectionBudget) Validate() error {
	if b.Generations < 1 || b.Generations > maxEntries {
		return ErrConflict
	}

	if b.Bytes < 1 {
		return ErrConflict
	}

	return nil
}

func (i CollectionIntent) validate() error {
	if i.Version != collectionVersion || i.Sequence == 0 || !validID(i.Generation) || !validDigest(i.ManifestSHA256) {
		return ErrCorrupt
	}

	if i.ManifestBytes <= 0 || i.ManifestBytes > model.MaxManifestBytes || len(i.Files) > model.MaxEntities {
		return ErrCorrupt
	}

	if !validCollectionIntentState(i.State, i.Reason) {
		return ErrCorrupt
	}

	files, err := collectionFileBytes(i.Files)
	if err != nil {
		return err
	}

	total, err := safeAdd(files, i.ManifestBytes)
	if err != nil || total != i.Bytes {
		return ErrCorrupt
	}

	return nil
}

func validCollectionIntentState(state, reason string) bool {
	switch state {
	case collectionStateDeleting, collectionStateRemoved:
		return reason == ""
	case collectionStateQuarantined:
		return reason == CollectionQuarantineEvidence || reason == CollectionQuarantineContent
	default:
		return false
	}
}

func collectionFileBytes(files []CollectionFile) (int64, error) {
	seen := make(map[string]bool, len(files))

	var total int64

	for _, file := range files {
		if !validCollectionFileName(file.Name) || seen[file.Name] || file.Bytes <= 0 || file.Bytes > maxCompressedBytes {
			return 0, ErrCorrupt
		}

		seen[file.Name] = true

		sum, err := safeAdd(total, file.Bytes)
		if err != nil {
			return 0, ErrCorrupt
		}

		total = sum
	}

	return total, nil
}

func validCollectionFileName(name string) bool {
	id, ok := strings.CutSuffix(name, collectionArtifactSuffix)

	return ok && model.ValidID(id)
}

func (r *Repository) Collect(ctx context.Context, budget CollectionBudget) (CollectionResult, error) {
	if err := budget.Validate(); err != nil {
		return CollectionResult{}, repositoryError("collect", err)
	}

	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return CollectionResult{}, repositoryError("collect", oops.In("snapshot.store").With("operation", "lock_generation_collection").Wrapf(err, "lock generation collection"))
	}

	result, collectErr := r.collectLocked(ctx, budget)

	return result, repositoryError("collect", errors.Join(collectErr, lock.Close()))
}

func (r *Repository) InspectCollection(ctx context.Context, generation string) (CollectionIntent, error) {
	if !validID(generation) {
		return CollectionIntent{}, repositoryError("inspect_collection", ErrCorrupt)
	}

	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return CollectionIntent{}, repositoryError("inspect_collection", oops.In("snapshot.store").With("operation", "lock_collection_inspection").Wrapf(err, "lock collection inspection"))
	}

	intent, inspectErr := r.inspectCollectionLocked(ctx, generation)

	return intent, repositoryError("inspect_collection", errors.Join(inspectErr, lock.Close()))
}

func (r *Repository) inspectCollectionLocked(ctx context.Context, generation string) (CollectionIntent, error) {
	records, err := r.openCollectionDirectory(false)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return CollectionIntent{}, err
		}

		if syncErr := r.root.Sync(); syncErr != nil {
			return CollectionIntent{}, errors.Join(ErrUncertain, syncErr, ctx.Err())
		}

		return CollectionIntent{}, ErrUnavailable
	}

	record := readCollectionRecord(records, generation)
	syncErr := records.Sync()
	closeErr := records.Close()

	if err := errors.Join(syncErr, closeErr); err != nil {
		return CollectionIntent{}, errors.Join(ErrUncertain, err, ctx.Err())
	}

	if err := ctx.Err(); err != nil {
		return CollectionIntent{}, errors.Join(ErrUncertain, err)
	}

	if record.reason != "" {
		return CollectionIntent{}, errors.Join(ErrCorrupt, record.cause)
	}

	if !record.found {
		return CollectionIntent{}, ErrUnavailable
	}

	return record.intent, nil
}

func (r *Repository) collectLocked(ctx context.Context, budget CollectionBudget) (CollectionResult, error) {
	catalog, err := r.catalog()
	if err != nil {
		return CollectionResult{}, err
	}

	if err := ctx.Err(); err != nil {
		return CollectionResult{}, oops.In("snapshot.store").With("operation", "collect_generations").Wrap(err)
	}

	names, err := r.generationNames()
	if err != nil {
		return CollectionResult{}, err
	}

	candidates, preserved := classifyCollection(catalog, names)
	if len(candidates) == 0 {
		return preservedCollectionResult(preserved), nil
	}

	if err := r.confirmCollectionCatalog(); err != nil {
		return preservedCollectionResult(preserved), err
	}

	records, err := r.openCollectionDirectory(false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return preservedCollectionResult(preserved), err
	}

	run := collectionRun{
		repository: r,
		records:    records,
		budget:     collectionBudget{generations: budget.Generations, bytes: budget.Bytes},
		result:     preservedCollectionResult(preserved),
	}

	if err := run.purgeAll(ctx, candidates); err != nil {
		return run.result, errors.Join(err, closeCollectionDirectory(run.records))
	}

	return run.result, closeCollectionDirectory(run.records)
}

func (r *Repository) confirmCollectionCatalog() error {
	if err := r.boundary(collectionPhaseCatalogConfirmed); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	if err := r.root.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	return nil
}

func (r *Repository) generationNames() ([]string, error) {
	dir, err := r.root.Child(generationsDirectory, false)
	if err != nil {
		return nil, errors.Join(ErrCorrupt, err)
	}

	names, readErr := dir.NamesLimit(maxCollectionEntries)
	if err := errors.Join(readErr, dir.Close()); err != nil {
		return nil, err
	}

	return names, nil
}

func classifyCollection(catalog Catalog, names []string) ([]collectionCandidate, []CollectionSkip) {
	candidates := make([]collectionCandidate, 0, len(names))
	preserved := make([]CollectionSkip, 0)
	present := make(map[string]bool, len(names))

	for _, name := range slices.Sorted(slices.Values(names)) {
		present[name] = true

		attempt, block := catalog.collectionBlock(name)
		if block == CollectionReasonMember {
			continue
		}

		if block != "" {
			preserved = append(preserved, CollectionSkip{Generation: name, Reason: block})

			continue
		}

		candidates = append(candidates, collectionCandidate{id: name, sequence: attempt.Sequence, manifestSHA256: attempt.ManifestSHA256, present: true})
	}

	for _, attempt := range catalog.Attempts {
		if present[attempt.ID] || !collectionRetiredState(attempt.State) || catalog.generationIndex(attempt.ID) >= 0 {
			continue
		}

		candidates = append(candidates, collectionCandidate{id: attempt.ID, sequence: attempt.Sequence, manifestSHA256: attempt.ManifestSHA256, present: false})
	}

	slices.SortFunc(candidates, func(a, b collectionCandidate) int {
		return cmp.Compare(a.sequence, b.sequence)
	})

	return candidates, preserved
}

func (c Catalog) collectionBlock(id string) (Attempt, string) {
	var absent Attempt

	if !validID(id) {
		return absent, CollectionReasonUnrecognized
	}

	if c.generationIndex(id) >= 0 {
		return absent, CollectionReasonMember
	}

	index := c.attemptIndex(id)
	if index < 0 {
		return absent, CollectionReasonNoEvidence
	}

	switch c.Attempts[index].State {
	case attemptStateCommitted, attemptStateIncomplete:
		return c.Attempts[index], ""
	case attemptStatePending:
		return absent, CollectionReasonPending
	case attemptStateFailed:
		return absent, CollectionReasonFailed
	default:
		return absent, CollectionReasonNoEvidence
	}
}

func collectionRetiredState(state string) bool {
	return state == attemptStateCommitted || state == attemptStateIncomplete
}

func preservedCollectionResult(preserved []CollectionSkip) CollectionResult {
	var result CollectionResult

	result.Preserved = preserved

	return result
}

func (b *collectionBudget) depleted() bool {
	return b.generations < 1 || b.bytes < 1
}

func (run *collectionRun) purgeAll(ctx context.Context, candidates []collectionCandidate) error {
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return oops.In("snapshot.store").With("operation", "collect_generations").Wrap(err)
		}

		if err := run.purgeCandidate(ctx, candidate); err != nil {
			return err
		}
	}

	return nil
}

func (run *collectionRun) purgeCandidate(ctx context.Context, candidate collectionCandidate) error {
	record, work := run.authorizedWork(candidate)
	if !work {
		return nil
	}

	if record.found && (record.intent.Sequence != candidate.sequence || record.intent.ManifestSHA256 != candidate.manifestSHA256) {
		return run.quarantine(record.intent, CollectionQuarantineEvidence)
	}

	if !record.found && !candidate.present {
		return nil
	}

	if run.budget.depleted() {
		run.deferCandidate(candidate.id)

		return nil
	}

	if record.found {
		if err := run.resumeIntent(record.intent); err != nil {
			return err
		}

		return run.removeCandidate(ctx, candidate.id, record.intent)
	}

	intent, started, err := run.startCandidate(ctx, candidate)
	if err != nil {
		return err
	}

	if !started {
		return nil
	}

	return run.removeCandidate(ctx, candidate.id, intent)
}

func (run *collectionRun) authorizedWork(candidate collectionCandidate) (collectionRecord, bool) {
	record := readCollectionRecord(run.records, candidate.id)
	if record.reason != "" {
		run.preserve(candidate.id, record.reason)

		return record, false
	}

	if record.found && record.intent.State != collectionStateDeleting {
		if candidate.present {
			run.preserve(candidate.id, resolvedCollectionReason(record.intent.State))
		}

		return record, false
	}

	return record, true
}

func (run *collectionRun) startCandidate(ctx context.Context, candidate collectionCandidate) (CollectionIntent, bool, error) {
	inspected, err := run.inspectCandidate(ctx, candidate)
	if err != nil {
		return inspected, false, run.refuseInspection(ctx, candidate.id, err)
	}

	if inspected.Bytes > run.budget.bytes {
		run.deferCandidate(candidate.id)

		return inspected, false, nil
	}

	if err := run.recordIntent(inspected); err != nil {
		return inspected, false, err
	}

	return inspected, true, nil
}

func (run *collectionRun) removeCandidate(ctx context.Context, id string, intent CollectionIntent) error {
	target, err := run.verifyTarget(ctx, intent)
	if err != nil {
		return run.quarantineContradiction(ctx, intent, err)
	}

	if target.bytes > run.budget.bytes {
		closeErr := target.close()

		run.deferCandidate(id)

		return closeErr
	}

	removed, removeErr := run.remove(ctx, intent, &target)
	run.budget.generations--
	run.budget.bytes -= removed
	run.result.Bytes += removed

	if removeErr != nil {
		return errors.Join(ErrUncertain, removeErr)
	}

	if err := run.finish(intent); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	run.result.Removed = append(run.result.Removed, intent.Generation)

	return nil
}

func (run *collectionRun) refuseInspection(ctx context.Context, id string, err error) error {
	if interrupted := interruption(ctx, err); interrupted != nil {
		return interrupted
	}

	reason, ok := collectionRefusal(err)
	if !ok {
		return err
	}

	run.preserve(id, reason)

	return nil
}

func (run *collectionRun) quarantineContradiction(ctx context.Context, intent CollectionIntent, err error) error {
	if interrupted := interruption(ctx, err); interrupted != nil {
		return interrupted
	}

	reason, ok := collectionContradiction(err)
	if !ok {
		return err
	}

	return run.quarantine(intent, reason)
}

func (run *collectionRun) recordIntent(intent CollectionIntent) error {
	records, err := run.intentDirectory()
	if err != nil {
		return err
	}

	run.result.Intents = append(run.result.Intents, intent.Generation)

	if err := run.repository.writeCollectionIntent(records, intent); err != nil {
		return err
	}

	return run.repository.boundary(collectionPhaseIntentSynced)
}

func (run *collectionRun) resumeIntent(intent CollectionIntent) error {
	run.result.Intents = append(run.result.Intents, intent.Generation)
	run.result.Resumed = true

	if err := run.records.Sync(); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	return run.repository.boundary(collectionPhaseIntentSynced)
}

func (run *collectionRun) intentDirectory() (*privatefs.Dir, error) {
	if run.records != nil {
		return run.records, nil
	}

	dir, err := run.repository.openCollectionDirectory(true)
	if err != nil {
		return nil, err
	}

	run.records = dir

	return dir, nil
}

func (run *collectionRun) quarantine(intent CollectionIntent, reason string) error {
	final := intent
	final.State, final.Reason = collectionStateQuarantined, reason

	run.result.Intents = append(run.result.Intents, intent.Generation)
	if err := run.repository.writeCollectionIntent(run.records, final); err != nil {
		return err
	}

	if err := run.repository.boundary(collectionPhaseOutcomeSynced); err != nil {
		return errors.Join(ErrUncertain, err)
	}

	run.result.Quarantined = append(run.result.Quarantined, CollectionSkip{Generation: intent.Generation, Reason: reason})

	return nil
}

func (run *collectionRun) finish(intent CollectionIntent) error {
	final := intent
	final.State, final.Reason = collectionStateRemoved, ""

	if err := run.repository.writeCollectionIntent(run.records, final); err != nil {
		return err
	}

	return run.repository.boundary(collectionPhaseOutcomeSynced)
}

func (run *collectionRun) preserve(id, reason string) {
	run.result.Preserved = append(run.result.Preserved, CollectionSkip{Generation: id, Reason: reason})
}

func (run *collectionRun) deferCandidate(id string) {
	run.result.Deferred = append(run.result.Deferred, id)
}

func resolvedCollectionReason(state string) string {
	if state == collectionStateQuarantined {
		return CollectionReasonQuarantined
	}

	return CollectionReasonCollected
}
