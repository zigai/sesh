//go:build linux || darwin

package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/snapshot/model"
)

// RetentionPolicy always keeps the five newest generations by sequence, the current generation and
// every active restore's source, and never alters the latest save attempt.
type RetentionPolicy struct {
	MaxAge   time.Duration
	MaxCount int
	MaxBytes int64
}

type RetentionResult struct {
	Removed        []string
	Retained       []string
	Protected      []string
	RetainedBytes  int64
	ProtectedBytes int64
	BudgetUnmet    bool
}

func (p RetentionPolicy) Validate(now time.Time) error {
	if p.MaxCount < 0 {
		return ErrConflict
	}

	if p.MaxCount > 0 && p.MaxCount < minimumRetained {
		return ErrConflict
	}

	if p.MaxAge < 0 {
		return ErrConflict
	}

	if p.MaxAge > 0 && now.IsZero() {
		return ErrConflict
	}

	if p.MaxBytes < 0 {
		return ErrConflict
	}

	return nil
}

func (r *Repository) RetainPolicy(ctx context.Context, now time.Time, policy RetentionPolicy) (RetentionResult, error) {
	if err := policy.Validate(now); err != nil {
		return RetentionResult{}, repositoryError("retain", err)
	}

	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return RetentionResult{}, repositoryError("retain", oops.In("snapshot.store").With("operation", "lock_catalog_update").Wrapf(err, "lock catalog update"))
	}

	result, retainErr := r.retainPolicyLocked(ctx, now, policy)
	if retainErr == nil {
		retainErr = r.compactHistory(ctx)
	}

	closeErr := lock.Close()

	if err := errors.Join(retainErr, closeErr); err != nil {
		return RetentionResult{}, repositoryError("retain", err)
	}

	return result, nil
}

func (r *Repository) retainPolicyLocked(ctx context.Context, now time.Time, policy RetentionPolicy) (RetentionResult, error) {
	if err := ctx.Err(); err != nil {
		return RetentionResult{}, oops.In("snapshot.store").With("operation", "evaluate_retention").Wrap(err)
	}

	catalog, err := r.catalog()
	if err != nil {
		return RetentionResult{}, err
	}

	if err := ctx.Err(); err != nil {
		return RetentionResult{}, oops.In("snapshot.store").With("operation", "evaluate_retention").Wrap(err)
	}

	if len(catalog.Generations) == 0 {
		return RetentionResult{
			Removed:        nil,
			Retained:       nil,
			Protected:      nil,
			RetainedBytes:  0,
			ProtectedBytes: 0,
			BudgetUnmet:    false,
		}, nil
	}

	sizes, timestamps, retired, loadErr := r.loadRetentionMetadata(ctx, catalog.Generations, policy)
	if loadErr != nil {
		return RetentionResult{}, loadErr
	}

	result, evalErr := catalog.evaluateRetention(now, policy, timestamps, sizes, retired)
	if evalErr != nil {
		return RetentionResult{}, evalErr
	}

	if len(result.Removed) == 0 {
		return result, nil
	}

	catalog.Generations = slices.DeleteFunc(catalog.Generations, func(g Generation) bool {
		return slices.Contains(result.Removed, g.ID)
	})

	if err := ctx.Err(); err != nil {
		return RetentionResult{}, oops.In("snapshot.store").With("operation", "publish_catalog").Wrapf(err, "publish catalog")
	}

	if err := r.publish(ctx, catalog, nil); err != nil {
		return RetentionResult{}, err
	}

	logRetiredRemovals(ctx, result.Removed, retired)

	return result, nil
}

func logRetiredRemovals(ctx context.Context, removed []string, retired map[string]bool) {
	ids := slices.DeleteFunc(slices.Clone(removed), func(id string) bool { return !retired[id] })
	if len(ids) == 0 {
		return
	}

	zerolog.Ctx(ctx).Info().
		Str("component", "snapshot").
		Str("operation", "prune").
		Strs("generations", ids).
		Msg("removed snapshot generations with an unsupported manifest schema")
}

