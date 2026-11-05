//go:build linux || darwin

package app

import (
	"context"
	"errors"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

func (local *Local) moveWindow(
	ctx context.Context,
	_ action.Caller,
	input action.MoveWindow,
) (action.MovedWindow, error) {
	result := action.MovedWindow{
		Source: input.Source, Destination: input.Destination, Window: input.Window,
		Index: 0, PaneIDs: nil, Moved: false, HistoryUpdated: false,
	}
	if input.Source.Kind != session.KindLive || input.Destination.Kind != session.KindLive ||
		!gotmux.SessionID(input.Source.ID).
			Valid() ||
		!gotmux.SessionID(input.Destination.ID).Valid() ||
		input.Source.ID == input.Destination.ID {
		return result, action.Wrap(action.CodeInvalidArgument, session.ErrInvalidWindowMove)
	}

	source, err := local.moveWindowTarget(ctx, input.Source)
	if err != nil {
		return result, err
	}

	destination, err := local.moveWindowTarget(ctx, input.Destination)
	if err != nil {
		return result, err
	}

	result.Source = action.RefOf(source)
	result.Destination = action.RefOf(destination)
	out, err := local.workflow.MoveWindow(ctx, session.MoveWindowOptions{
		Source: source, Destination: destination, WindowID: input.Window, PaneID: input.Pane,
	})
	result.Window = out.WindowID
	result.Index = out.Index
	result.PaneIDs = out.PaneIDs
	result.Moved = out.Moved

	result.HistoryUpdated = out.HistoryUpdated
	if errors.Is(err, session.ErrWindowMoveUncertain) {
		return result, action.Wrap(action.CodeUncertain, err)
	}

	if err != nil && out.Moved {
		return result, action.Wrap(action.CodePartial, err)
	}

	if err != nil {
		return result, oops.In("app").With("operation", "move_window").Wrap(err)
	}

	return result, nil
}

func (local *Local) moveWindowTarget(
	ctx context.Context,
	ref action.SessionRef,
) (session.Session, error) {
	// An ID-only destination from the picker binder is resolved on this local
	// server, never interpreted as a session name or a remote endpoint.
	if ref.Name == "" {
		row, err := local.findLive(ctx, ref.ID)
		if err != nil {
			return session.Session{}, err
		}

		ref.Name = row.Name
	}

	return local.liveTarget(ctx, ref)
}
