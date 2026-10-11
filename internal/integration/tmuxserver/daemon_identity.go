package tmuxserver

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/samber/oops"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/process"
)

const (
	CodeDaemonIdentityUnavailable = "daemon_identity_unavailable"
	CodeDaemonIdentityChanged     = "daemon_identity_changed"
)

var (
	ErrDaemonIdentityUnavailable = errors.New("tmux daemon identity is unavailable")
	ErrDaemonIdentityChanged     = errors.New("tmux daemon identity changed during observation")
)

type DaemonIdentity struct {
	Server gotmux.ServerIdentity
	Birth  process.Identity
}

func (i DaemonIdentity) Equal(other DaemonIdentity) bool {
	return i.Server.Equal(other.Server) && i.Birth == other.Birth
}

func ObserveDaemonIdentity(ctx context.Context, server *gotmux.Server) (DaemonIdentity, error) {
	if err := ctx.Err(); err != nil {
		return DaemonIdentity{}, daemonIdentityError(CodeDaemonIdentityUnavailable, "observe_daemon_identity", errors.Join(ErrDaemonIdentityUnavailable, err))
	}

	if server == nil {
		return DaemonIdentity{}, daemonIdentityError(CodeDaemonIdentityUnavailable, "observe_daemon_identity", ErrDaemonIdentityUnavailable)
	}

	selected := server.Endpoint()

	endpoint := selected.SocketPath
	if !filepath.IsAbs(endpoint) || filepath.Clean(endpoint) != endpoint {
		return DaemonIdentity{}, daemonIdentityError(CodeDaemonIdentityUnavailable, "observe_daemon_identity", errors.Join(ErrDaemonIdentityUnavailable, ErrEndpoint))
	}

	before, err := probeDaemonIdentity(ctx, server, selected, "probe_daemon_before_identity")
	if err != nil {
		return DaemonIdentity{}, err
	}

	beforeBirth, err := readDaemonBirth(ctx, before.PID)
	if err != nil {
		return DaemonIdentity{}, daemonIdentityError(CodeDaemonIdentityUnavailable, "read_daemon_birth_before_reprobe", errors.Join(ErrDaemonIdentityUnavailable, err))
	}

	after, err := probeDaemonIdentity(ctx, server, selected, "probe_daemon_after_identity")
	if err != nil {
		return DaemonIdentity{}, err
	}

	afterBirth, err := readDaemonBirth(ctx, after.PID)
	if err != nil {
		return DaemonIdentity{}, daemonIdentityError(CodeDaemonIdentityUnavailable, "read_daemon_birth_after_probe", errors.Join(ErrDaemonIdentityUnavailable, err))
	}

	if !before.Equal(after) || beforeBirth != afterBirth {
		return DaemonIdentity{}, daemonIdentityError(CodeDaemonIdentityChanged, "observe_daemon_identity", ErrDaemonIdentityChanged)
	}

	return DaemonIdentity{Server: after, Birth: afterBirth}, nil
}

func probeDaemonIdentity(ctx context.Context, server *gotmux.Server, endpoint gotmux.Endpoint, operation string) (gotmux.ServerIdentity, error) {
	info, err := server.Probe(ctx)
	if err != nil {
		return gotmux.ServerIdentity{}, daemonIdentityError(CodeDaemonIdentityUnavailable, operation, errors.Join(ErrDaemonIdentityUnavailable, err))
	}

	if !validDaemonServerIdentity(info.Identity, endpoint) {
		return gotmux.ServerIdentity{}, daemonIdentityError(CodeDaemonIdentityUnavailable, operation, ErrDaemonIdentityUnavailable)
	}

	return info.Identity, nil
}

func validDaemonServerIdentity(identity gotmux.ServerIdentity, endpoint gotmux.Endpoint) bool {
	return identity.PID > 0 && !identity.Started.IsZero() && identity.Endpoint == endpoint && identity.ReportedSocket == endpoint.SocketPath
}

func daemonIdentityError(code, operation string, err error) error {
	return oops.In("tmux").Code(code).With("operation", operation).Wrap(err)
}
