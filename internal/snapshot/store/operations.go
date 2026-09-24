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

func (r *Repository) Pin(ctx context.Context, source string, target model.Bytes, actions []Action) (Operation, error) {
	op, err := r.registerOperation(ctx, newID(), source, target, actions)
	return op, repositoryError("pin", err)
}

func NewOperationID() string {
	return newID()
}

func (r *Repository) Transition(ctx context.Context, operation, action string, expected, next model.ActionState, runtimeID model.Bytes) error {
	err := r.update(ctx, func(c *Catalog) error {
		index := slices.IndexFunc(c.Operations, func(op Operation) bool { return op.ID == operation })
		if index < 0 || c.Operations[index].State != "active" {
			return ErrConflict
		}

		actions := c.Operations[index].Actions

		actionIndex := slices.IndexFunc(actions, func(a Action) bool { return a.ID == action })
		if actionIndex < 0 || actions[actionIndex].State != expected || !expected.CanBecome(next) {
			return ErrConflict
		}

		actions[actionIndex].State, actions[actionIndex].RuntimeID = next, bytes.Clone(runtimeID)

		return nil
	})

	return repositoryError("transition", err)
}

func (r *Repository) Finish(ctx context.Context, operation string, abandon bool) error {
	err := r.update(ctx, func(c *Catalog) error {
		index := slices.IndexFunc(c.Operations, func(op Operation) bool { return op.ID == operation })
		if index < 0 || c.Operations[index].State != "active" {
			return ErrConflict
		}

		if abandon {
			c.Operations[index].State = "abandoned"
			return nil
		}

		for _, action := range c.Operations[index].Actions {
			if action.State != model.ActionStateVerified {
				return ErrConflict
			}
		}

		c.Operations[index].State = "complete"

		return nil
	})

	return repositoryError("finish", err)
}

func (r *Repository) ReconcileOperation(ctx context.Context, id string) (Operation, error) {
	lock, err := r.root.Lock(ctx, "repository.lock")
	if err != nil {
		return Operation{}, repositoryError("reconcile_operation", err)
	}

	op, readErr := r.reconcileOperationLocked(ctx, id)
	if err := errors.Join(readErr, lock.Close()); err != nil {
		return Operation{}, repositoryError("reconcile_operation", err)
	}

	return op, nil
}

func (r *Repository) reconcileOperationLocked(ctx context.Context, id string) (Operation, error) {
	catalog, err := r.catalog()
	if err != nil {
		return Operation{}, err
	}

	if err := ctx.Err(); err != nil {
		return Operation{}, oops.In("snapshot.store").With("operation", "read_operation_journal").Wrapf(err, "read operation journal")
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

	index := slices.IndexFunc(catalog.Operations, func(op Operation) bool { return op.ID == id })
	if index < 0 {
		return Operation{}, ErrUnavailable
	}

	return cloneOperation(catalog.Operations[index]), nil
}

func validateActions(actions []Action) error {
	if len(actions) > model.MaxEntities {
		return ErrCorrupt
	}

	seen := make(map[string]bool)
	for _, action := range actions {
		if !model.ValidID(action.ID) || seen[action.ID] {
			return ErrCorrupt
		}

		if !action.Kind.Valid() {
			return ErrCorrupt
		}

		if !action.State.Valid() {
			return ErrCorrupt
		}

		seen[action.ID] = true
	}

	return nil
}
