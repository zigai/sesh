//go:build linux || darwin

package session

import (
	"context"
	"errors"
	"slices"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

var (
	ErrInvalidWindowMove = errors.New(
		"window move requires distinct local live sessions and stable IDs",
	)
	ErrWindowChanged = errors.New(
		"selected window or pane no longer belongs exclusively to the source session",
	)
	ErrWindowLinked = errors.New(
		"moving linked windows or moving into grouped sessions is ambiguous",
	)
	ErrWindowMoveUncertain = errors.New("window move outcome could not be verified; do not retry")
)

type MoveWindowOptions struct {
	Source      Session
	Destination Session
	WindowID    string
	PaneID      string
}

type MoveWindowOutcome struct {
	WindowID       string
	Index          int
	PaneIDs        []string
	Moved          bool
	HistoryUpdated bool
}

// MoveWindow moves the whole window detached, never joins panes or respawns
// processes. An explicit unused index after the last destination window avoids
// overwriting occupied slots (including concurrent additions). There is no retry.
// tmux removes the source session when its last window is moved; clients of that
// session are then subject to tmux's normal destroyed-session behavior.
func (w *Workflow) MoveWindow(
	ctx context.Context,
	opts MoveWindowOptions,
) (MoveWindowOutcome, error) {
	out, err := w.moveWindow(ctx, opts)
	if err != nil {
		return out, w.fail(
			ctx,
			"move_window",
			oops.In("session").With("operation", "move_window").Wrap(err),
		)
	}

	return out, nil
}

func (w *Workflow) moveWindow(
	ctx context.Context,
	opts MoveWindowOptions,
) (MoveWindowOutcome, error) {
	out := MoveWindowOutcome{
		WindowID:       "",
		Index:          0,
		PaneIDs:        nil,
		Moved:          false,
		HistoryUpdated: false,
	}
	if err := validateWindowMove(opts); err != nil {
		return out, err
	}

	if w.server == nil {
		return out, errServerRequired
	}

	source, err := w.moveSession(ctx, &opts.Source)
	if err != nil {
		return out, err
	}

	destination, err := w.moveSession(ctx, &opts.Destination)
	if err != nil {
		return out, err
	}

	window, err := w.moveWindowHandle(ctx, opts)
	if err != nil {
		return out, err
	}

	link, paneIDs, err := moveWindowSource(ctx, window, source.ID(), opts.PaneID)
	if err != nil {
		return out, err
	}

	out.WindowID = string(window.ID())
	out.PaneIDs = paneIDs

	index, err := destinationAppendIndex(ctx, destination)
	if err != nil {
		return out, err
	}

	out.Index = index

	moved, err := link.Move(
		ctx,
		destination,
		gotmux.LinkOptions{Index: &index, Select: false, Replace: false},
	)
	if err != nil {
		return out, windowMoveMutationError(err)
	}

	out.Moved = true

	out.Index = moved.Index()
	if err := w.moveWindowHistory(ctx, opts, out.PaneIDs); err != nil {
		return out, oops.In("session").
			Code("history_failed").
			With("operation", "move_window_history").
			Wrap(err)
	}

	out.HistoryUpdated = true

	return out, nil
}

func validateWindowMove(opts MoveWindowOptions) error {
	if !validLocalMoveSession(opts.Source) || !validLocalMoveSession(opts.Destination) ||
		opts.Source.ID == opts.Destination.ID ||
		!validMoveSelection(opts) {
		return ErrInvalidWindowMove
	}

	return nil
}

func validLocalMoveSession(target Session) bool {
	return gotmux.SessionID(target.ID).Valid() && target.Kind == KindLive &&
		(target.Origin == "" || target.Origin == OriginLocal) && target.RuntimeIdentity.ConnectionID == ""
}

func validMoveSelection(opts MoveWindowOptions) bool {
	return (opts.WindowID != "" || opts.PaneID != "") &&
		(opts.WindowID == "" || gotmux.WindowID(opts.WindowID).Valid()) &&
		(opts.PaneID == "" || gotmux.PaneID(opts.PaneID).Valid())
}

func (w *Workflow) moveSession(ctx context.Context, target *Session) (gotmux.Session, error) {
	var zero gotmux.Session
	if target.TmuxServerSocket != "" && target.TmuxServerSocket != w.server.Endpoint().SocketPath {
		return zero, ErrInvalidWindowMove
	}

	handle, err := w.server.Session(ctx, gotmux.SessionID(target.ID))
	if err != nil {
		return zero, tmuxWindowMoveError(err)
	}

	info, err := handle.Info(ctx)
	if err != nil {
		return zero, tmuxWindowMoveError(err)
	}

	if !sameMoveSession(target, info, handle.ServerIdentity()) {
		return zero, ErrSessionReplaced
	}

	if group, ok := info.Group.Get(); ok && group != "" {
		return zero, ErrWindowLinked
	}

	target.Name = info.Name

	return handle, nil
}

func (w *Workflow) moveWindowHandle(
	ctx context.Context,
	opts MoveWindowOptions,
) (gotmux.Window, error) {
	id := gotmux.WindowID(opts.WindowID)
	if opts.PaneID != "" {
		pane, err := w.server.Pane(ctx, gotmux.PaneID(opts.PaneID))
		if err != nil {
			return gotmux.Window{}, tmuxWindowMoveError(err)
		}

		window, err := pane.Window(ctx)
		if err != nil {
			return gotmux.Window{}, tmuxWindowMoveError(err)
		}

		if id != "" && id != window.ID() {
			return gotmux.Window{}, ErrWindowChanged
		}

		return window, nil
	}

	window, err := w.server.Window(ctx, id)
	if err != nil {
		return gotmux.Window{}, tmuxWindowMoveError(err)
	}

	return window, nil
}

func appendWindowIndex(windows []gotmux.WindowLink) (int, error) {
	index := 0
	for _, window := range windows {
		if window.Index() >= index {
			index = window.Index() + 1
		}
	}

	if index > 1<<30 {
		return 0, ErrInvalidWindowMove
	}

	return index, nil
}

func (w *Workflow) moveWindowHistory(
	ctx context.Context,
	opts MoveWindowOptions,
	panes []string,
) error {
	source, ok := w.localIdentity(opts.Source.Name)
	if !ok {
		return nil
	}

	destination, ok := w.localIdentity(opts.Destination.Name)
	if !ok {
		return nil
	}

	_, err := UpdateState(ctx, w.historyPath, func(state *State) {
		state.MoveAgentViews(source, destination, panes)
	})

	return err
}

func sameMoveSession(
	target *Session,
	info gotmux.SessionInfo,
	identity gotmux.ServerIdentity,
) bool {
	expected := target.RuntimeIdentity
	if target.Name != "" && info.Name != target.Name {
		return false
	}

	if expected.TmuxServerSocket != "" &&
		expected.TmuxServerSocket != identity.Endpoint.SocketPath {
		return false
	}

	if expected.TmuxServerPID != 0 &&
		(expected.TmuxServerPID != identity.PID || !expected.TmuxServerStartedAt.Equal(identity.Started)) {
		return false
	}

	return expected.TmuxSessionCreatedAt.IsZero() ||
		expected.TmuxSessionCreatedAt.Equal(info.Created)
}

func moveWindowSource(
	ctx context.Context,
	window gotmux.Window,
	source gotmux.SessionID,
	paneID string,
) (gotmux.WindowLink, []string, error) {
	var zero gotmux.WindowLink

	links, err := window.Links(ctx)
	if err != nil {
		return zero, nil, tmuxWindowMoveError(err)
	}

	if len(links) != 1 {
		return zero, nil, ErrWindowLinked
	}

	if links[0].Session().ID() != source {
		return zero, nil, ErrWindowChanged
	}

	panes, err := window.Panes(ctx)
	if err != nil {
		return zero, nil, tmuxWindowMoveError(err)
	}

	ids := make([]string, len(panes))
	for i, pane := range panes {
		ids[i] = string(pane.ID)
	}
	// Recheck pane ownership in the window inventory in case break-pane moved it.
	if paneID != "" && !slices.Contains(ids, paneID) {
		return zero, nil, ErrWindowChanged
	}

	return links[0], ids, nil
}

func destinationAppendIndex(ctx context.Context, destination gotmux.Session) (int, error) {
	windows, err := destination.Windows(ctx)
	if err != nil {
		return 0, tmuxWindowMoveError(err)
	}

	return appendWindowIndex(windows)
}

func windowMoveMutationError(err error) error {
	// Preserve transport uncertainty instead of advertising a safe retry.
	if op, ok := errors.AsType[*gotmux.OperationError](err); !ok ||
		(op.Outcome.Effect != gotmux.EffectNotSent && op.Outcome.Effect != gotmux.EffectRejected) {
		return tmuxWindowMoveError(errors.Join(ErrWindowMoveUncertain, err))
	}

	return tmuxWindowMoveError(err)
}

func tmuxWindowMoveError(err error) error {
	return oops.In("session").Code("tmux_failed").With("operation", "move_window").Wrap(err)
}
