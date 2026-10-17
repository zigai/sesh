//go:build linux || darwin

package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/session"
)

type agentNavigation func(navigator *agents.Navigator, ctx context.Context, scope agents.NavigationScope) (agents.PaneLocation, error)

func (local *Local) registerAgentHandlers() {
	local.handleNavigation(action.AgentNext, (*agents.Navigator).NextIdle)
	local.handleNavigation(action.AgentLatest, (*agents.Navigator).LatestIdle)
	local.handleNavigation(action.AgentWaiting, (*agents.Navigator).Waiting)
	local.handleNavigation(action.AgentFailed, (*agents.Navigator).Failed)
	local.handleNavigation(action.AgentToggle, (*agents.Navigator).Toggle)
	handle(local, action.AgentUnread, local.unreadAgent)
	handle(local, action.AgentBack, local.agentBack)
	handle(local, action.AgentFocus, local.focusAgent)
	handle(local, action.AgentWindowStatusClear, local.clearWindowStatus)
	handle(local, action.AgentMerge, local.mergeAgentBranch)
}

func (local *Local) handleNavigation(definition *action.Definition[action.NavigateAgents, action.FocusedPane], navigate agentNavigation) {
	handle(local, definition, func(ctx context.Context, caller action.Caller, input action.NavigateAgents) (action.FocusedPane, error) {
		navigator, err := local.navigator(caller)
		if err != nil {
			return action.FocusedPane{}, err
		}

		target, err := navigate(navigator, ctx, navigationScope(input))
		if err != nil {
			return action.FocusedPane{}, oops.In("app").With("operation", string(definition.ID)).Wrap(err)
		}

		local.recordAgentView(ctx, target)

		return focusedPane(target), nil
	})
}

// unreadAgent acknowledges what it observed even when no unread result remains, so repeated
// invocations advance instead of revisiting the same pane.
func (local *Local) unreadAgent(ctx context.Context, caller action.Caller, input action.NavigateAgents) (action.FocusedPane, error) {
	navigator, err := local.navigator(caller)
	if err != nil {
		return action.FocusedPane{}, err
	}

	state, err := session.LoadState(local.historyPath)
	if err != nil {
		return action.FocusedPane{}, oops.In("app").With("operation", "agent_unread").Wrap(err)
	}

	visits := make([]agents.PaneVisit, 0, len(state.AgentViewHistory))
	for _, entry := range state.AgentViewHistory {
		if entry.Session.Origin != string(session.OriginLocal) || entry.Session.ConnectionID != "" {
			continue
		}

		visits = append(visits, agents.PaneVisit{
			ServerSocket: entry.Session.Socket, SessionName: entry.Session.Name, PaneID: entry.PaneID, ViewedAt: time.Unix(0, entry.ViewedAtUnixNano),
		})
	}

	visit, navErr := navigator.UnreadIdle(ctx, navigationScope(input), visits)
	if navErr != nil && !errors.Is(navErr, agents.ErrNoTargetPane) {
		return action.FocusedPane{}, oops.In("app").With("operation", "agent_unread").Wrap(navErr)
	}

	if err := errors.Join(navErr, local.acknowledgeUnread(ctx, visit)); err != nil {
		return action.FocusedPane{}, oops.In("app").With("operation", "agent_unread").Wrap(err)
	}

	return focusedPane(visit.Pane), nil
}

func (local *Local) agentBack(ctx context.Context, caller action.Caller, _ action.ReturnToAgent) (action.FocusedPane, error) {
	navigator, err := local.navigator(caller)
	if err != nil {
		return action.FocusedPane{}, err
	}

	target, err := navigator.Back(ctx)
	if err != nil {
		return action.FocusedPane{}, oops.In("app").With("operation", "agent_back").Wrap(err)
	}

	return focusedPane(target), nil
}

