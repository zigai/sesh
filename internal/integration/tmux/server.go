package tmux

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/samber/oops"

	gotmux "github.com/zigai/gotmux/tmux"
)

const (
	LibraryVersion            = "v0.6.0"
	interactiveCommandTimeout = 24 * time.Hour

	// OutputBytes bounds stdout and stderr of one tmux command. It matches the snapshot
	// artifact limit so a pane's full terminal text fits in a single capture.
	OutputBytes = 256 << 20
)

var ErrEndpoint = errors.New("an explicit absolute tmux socket is required")

func Open(socket string) (*gotmux.Server, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return nil, oops.In("tmux").Code("endpoint_required").With("operation", "open").Wrap(ErrEndpoint)
	}

	var config gotmux.Config

	config.Binary = serverBinary(socket)
	config.SocketPath = socket
	config.ConfigFile = "/dev/null"
	config.Limits = gotmux.DefaultLimits()
	config.Limits.OutputBytes = OutputBytes

	server, err := gotmux.New(config)
	if err != nil {
		return nil, oops.In("tmux").With("operation", "open_tmux_endpoint").Wrapf(err, "open tmux endpoint")
	}

	return server, nil
}

func OpenInteractive(socket string) (*gotmux.Server, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return nil, oops.In("tmux").Code("endpoint_required").With("operation", "open_interactive").Wrap(ErrEndpoint)
	}

	var config gotmux.Config

	config.Binary = serverBinary(socket)
	config.SocketPath = socket
	config.ConfigFile = "/dev/null"
	config.Limits = gotmux.DefaultLimits()
	config.Limits.CommandTimeout = interactiveCommandTimeout

	server, err := gotmux.New(config)
	if err != nil {
		return nil, oops.In("tmux").With("operation", "open_tmux_endpoint").Wrapf(err, "open tmux endpoint")
	}

	return server, nil
}
