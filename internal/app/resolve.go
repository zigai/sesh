//go:build linux || darwin

package app

import (
	"context"
	"errors"
	"slices"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/session"
)

var errUnresolvableSubject = errors.New("action subject cannot be selected by name")

// ResolveSubject selects what an action applies to for callers that name their target, such as
// the command line: sessions by name or ID, or an agent pane by pane ID. Saved sessions come
// from generation, or the current one when empty.
func (local *Local) ResolveSubject(ctx context.Context, kind action.SubjectKind, generation string, selectors []string) (action.Subject, error) {
	subject := action.Subject{At: action.Local(), Sessions: nil, Pane: ""}

	switch kind {
	case action.SubjectNone:
		return subject, nil
	case action.SubjectAgentPane:
		if len(selectors) != 1 {
			return subject, action.Wrap(action.CodeInvalidArgument, action.ErrSubjectMismatch)
		}

		owner, err := local.resolvePane(ctx, selectors[0])
		if err != nil {
			return subject, err
		}

		subject.Sessions, subject.Pane = []session.Session{owner}, selectors[0]

		return subject, nil
	case action.SubjectLiveSession, action.SubjectSavedSession, action.SubjectSession:
	}

	resolved := make([]session.Session, 0, len(selectors))

	for _, selector := range selectors {
		target, err := local.resolveSession(ctx, kind, generation, selector)
		if err != nil {
			return subject, err
		}

		resolved = append(resolved, target)
	}

	subject.Sessions = resolved

	return subject, nil
}

func (local *Local) resolvePane(ctx context.Context, paneID string) (session.Session, error) {
	if local.server == nil {
		return session.Session{}, action.Wrap(action.CodeUnavailable, ErrServerRequired)
	}

	panes, err := agents.ObservePanes(ctx, local.server)
	if err != nil {
		return session.Session{}, oops.In("app").With("operation", "list_panes").Wrap(err)
	}

	index := slices.IndexFunc(panes, func(pane agents.PaneLocation) bool { return pane.PaneID == paneID })
	if index < 0 {
		return session.Session{}, action.Wrap(action.CodeNotFound, agents.ErrPaneNotLive)
	}

	return local.findLive(ctx, panes[index].SessionID)
}

func (local *Local) resolveSession(ctx context.Context, kind action.SubjectKind, generation, selector string) (session.Session, error) {
	switch kind {
	case action.SubjectLiveSession:
		return local.resolveLive(ctx, selector)
	case action.SubjectSavedSession:
		return local.resolveSaved(ctx, generation, selector)
	case action.SubjectSession:
		target, err := local.resolveLive(ctx, selector)
		if code := classify(err); code != action.CodeNotFound && code != action.CodeUnavailable {
			return target, err
		}

		return local.resolveSaved(ctx, generation, selector)
	case action.SubjectNone, action.SubjectAgentPane:
		return session.Session{}, action.Wrap(action.CodeInvalidArgument, errUnresolvableSubject)
	default:
		return session.Session{}, action.Wrap(action.CodeInvalidArgument, errUnresolvableSubject)
	}
}

func (local *Local) resolveLive(ctx context.Context, selector string) (session.Session, error) {
	rows, err := local.liveSessions(ctx)
	if err != nil {
		return session.Session{}, err
	}

	target, err := action.SelectSession(rows, selector)
	if err != nil {
		return session.Session{}, oops.In("app").With("operation", "select_session").Wrap(err)
	}

	return target, nil
}

func (local *Local) resolveSaved(ctx context.Context, generation, selector string) (session.Session, error) {
	repo, found, err := local.useRepository(ctx, false)
	if err != nil {
		return session.Session{}, err
	}

	if !found {
		return session.Session{}, action.Wrap(action.CodeNotFound, session.ErrSessionNotFound)
	}

	rows, err := session.ListSaved(ctx, repo, local.repository.Path(), generation)
	if err != nil {
		return session.Session{}, oops.In("app").With("operation", "list_saved_sessions").Wrap(err)
	}

	target, err := action.SelectSession(rows, selector)
	if err != nil {
		return session.Session{}, oops.In("app").With("operation", "select_session").Wrap(err)
	}

	return target, nil
}
