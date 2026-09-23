//go:build linux || darwin

package session

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
)

func (w *Workflow) RenameSession(ctx context.Context, oldName, newName string) error {
	return w.RenameSessionTarget(ctx, oldName, newName, "")
}

func (w *Workflow) RenameSessionTarget(ctx context.Context, oldName, newName, expectedSessionID string) error {
	_, err := w.RenameSessionTargetOutcome(ctx, oldName, newName, expectedSessionID)

	return err
}

func (w *Workflow) RenameSessionOutcome(ctx context.Context, oldName, newName string) (RenameOutcome, error) {
	return w.RenameSessionTargetOutcome(ctx, oldName, newName, "")
}

func (w *Workflow) RenameSessionTargetOutcome(
	ctx context.Context,
	oldName string,
	newName string,
	expectedSessionID string,
) (RenameOutcome, error) {
	out := RenameOutcome{
		PreviousName:   oldName,
		Name:           newName,
		Renamed:        false,
		HistoryUpdated: false,
	}

	if err := validateSessionName(newName); err != nil {
		return out, oops.In("session").Code("invalid_argument").Wrap(err)
	}

	if w.server == nil {
		return out, oops.In("session").Code("endpoint_required").Wrap(errServerRequired)
	}

	var target deleteLiveTarget

	if expectedSessionID != "" {
		var targetErr error

		target, targetErr = w.prepareRequiredLiveTarget(ctx, oldName, expectedSessionID)
		if targetErr != nil {
			return out, w.fail(ctx, "rename", targetErr)
		}
	}

	exists, err := w.sessionExists(ctx, newName)
	if err != nil {
		return out, w.fail(ctx, "rename", err)
	}

	if exists {
		return out, w.fail(ctx, "rename", oops.In("session").Code("already_exists").Wrapf(ErrSessionAlreadyExists, "session %q already exists", newName))
	}

	if expectedSessionID == "" {
		target, err = w.prepareRequiredLiveTarget(ctx, oldName, "")
		if err != nil {
			return out, w.fail(ctx, "rename", err)
		}
	}

	if err := w.revalidateLiveTarget(ctx, target, oldName, true); err != nil {
		return out, w.fail(ctx, "rename", err)
	}

	if err := target.session.Rename(ctx, newName); err != nil {
		return out, w.fail(ctx, "rename", oops.In("session").Code("tmux_failed").With("operation", "rename_session").Wrap(err))
	}

	out.Renamed = true

	if err := w.renameHistory(ctx, oldName, newName); err != nil {
		return out, w.fail(ctx, "rename", oops.In("session").Code("history_failed").With("operation", "rename_history").Wrap(err))
	}

	out.HistoryUpdated = true

	zerolog.Ctx(ctx).Info().
		Str("component", "session").
		Str("operation", "rename").
		Str("status", "ok").
		Msg("session renamed")

	return out, nil
}

func (w *Workflow) renameHistory(ctx context.Context, oldName, newName string) error {
	oldIdent, ok := w.localIdentity(oldName)
	if !ok {
		return nil
	}

	newIdent, ok := w.localIdentity(newName)
	if !ok {
		return nil
	}

	_, err := UpdateState(ctx, w.historyPath, func(st *State) {
		st.RenameSession(oldIdent, newIdent)
	})

	return err
}

func (w *Workflow) KillSession(ctx context.Context, name string, force bool) error {
	return w.KillSessionTarget(ctx, name, "", force)
}

func (w *Workflow) KillSessionTarget(ctx context.Context, name, expectedSessionID string, force bool) error {
	if w.server == nil {
		return oops.In("session").Code("endpoint_required").Wrap(errServerRequired)
	}

	target, err := w.prepareRequiredLiveTarget(ctx, name, expectedSessionID)
	if err != nil {
		return w.fail(ctx, "kill", err)
	}

	if err := w.revalidateLiveTarget(ctx, target, name, true); err != nil {
		return w.fail(ctx, "kill", err)
	}

	if err := revalidateLiveNotAttached(ctx, target.session, force, true); err != nil {
		return w.fail(ctx, "kill", err)
	}

	if err := target.session.Kill(ctx); err != nil {
		return w.fail(ctx, "kill", oops.In("session").Code("tmux_failed").With("operation", "kill_session").Wrap(err))
	}

	zerolog.Ctx(ctx).Info().
		Str("component", "session").
		Str("operation", "kill").
		Str("status", "ok").
		Msg("session killed")

	return nil
}
