//go:build !linux

package apps

import (
	"context"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func editorFilesFromProcess(context.Context, model.Launch, int) ([]string, error) {
	return nil, ErrEditorEndpointMissing
}
