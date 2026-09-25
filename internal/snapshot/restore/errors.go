package restore

import (
	"errors"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/snapshot/model"
)

var (
	ErrInvalid            = model.ErrInvalid
	ErrVersion            = model.ErrVersion
	ErrGroupCollision     = errors.New("group collision")
	ErrSessionCollision   = errors.New("session collision")
	ErrWorkingDirNotFound = errors.New("working directory not found")
	ErrExecutionFailed    = errors.New("execution failed")
)

func planFailure(phase, reason string, cause error) error {
	builder := oops.In("snapshot.restore").
		Code("snapshot_invalid").
		With("operation", "plan", "phase", phase)
	if reason != "" {
		builder = builder.With("reason", reason)
	}

	return builder.Wrap(cause)
}

func versionFailure(phase string, cause error) error {
	return oops.In("snapshot.restore").
		Code("snapshot_version_unsupported").
		With("operation", "plan", "phase", phase).
		Wrap(cause)
}
