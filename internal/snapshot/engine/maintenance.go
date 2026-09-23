//go:build linux || darwin

package engine

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/snapshot/store"
)

var (
	ErrRetentionBudgetUnmet  = errors.New("protected snapshot bytes exceed retention budget")
	ErrCollectionQuarantined = errors.New("snapshot collection quarantined contradictory evidence")
)

type maintenanceError struct{ error }

func (e *maintenanceError) Unwrap() error       { return e.error }
func (e *maintenanceError) OutcomeLogged() bool { return true }

func Prune(
	ctx context.Context,
	repository *store.Repository,
	now time.Time,
	policy store.RetentionPolicy,
) (store.RetentionResult, error) {
	zerolog.Ctx(ctx).Info().
		Str("component", "snapshot").
		Str("operation", "prune").
		Str("status", "started").
		Msg("snapshot prune started")

	if repository == nil {
		return store.RetentionResult{}, maintenanceFailure(ctx, "prune", "admission", ErrRepositoryRequired)
	}

	result, err := repository.RetainPolicy(ctx, now, policy)
	if err != nil {
		return result, maintenanceFailure(ctx, "prune", "retention", err)
	}

	if result.BudgetUnmet {
		zerolog.Ctx(ctx).Warn().
			Str("component", "snapshot").
			Str("operation", "prune").
			Str("status", "degraded").
			Str("reason", "protected_overage").
			Msg("snapshot prune finished with protected overage")

		return result, &maintenanceError{error: oops.In("snapshot").
			Code("retention_budget_unmet").
			With("operation", "prune", "phase", "retention", "reason", "protected_overage").
			Wrap(ErrRetentionBudgetUnmet)}
	}

	zerolog.Ctx(ctx).Info().
		Str("component", "snapshot").
		Str("operation", "prune").
		Str("status", "ok").
		Msg("snapshot prune finished")

	return result, nil
}

func GarbageCollect(
	ctx context.Context,
	repository *store.Repository,
	budget store.CollectionBudget,
) (store.CollectionResult, error) {
	zerolog.Ctx(ctx).Info().
		Str("component", "snapshot").
		Str("operation", "gc").
		Str("status", "started").
		Msg("snapshot collection started")

	if repository == nil {
		return store.CollectionResult{}, maintenanceFailure(ctx, "gc", "admission", ErrRepositoryRequired)
	}

	result, err := repository.Collect(ctx, budget)
	if err != nil {
		return result, maintenanceFailure(ctx, "gc", "collection", err)
	}

	return collectionOutcome(ctx, result)
}

func collectionOutcome(ctx context.Context, result store.CollectionResult) (store.CollectionResult, error) {
	if len(result.Quarantined) > 0 {
		zerolog.Ctx(ctx).Warn().
			Str("component", "snapshot").
			Str("operation", "gc").
			Str("status", "degraded").
			Str("reason", "quarantined").
			Msg("snapshot collection finished with quarantined evidence")

		return result, &maintenanceError{error: oops.In("snapshot").
			Code("collection_quarantined").
			With("operation", "gc", "phase", "collection", "reason", "quarantined").
			Wrap(ErrCollectionQuarantined)}
	}

	zerolog.Ctx(ctx).Info().
		Str("component", "snapshot").
		Str("operation", "gc").
		Str("status", "ok").
		Msg("snapshot collection finished")

	return result, nil
}

func maintenanceFailure(ctx context.Context, operation, phase string, err error) error {
	reason := "repository_failed"

	switch {
	case errors.Is(err, store.ErrUncertain):
		reason = "publication_uncertain"
	case errors.Is(err, context.Canceled):
		reason = "operation_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "operation_timed_out"
	case errors.Is(err, store.ErrCorrupt):
		reason = "repository_corrupt"
	case errors.Is(err, store.ErrConflict):
		reason = "repository_conflict"
	}

	zerolog.Ctx(ctx).Error().
		Str("component", "snapshot").
		Str("operation", operation).
		Str("status", "failed").
		Str("phase", phase).
		Str("reason", reason).
		Msg("snapshot maintenance failed")

	return &maintenanceError{error: oops.In("snapshot").Code(reason).
		With("operation", operation, "phase", phase).
		Wrap(err)}
}
