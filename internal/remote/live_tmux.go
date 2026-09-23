//go:build linux || darwin

package remote

import (
	"context"
	"errors"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/session"
)

type GotmuxLiveServer struct {
	server *gotmux.Server
}

func NewGotmuxLiveServer(server *gotmux.Server) *GotmuxLiveServer {
	return &GotmuxLiveServer{server: server}
}

func OpenLiveServer(socket string) (*GotmuxLiveServer, error) {
	server, err := tmux.Open(socket)
	if err != nil {
		return nil, oops.In("remote").Code("remote_tmux_failed").With("operation", "open_live_server").Wrap(err)
	}

	return NewGotmuxLiveServer(server), nil
}

func (live *GotmuxLiveServer) Sessions(ctx context.Context) ([]session.Session, error) {
	if live == nil || live.server == nil {
		return nil, oops.In("remote").Code("remote_tmux_failed").With("operation", "sessions").Wrap(ErrIncompleteDependencies)
	}

	sessions, err := session.ListLive(ctx, live.server, "")
	if err != nil {
		return nil, oops.In("remote").Code("remote_tmux_failed").With("operation", "sessions").Wrap(err)
	}

	return sessions, nil
}

func (live *GotmuxLiveServer) Create(ctx context.Context, name, directory string) error {
	if live == nil || live.server == nil {
		return oops.In("remote").Code("remote_tmux_failed").With("operation", "create_session").Wrap(ErrIncompleteDependencies)
	}

	_, err := live.server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    name,
		Dir:     directory,
		Window:  "",
		Program: gotmux.Program{},
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 0, Height: 0},
		Start:   gotmux.ExistingOnly,
		Group:   "",
	})
	if err != nil {
		return oops.In("remote").Code("remote_tmux_failed").With("operation", "create_session").Wrap(err)
	}

	return nil
}

func (live *GotmuxLiveServer) Rename(ctx context.Context, sessionID, name string) error {
	handle, err := live.session(ctx, sessionID)
	if err != nil {
		return err
	}

	if err := handle.Rename(ctx, name); err != nil {
		if errors.Is(err, gotmux.ErrServerChanged) {
			return oops.In("remote").Code("remote_tmux_failed").With("operation", "rename_session").Wrap(errors.Join(ErrTargetChanged, err))
		}

		return oops.In("remote").Code("remote_tmux_failed").With("operation", "rename_session").Wrap(err)
	}

	return nil
}

func (live *GotmuxLiveServer) Kill(ctx context.Context, sessionID string) error {
	handle, err := live.session(ctx, sessionID)
	if err != nil {
		return err
	}

	if err := handle.Kill(ctx); err != nil {
		if errors.Is(err, gotmux.ErrServerChanged) {
			return oops.In("remote").Code("remote_tmux_failed").With("operation", "kill_session").Wrap(errors.Join(ErrTargetChanged, err))
		}

		return oops.In("remote").Code("remote_tmux_failed").With("operation", "kill_session").Wrap(err)
	}

	return nil
}

func (live *GotmuxLiveServer) Attach(ctx context.Context, sessionID string, streams AttachStreams) error {
	handle, err := live.session(ctx, sessionID)
	if err != nil {
		return err
	}

	if err := handle.Attach(ctx, gotmux.TerminalStreams{In: streams.In, Out: streams.Out, Err: streams.Err}, gotmux.AttachOptions{
		ReadOnly:            false,
		PreserveEnvironment: false,
		Detach:              gotmux.DetachNone,
		Dir:                 "",
		Flags:               nil,
	}); err != nil {
		return oops.In("remote").Code("remote_tmux_failed").With("operation", "attach_session").Wrap(err)
	}

	return nil
}

func (live *GotmuxLiveServer) PaneSession(ctx context.Context, paneID string) (string, error) {
	if live == nil || live.server == nil {
		return "", oops.In("remote").Code("remote_tmux_failed").With("operation", "pane_session").Wrap(ErrIncompleteDependencies)
	}

	pane, err := live.server.Pane(ctx, gotmux.PaneID(paneID))
	if err != nil {
		return "", oops.In("remote").Code("remote_tmux_failed").With("operation", "pane_session").Wrap(err)
	}

	info, err := pane.Info(ctx)
	if err != nil {
		return "", oops.In("remote").Code("remote_tmux_failed").With("operation", "pane_session").Wrap(err)
	}

	sessionID, present := info.SessionID.Get()
	if !present {
		return "", oops.In("remote").Code("remote_tmux_failed").With("operation", "pane_session").Wrap(ErrTargetChanged)
	}

	return string(sessionID), nil
}

func (live *GotmuxLiveServer) session(ctx context.Context, sessionID string) (gotmux.Session, error) {
	if live == nil || live.server == nil {
		return gotmux.Session{}, oops.In("remote").Code("remote_tmux_failed").With("operation", "find_session").Wrap(ErrIncompleteDependencies)
	}

	handle, err := live.server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		if errors.Is(err, gotmux.ErrNotFound) {
			return gotmux.Session{}, oops.In("remote").Code("remote_tmux_failed").With("operation", "find_session").Wrap(errors.Join(ErrTargetChanged, err))
		}

		return gotmux.Session{}, oops.In("remote").Code("remote_tmux_failed").With("operation", "find_session").Wrap(err)
	}

	return handle, nil
}
