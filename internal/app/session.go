//go:build linux || darwin

package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

const sessionDirectoryMode = 0o755

var (
	ErrTargetChanged     = errors.New("selected session changed since it was observed")
	ErrDirectoryNotFound = errors.New("directory does not exist")
	ErrDirectoryNotDir   = errors.New("path is not a directory")
	errLiveRequired      = errors.New("action needs a live session")
)

func (local *Local) registerSessionHandlers() {
	handle(local, action.SessionCreate, local.createSession)
	handle(local, action.SessionRename, local.renameSession)
	handle(local, action.SessionKill, local.killSession)
	handle(local, action.SessionDelete, local.deleteSession)
	handle(local, action.SessionSwitch, local.switchSession)
}

func (local *Local) createSession(ctx context.Context, _ action.Caller, input action.CreateSession) (action.CreatedSession, error) {
	var created action.CreatedSession

	directory, err := sessionDirectory(ctx, input.Directory, input.CreateDirectory)
	if err != nil {
		return created, err
	}

	handle, err := local.workflow.CreateSession(ctx, input.Name, directory)
	if err != nil {
		return created, oops.In("app").With("operation", "create_session").Wrap(err)
	}

	row, err := local.findLive(ctx, string(handle.ID()))
	if err != nil {
		return created, err
	}

	return action.CreatedSession{Session: action.RefOf(row), Directory: directory}, nil
}

func (local *Local) renameSession(ctx context.Context, _ action.Caller, input action.RenameSession) (action.RenamedSession, error) {
	result := action.RenamedSession{PreviousName: input.Session.Name, Name: input.Name, HistoryUpdated: false}

	if _, err := local.liveTarget(ctx, input.Session); err != nil {
		return result, err
	}

	outcome, err := local.workflow.RenameSessionTargetOutcome(ctx, input.Session.Name, input.Name, input.Session.ID)
	result.HistoryUpdated = outcome.HistoryUpdated

	if err != nil {
		return result, oops.In("app").With("operation", "rename_session").Wrap(err)
	}

	return result, nil
}

func (local *Local) killSession(ctx context.Context, _ action.Caller, input action.KillSession) (action.KilledSession, error) {
	result := action.KilledSession{Session: input.Session}

	if _, err := local.liveTarget(ctx, input.Session); err != nil {
		return result, err
	}

	if err := local.workflow.KillSessionTarget(ctx, input.Session.Name, input.Session.ID, input.Force); err != nil {
		return result, oops.In("app").With("operation", "kill_session").Wrap(err)
	}

	return result, nil
}

func (local *Local) deleteSession(ctx context.Context, _ action.Caller, input action.DeleteSession) (action.DeletedSession, error) {
	refused := deletedSession(input.Session.Name, session.DeleteOutcome{
		LivePresent: false, LiveKilled: false, LiveSessionID: "", SavedFound: false, SavedDeleted: false,
		SavedGenerations: 0, SavedSessions: 0, PreservedGenerations: nil, PendingRewrites: nil, HistoryUpdated: false,
	})
	expectedID := ""

	if input.Session.Saved() {
		if err := local.refuseLiveNamesake(ctx, input.Session.Name); err != nil {
			return refused, err
		}
	} else {
		if _, err := local.liveTarget(ctx, input.Session); err != nil {
			return refused, err
		}

		expectedID = input.Session.ID
	}

	if err := local.ensureLeaseRoot(); err != nil {
		return refused, err
	}

	if _, _, err := local.useRepository(ctx, false); err != nil {
		return refused, err
	}

	outcome, err := local.workflow.DeleteSessionTargetEverywhere(ctx, input.Session.Name, expectedID, input.Force)
	if err != nil {
		return deletedSession(input.Session.Name, outcome), oops.In("app").With("operation", "delete_session").Wrap(err)
	}

	return deletedSession(input.Session.Name, outcome), nil
}

func (local *Local) switchSession(ctx context.Context, caller action.Caller, input action.SwitchSession) (action.SwitchedSession, error) {
	result := action.SwitchedSession(input)

	target, err := local.liveTarget(ctx, input.Session)
	if err != nil {
		return result, err
	}

	if caller.Client != "" && caller.Session != "" {
		_, err = local.workflow.SwitchSessionOutcomeForClientTarget(ctx, target, caller.Client, caller.Session)
	} else {
		_, err = local.workflow.SwitchSessionOutcome(ctx, target.Name)
	}

	if err != nil {
		return result, oops.In("app").With("operation", "switch_session").Wrap(err)
	}

	return result, nil
}