func (local *Local) focusAgent(ctx context.Context, caller action.Caller, input action.FocusAgent) (action.FocusedPane, error) {
	target, err := local.liveTarget(ctx, input.Session)
	if err != nil {
		return action.FocusedPane{}, err
	}

	navigator, err := local.navigator(caller)
	if err != nil {
		return action.FocusedPane{}, err
	}

	focused, err := navigator.Focus(ctx, agents.FocusTarget{PaneID: input.Pane, SessionID: target.ID, SessionName: target.Name})
	if err != nil {
		return action.FocusedPane{}, oops.In("app").With("operation", "agent_focus").Wrap(err)
	}

	local.recordAgentView(ctx, focused)

	return focusedPane(focused), nil
}

func (local *Local) clearWindowStatus(ctx context.Context, _ action.Caller, _ action.ClearWindowStatus) (action.ClearedWindowStatus, error) {
	if local.server == nil {
		return action.ClearedWindowStatus{}, action.Wrap(action.CodeUnavailable, ErrServerRequired)
	}

	if err := agents.NewTmuxStyleController(local.server, true, true).ClearOwned(ctx); err != nil {
		return action.ClearedWindowStatus{}, oops.In("app").With("operation", "agent_window_status_clear").Wrap(err)
	}

	return action.ClearedWindowStatus{}, nil
}

func (local *Local) navigator(caller action.Caller) (*agents.Navigator, error) {
	if local.server == nil {
		return nil, action.Wrap(action.CodeUnavailable, ErrServerRequired)
	}

	current := agents.CurrentTarget{PaneID: caller.Pane, Client: caller.Client, SessionID: "", Session: ""}
	if strings.HasPrefix(caller.Session, "$") {
		current.SessionID = caller.Session
	} else {
		current.Session = caller.Session
	}

	navigator, err := agents.NewNavigator(agents.NavigatorOptions{
		Server: local.server, StatePath: "", Target: current, Controller: nil, Source: local.agentSource,
	})
	if err != nil {
		return nil, oops.In("app").With("operation", "agent_navigator").Wrap(err)
	}

	return navigator, nil
}

// recordAgentView is best effort: the pane is already focused, and a lost history entry only
// affects which results agent.unread reports as unvisited.
func (local *Local) recordAgentView(ctx context.Context, target agents.PaneLocation) {
	if local.historyPath == "" || target.SessionName == "" || target.PaneID == "" {
		return
	}

	_, err := session.UpdateState(ctx, local.historyPath, func(state *session.State) {
		state.RecordAgentView(localIdentity(target.ServerSocket, target.SessionName), target.PaneID)
	})
	if err != nil {
		zerolog.Ctx(ctx).Warn().Str("component", "agents").Str("operation", "record_view").
			Str("status", "degraded").Str("reason", "history_update_failed").Msg("agent navigation history was not recorded")
	}
}

func (local *Local) acknowledgeUnread(ctx context.Context, visit agents.AgentVisit) error {
	if visit.Origin == nil && visit.Pane.PaneID == "" {
		return nil
	}

	_, err := session.UpdateState(ctx, local.historyPath, func(state *session.State) {
		if origin := visit.Origin; origin != nil {
			state.RecordAgentViewAt(localIdentity(origin.ServerSocket, origin.SessionName), origin.PaneID, origin.ViewedAt)
		}

		if pane := visit.Pane; pane.PaneID != "" {
			state.RecordAgentViewAt(localIdentity(pane.ServerSocket, pane.SessionName), pane.PaneID, visit.ActivityChanged)
		}
	})
	if err != nil {
		return oops.In("app").Code("history_write_failed").With("operation", "agent_unread").Wrap(err)
	}

	return nil
}

func localIdentity(socket, name string) session.Identity {
	return session.Identity{Origin: string(session.OriginLocal), ConnectionID: "", Socket: socket, RepositorySource: "", Name: name}
}

func navigationScope(input action.NavigateAgents) agents.NavigationScope {
	if input.Local {
		return agents.NavigationCurrentSession
	}

	return agents.NavigationGlobal
}

func focusedPane(target agents.PaneLocation) action.FocusedPane {
	return action.FocusedPane{Pane: target.PaneID, SessionID: target.SessionID, Session: target.SessionName}
}
