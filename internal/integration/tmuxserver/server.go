package tmuxserver

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/samber/oops"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	LibraryVersion            = "v0.7.4"
	interactiveCommandTimeout = 24 * time.Hour

	// OutputBytes bounds stdout and stderr of one tmux command, so a pane's full terminal text
	// fits in a single capture.
	OutputBytes = model.MaxArtifactBytes
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

// OpenNamed opens the server on the named socket with configFile, running the same tmux build as
// the server at socket so both answer to the same features.
func OpenNamed(socket, name, configFile string) (*gotmux.Server, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return nil, oops.In("tmux").Code("endpoint_required").With("operation", "open_named").Wrap(ErrEndpoint)
	}

	var config gotmux.Config

	config.Binary = serverBinary(socket)
	config.SocketName = name
	config.ConfigFile = configFile
	config.Limits = gotmux.DefaultLimits()
	config.Limits.CommandTimeout = interactiveCommandTimeout

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