func (local *Local) refuseLiveNamesake(ctx context.Context, name string) error {
	rows, err := local.liveSessions(ctx)
	if errors.Is(err, gotmux.ErrNoServer) {
		return nil
	}

	if err != nil {
		return err
	}

	if slices.ContainsFunc(rows, func(row session.Session) bool { return row.Name == name }) {
		return action.Wrap(action.CodeTargetChanged, ErrTargetChanged)
	}

	return nil
}

func (local *Local) liveTarget(ctx context.Context, ref action.SessionRef) (session.Session, error) {
	if ref.Saved() {
		return session.Session{}, action.Wrap(action.CodeInvalidArgument, errLiveRequired)
	}

	row, err := local.findLive(ctx, ref.ID)
	if err != nil {
		return session.Session{}, err
	}

	identity := row.RuntimeIdentity
	if row.Name != ref.Name ||
		(ref.Socket != "" && row.TmuxServerSocket != ref.Socket) ||
		(ref.ServerPID != 0 && (identity.TmuxServerPID != ref.ServerPID || !identity.TmuxServerStartedAt.Equal(ref.ServerStartedAt))) ||
		(!ref.CreatedAt.IsZero() && !identity.TmuxSessionCreatedAt.Equal(ref.CreatedAt)) {
		return session.Session{}, action.Wrap(action.CodeTargetChanged, ErrTargetChanged)
	}

	return row, nil
}

func (local *Local) findLive(ctx context.Context, id string) (session.Session, error) {
	rows, err := local.liveSessions(ctx)
	if err != nil {
		return session.Session{}, err
	}

	index := slices.IndexFunc(rows, func(row session.Session) bool { return row.ID == id })
	if index < 0 {
		return session.Session{}, action.Wrap(action.CodeTargetChanged, ErrTargetChanged)
	}

	return rows[index], nil
}

func (local *Local) liveSessions(ctx context.Context) ([]session.Session, error) {
	if local.server == nil {
		return nil, action.Wrap(action.CodeUnavailable, ErrServerRequired)
	}

	rows, err := session.ListLive(ctx, local.server, "")
	if err != nil {
		return nil, oops.In("app").With("operation", "list_live_sessions").Wrap(err)
	}

	return rows, nil
}

func deletedSession(name string, outcome session.DeleteOutcome) action.DeletedSession {
	preserved := slices.Clone(outcome.PreservedGenerations)
	if preserved == nil {
		preserved = []string{}
	}

	pending := slices.Clone(outcome.PendingRewrites)
	if pending == nil {
		pending = []string{}
	}

	return action.DeletedSession{
		Name:                 name,
		LiveKilled:           outcome.LiveKilled,
		SavedDeleted:         outcome.SavedDeleted,
		SavedGenerations:     outcome.SavedGenerations,
		SavedSessions:        outcome.SavedSessions,
		PreservedGenerations: preserved,
		PendingRewrites:      pending,
	}
}

func sessionDirectory(ctx context.Context, requested string, create bool) (string, error) {
	if requested == "" {
		return "", nil
	}

	if expanded, ok := session.ExpandHomePath(requested); ok {
		requested = expanded
	}

	directory, err := filepath.Abs(filepath.Clean(requested))
	if err != nil {
		return "", action.Wrap(action.CodeInvalidArgument, err)
	}

	info, err := os.Stat(directory)
	if errors.Is(err, os.ErrNotExist) && create {
		return createSessionDirectory(ctx, directory)
	}

	if errors.Is(err, os.ErrNotExist) {
		return "", action.Wrap(action.CodeInvalidArgument, ErrDirectoryNotFound)
	}

	if err != nil {
		return "", action.Wrap(action.CodeUnavailable, err)
	}

	if !info.IsDir() {
		return "", action.Wrap(action.CodeInvalidArgument, ErrDirectoryNotDir)
	}

	return directory, nil
}

func createSessionDirectory(ctx context.Context, directory string) (string, error) {
	if err := os.MkdirAll(directory, sessionDirectoryMode); err != nil {
		code := action.CodeUnavailable
		if errors.Is(err, fs.ErrPermission) {
			code = action.CodeForbidden
		}

		return "", action.Wrap(code, oops.In("app").Code("session_directory_create_failed").
			With("operation", "create_session_directory").With("directory", directory).Wrapf(err, "creating directory"))
	}

	zerolog.Ctx(ctx).Info().Str("component", "session").Str("operation", "create_session_directory").
		Str("directory", directory).Msg("created session directory")

	return directory, nil
}