func (r *Repository) loadRetentionMetadata(
	ctx context.Context,
	gens []Generation,
	policy RetentionPolicy,
) (map[string]int64, map[string]int64, map[string]bool, error) {
	if policy.MaxBytes > 0 {
		return r.loadRetentionBytes(ctx, gens)
	}

	if policy.MaxAge > 0 {
		timestamps, retired, err := r.loadRetentionTimestamps(ctx, gens)
		return nil, timestamps, retired, err
	}

	return nil, nil, nil, nil
}

func (r *Repository) loadRetentionTimestamps(ctx context.Context, gens []Generation) (map[string]int64, map[string]bool, error) {
	timestamps := make(map[string]int64, len(gens))
	retired := make(map[string]bool)

	for _, gen := range gens {
		if err := ctx.Err(); err != nil {
			return nil, nil, oops.In("snapshot.store").With("operation", "load_retention_timestamps").Wrap(err)
		}

		finished, err := r.readGenerationFinished(gen)
		if errors.Is(err, model.ErrVersion) {
			retired[gen.ID] = true
			continue
		}

		if err != nil {
			return nil, nil, err
		}

		timestamps[gen.ID] = finished
	}

	return timestamps, retired, nil
}

func (c Catalog) orderedWithProtection(retired map[string]bool) ([]Generation, map[string]bool) {
	ordered := slices.Clone(c.Generations)
	slices.SortFunc(ordered, func(a, b Generation) int {
		return cmp.Compare(b.Sequence, a.Sequence)
	})

	protectedMap := c.protected()
	usable := 0

	for _, gen := range ordered {
		if usable == minimumRetained {
			break
		}

		if !retired[gen.ID] {
			protectedMap[gen.ID] = true
			usable++
		}
	}

	return ordered, protectedMap
}

func (c Catalog) evaluateRetention(
	now time.Time,
	policy RetentionPolicy,
	timestamps map[string]int64,
	sizes map[string]int64,
	retired map[string]bool,
) (RetentionResult, error) {
	ordered, protectedMap := c.orderedWithProtection(retired)

	removedSet := c.evaluateAgeCountRemovals(ordered, protectedMap, policy, now, timestamps, retired)

	retainedBytes, protectedBytes, budgetUnmet, err := evaluateByteBudget(ordered, removedSet, protectedMap, policy.MaxBytes, sizes, retired)
	if err != nil {
		return RetentionResult{}, err
	}

	removed, retained, protected := assembleRetentionSlices(ordered, removedSet, protectedMap)

	return RetentionResult{
		Removed:        removed,
		Retained:       retained,
		Protected:      protected,
		RetainedBytes:  retainedBytes,
		ProtectedBytes: protectedBytes,
		BudgetUnmet:    budgetUnmet,
	}, nil
}

func (c Catalog) evaluateAgeCountRemovals(
	ordered []Generation,
	protectedMap map[string]bool,
	policy RetentionPolicy,
	now time.Time,
	timestamps map[string]int64,
	retired map[string]bool,
) map[string]bool {
	var cutoff time.Time
	if policy.MaxAge > 0 {
		cutoff = now.UTC().Add(-policy.MaxAge)
	}

	removedSet := make(map[string]bool)
	index := 0

	for _, gen := range ordered {
		if retired[gen.ID] {
			if !protectedMap[gen.ID] {
				removedSet[gen.ID] = true
			}

			continue
		}

		if !protectedMap[gen.ID] && shouldPrune(index, gen.ID, policy, cutoff, timestamps) {
			removedSet[gen.ID] = true
		}

		index++
	}

	return removedSet
}

