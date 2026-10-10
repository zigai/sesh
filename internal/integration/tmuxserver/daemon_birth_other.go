//go:build !linux

package tmuxserver

import (
	"context"

	"github.com/zigai/sesh/internal/integration/process"
)

func readDaemonBirth(context.Context, int) (process.Identity, error) {
	return process.Identity{}, process.ErrUnsupported
}
