//go:build linux || darwin

package store

import (
	"context"
	"errors"

	"github.com/samber/oops"
)

// PublishedNothing reports whether a failed write is known to have left the catalog unchanged.
// An uncertain publication may have committed.
func PublishedNothing(err error) bool {
	return !errors.Is(err, ErrUncertain)
}

func repositoryError(operation string, err error) error {
	if err == nil {
		return nil
	}

	code := "repository_failed"

	switch {
	case errors.Is(err, ErrUncertain):
		code = "publication_uncertain"
	case errors.Is(err, context.Canceled):
		code = "operation_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		code = "operation_timed_out"
	case errors.Is(err, ErrCorrupt):
		code = "repository_corrupt"
	case errors.Is(err, ErrUnavailable):
		code = "snapshot_unavailable"
	case errors.Is(err, ErrConflict):
		code = "repository_conflict"
	case errors.Is(err, ErrRecoveryRequired):
		code = "recovery_required"
	}

	return oops.In("snapshot.store").Code(code).With("operation", operation).Wrap(err)
}