func evaluateByteBudget(
	ordered []Generation,
	removedSet map[string]bool,
	protectedMap map[string]bool,
	maxBytes int64,
	sizes map[string]int64,
	retired map[string]bool,
) (int64, int64, bool, error) {
	if maxBytes <= 0 {
		return 0, 0, false, nil
	}

	retainedBytes, protectedBytes, err := calculateCandidateBytes(ordered, removedSet, protectedMap, sizes, retired)
	if err != nil {
		return 0, 0, false, err
	}

	if retainedBytes <= maxBytes {
		return retainedBytes, protectedBytes, false, nil
	}

	for _, gen := range slices.Backward(ordered) {
		if removedSet[gen.ID] || protectedMap[gen.ID] {
			continue
		}

		size, ok := sizes[gen.ID]
		if !ok || size <= 0 {
			return 0, 0, false, ErrCorrupt
		}

		removedSet[gen.ID] = true
		retainedBytes -= size

		if retainedBytes <= maxBytes {
			break
		}
	}

	return retainedBytes, protectedBytes, retainedBytes > maxBytes, nil
}

func calculateCandidateBytes(
	ordered []Generation,
	removedSet map[string]bool,
	protectedMap map[string]bool,
	sizes map[string]int64,
	retired map[string]bool,
) (int64, int64, error) {
	var (
		retainedBytes  int64
		protectedBytes int64
	)

	for _, gen := range ordered {
		// A kept retired generation is catalog-protected and its size is unknown, so it stays out of the budget.
		if removedSet[gen.ID] || retired[gen.ID] {
			continue
		}

		size, ok := sizes[gen.ID]
		if !ok || size <= 0 {
			return 0, 0, ErrCorrupt
		}

		var addErr error

		retainedBytes, addErr = safeAdd(retainedBytes, size)
		if addErr != nil {
			return 0, 0, addErr
		}

		if protectedMap[gen.ID] {
			protectedBytes, addErr = safeAdd(protectedBytes, size)
			if addErr != nil {
				return 0, 0, addErr
			}
		}
	}

	return retainedBytes, protectedBytes, nil
}

func assembleRetentionSlices(
	ordered []Generation,
	removedSet map[string]bool,
	protectedMap map[string]bool,
) ([]string, []string, []string) {
	var (
		removed   []string
		retained  []string
		protected []string
	)

	for _, gen := range ordered {
		if removedSet[gen.ID] {
			removed = append(removed, gen.ID)
		} else {
			retained = append(retained, gen.ID)
			if protectedMap[gen.ID] {
				protected = append(protected, gen.ID)
			}
		}
	}

	return removed, retained, protected
}

func shouldPrune(index int, id string, policy RetentionPolicy, cutoff time.Time, timestamps map[string]int64) bool {
	if policy.MaxCount > 0 && index >= policy.MaxCount {
		return true
	}

	if policy.MaxAge > 0 {
		finishedNano := timestamps[id]
		manifestTime := time.Unix(0, finishedNano).UTC()

		return manifestTime.Before(cutoff)
	}

	return false
}

func (r *Repository) readGenerationFinished(generation Generation) (int64, error) {
	data, err := r.readManifestBytes(generation.ID)
	if err != nil {
		return 0, err
	}

	if digest(data) != generation.ManifestSHA256 {
		return 0, ErrCorrupt
	}

	manifest, decodeErr := model.Decode(data)
	if decodeErr != nil {
		return 0, errors.Join(ErrCorrupt, decodeErr)
	}

	if err := validateManifestMetadata(manifest, generation); err != nil {
		return 0, err
	}

	return manifest.Finished, nil
}

func (r *Repository) readManifestBytes(generationID string) ([]byte, error) {
	parent, err := r.root.Child("generations", false)
	if err != nil {
		return nil, errors.Join(ErrCorrupt, err)
	}

	dir, openErr := parent.Child(generationID, false)
	closeParentErr := parent.Close()

	if openErr != nil {
		return nil, errors.Join(ErrCorrupt, openErr, closeParentErr)
	}

	data, readErr := dir.Read("manifest.json", model.MaxManifestBytes)
	closeDirErr := dir.Close()

	if err := errors.Join(readErr, closeDirErr, closeParentErr); err != nil {
		return nil, errors.Join(ErrCorrupt, err)
	}

	return data, nil
}

func validateManifestMetadata(manifest model.Manifest, generation Generation) error {
	if manifest.Started <= 0 || manifest.Finished < manifest.Started {
		return ErrCorrupt
	}

	if manifest.Complete() != generation.Complete {
		return ErrCorrupt
	}

	return nil
}
