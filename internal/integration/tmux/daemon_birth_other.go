//go:build !linux

package tmux

import (
	"context"

	"github.com/zigai/sesh/internal/integration/process"
)

func readDaemonBirth(context.Context, int) (process.Identity, error) {
	return process.Identity{}, process.ErrUnsupported
}
