//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"errors"
	"slices"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const maxTargetBytes = 1 << 20

func (r *Repository) RegisterOperation(ctx context.Context, id, source string, target model.Bytes, actions []Action) (Operation, error) {
	op, err := r.registerOperation(ctx, id, source, target, actions)
	return op, repositoryError("register_operation", err)
}

func (r *Repository) registerOperation(ctx context.Context, id, source string, target model.Bytes, actions []Action) (Operation, error) {
	if err := validateRequestBoundsAndContent(id, source, target, actions); err != nil {
		return Operation{
			ID:      id,
			Source:  "",
			Target:  nil,
			State:   "",
			Actions: nil,
		}, err
	}

	candidate := Operation{
		ID:      id,
		Source:  source,
		Target:  bytes.Clone(target),
		State:   "active",
		Actions: cloneActions(actions),
	}

	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return candidate, oops.In("snapshot.store").With("operation", "lock_catalog_update").Wrapf(err, "lock catalog update")
	}

	op, regErr := r.registerOperationLocked(ctx, candidate)
	if regErr != nil {
		return candidate, errors.Join(regErr, lock.Close())
	}

	if closeErr := lock.Close(); closeErr != nil {
		return candidate, oops.In("snapshot.store").With("operation", "close_repository_lock").Wrapf(closeErr, "close repository lock")
	}

	return op, nil
}

func (r *Repository) registerOperationLocked(ctx context.Context, candidate Operation) (Operation, error) {
	catalog, err := r.catalog()
	if err != nil {
		return Operation{}, err
	}

	if err := ctx.Err(); err != nil {
		return Operation{}, oops.In("snapshot.store").With("operation", "register_operation").Wrap(err)
	}

	index := slices.IndexFunc(catalog.Operations, func(op Operation) bool { return op.ID == candidate.ID })
	if index >= 0 {
		return r.confirmExistingOperationLocked(ctx, catalog.Operations[index], candidate)
	}

	if len(catalog.Attempts)+len(catalog.Generations)+len(catalog.Operations)+len(catalog.Rewrites)+1 > maxEntries {
		return Operation{}, ErrCorrupt
	}

	genIndex := catalog.generationIndex(candidate.Source)
	if genIndex < 0 {
		return Operation{}, ErrUnavailable
	}

	for _, rewrite := range catalog.Rewrites {
		if rewrite.State == rewriteStatePending && rewrite.Source == candidate.Source {
			return Operation{}, ErrConflict
		}
	}

	if _, err := r.readGeneration(ctx, catalog.Generations[genIndex]); err != nil {
		return Operation{}, err
	}

	if err := ctx.Err(); err != nil {
		return Operation{}, oops.In("snapshot.store").With("operation", "publish_catalog").Wrap(err)
	}

	catalog.Operations = append(catalog.Operations, candidate)

	if err := r.publish(ctx, catalog, nil); err != nil {
		return Operation{}, err
	}

	return candidate, nil
}

func (r *Repository) confirmExistingOperationLocked(ctx context.Context, existing, candidate Operation) (Operation, error) {
	if !sameIntent(existing, candidate) {
		return Operation{}, ErrConflict
	}

	if err := r.boundary("operation_reconcile_sync"); err != nil {
		return Operation{}, errors.Join(ErrUncertain, err, ctx.Err())
	}

	if err := ctx.Err(); err != nil {
		return Operation{}, errors.Join(ErrUncertain, err)
	}

	if err := r.root.Sync(); err != nil {
		return Operation{}, errors.Join(ErrUncertain, err, ctx.Err())
	}

	return cloneOperation(existing), nil
}

func sameIntent(existing, candidate Operation) bool {
	if existing.Source != candidate.Source {
		return false
	}

	if !bytes.Equal(existing.Target, candidate.Target) {
		return false
	}

	if len(existing.Actions) != len(candidate.Actions) {
		return false
	}

	for i := range candidate.Actions {
		if existing.Actions[i].ID != candidate.Actions[i].ID {
			return false
		}

		if existing.Actions[i].Kind != candidate.Actions[i].Kind {
			return false
		}
	}

	return true
}

func validateRequestBoundsAndContent(id, source string, target model.Bytes, actions []Action) error {
	if !validID(id) || !validID(source) || len(target) == 0 || len(target) > maxTargetBytes {
		return ErrCorrupt
	}

	if len(actions) > model.MaxEntities {
		return ErrCorrupt
	}

	for _, action := range actions {
		if len(action.RuntimeID) != 0 || action.State != "planned" {
			return ErrConflict
		}
	}

	return validateActions(actions)
}

func cloneOperation(op Operation) Operation {
	clone := op
	clone.Target = bytes.Clone(op.Target)
	clone.Actions = cloneActions(op.Actions)

	return clone
}

func cloneActions(actions []Action) []Action {
	if actions == nil {
		return nil
	}

	res := slices.Clone(actions)
	for i := range res {
		res[i].RuntimeID = bytes.Clone(res[i].RuntimeID)
	}

	return res
}
