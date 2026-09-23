package apps

import (
	"context"
	"errors"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/snapshot/model"
)

var (
	ErrEditorEndpointMissing = errors.New("nvim server endpoint is unavailable")
	ErrEditorIdentityChanged = errors.New("nvim process identity changed")
	ErrEditorQueryFailed     = errors.New("nvim buffer query failed")
	ErrEditorResponseInvalid = errors.New("nvim buffer response is invalid")
)

func EnrichEditor(ctx context.Context, observed model.Launch, pid int) (model.Launch, Reason, error) {
	fallback := cloneLaunch(observed)
	fallback.Recovery = "unverified"

	if executableName(string(observed.Executable)) != "nvim" {
		return fallback, ReasonUnsupportedApplication, nil
	}

	files, err := editorFilesFromProcess(ctx, observed, pid)
	if err != nil {
		reason := editorEnrichmentReason(err)

		return fallback, reason, oops.In("snapshot.apps").Code(string(reason)).With("operation", "enrich_editor").Wrap(err)
	}

	if len(files) == 0 {
		return restartLaunch(observed, []string{"nvim"}), ReasonEditorRestart, nil
	}

	argv := append([]string{"nvim", "--"}, files...)

	return restartLaunch(observed, argv), ReasonEditorReopen, nil
}

func editorEnrichmentReason(err error) Reason {
	switch {
	case errors.Is(err, ErrEditorEndpointMissing):
		return ReasonEditorEndpointMissing
	case errors.Is(err, ErrEditorIdentityChanged):
		return ReasonEditorIdentityChanged
	case errors.Is(err, ErrEditorResponseInvalid):
		return ReasonEditorResponseInvalid
	default:
		return ReasonEditorQueryFailed
	}
}
