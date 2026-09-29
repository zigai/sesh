package capture

import (
	"context"
	"errors"

	"github.com/samber/oops"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/process"
	integration "github.com/zigai/sesh/internal/integration/tmux"
)

func captureError(err error) error {
	if err == nil {
		return nil
	}

	code := "capture_failed"

	switch {
	case errors.Is(err, context.Canceled):
		code = "operation_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		code = "operation_timed_out"
	case errors.Is(err, gotmux.ErrOutputLimit):
		code = "output_limit_exceeded"
	case errors.Is(err, ErrChanged):
		code = "capture_changed"
	case errors.Is(err, ErrSelection):
		code = "selection_required"
	case errors.Is(err, ErrUnsupported), errors.Is(err, process.ErrUnsupported):
		code = "capture_unsupported"
	case errors.Is(err, integration.ErrEndpoint):
		code = "endpoint_required"
	case errors.Is(err, ErrProcessDeleted):
		code = "capture_failed"
	}

	return oops.In("snapshot.capture").Code(code).With("operation", "observe").Wrap(err)
}
