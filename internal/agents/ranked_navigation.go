//go:build linux || darwin

package agents

import (
	"context"
	"slices"
	"time"

	"github.com/samber/oops"
)

const (
	selectionLatest agentSelectionKind = iota
	selectionWaiting
	selectionFailed
	selectionUnread
)

type PaneVisit struct {
	ServerSocket string
	SessionName  string
	PaneID       string
	ViewedAt     time.Time
}

type AgentVisit struct {
	Pane            PaneLocation
	ActivityChanged time.Time
	Origin          *PaneVisit
}

type agentSelectionKind uint8

type agentSelection struct {
	kind   agentSelectionKind
	visits []PaneVisit
}

func (n *Navigator) rankedAgent(ctx context.Context, scope NavigationScope, selection agentSelection) (AgentVisit, error) {
	current, panes, err := n.currentAndLivePanes(ctx)
	if err != nil {
		return AgentVisit{}, err
	}

	agents, err := LoadLiveAgents(ctx, n.source)
	if err != nil {
		return AgentVisit{}, err
	}

	agents = slices.DeleteFunc(agents, func(agent AgentSession) bool {
		return !SameServerSocket(agent.ServerSocket, current.ServerSocket)
	})
	panes = slices.DeleteFunc(slices.Clone(panes), func(pane PaneLocation) bool {
		return !SameServerSocket(pane.ServerSocket, current.ServerSocket)
	})
	agents = PruneVanishedPanes(agents, panes)

	sessionWindows, err := n.sessionWindows(ctx, current)
	if err != nil {
		return AgentVisit{}, err
	}

	visit, ok := rankedAgentPane(current, panes, agents, scope, sessionWindows, selection)
	if !ok {
		return visit, oops.In("agents").Code("not_found").With("operation", selection.operation()).Wrap(ErrNoTargetPane)
	}

	target := retargetToCurrentSession(current, visit.Pane, sessionWindows)

	visit.Pane = target
	if target.PaneID != current.PaneID {
		if err := n.activateWithHistory(ctx, current, target, navigationAgent); err != nil {
			return AgentVisit{}, err
		}
	}

	logNavigation(ctx, selection.operation(), current, target, navigationScopeName(scope))

	return visit, nil
}

func (s agentSelection) operation() string {
	return [...]string{"latest_idle", "waiting", "failed", "unread"}[s.kind]
}

func (s agentSelection) activity() string {
	return [...]string{"idle", "waiting", "failed", "idle"}[s.kind]
}

func observedCurrentIdleVisit(current CurrentContext, panes []PaneLocation, agents []AgentSession) *PaneVisit {
	for _, pane := range panes {
		if pane.PaneID != current.PaneID {
			continue
		}

		for _, agent := range agents {
			if agent.Activity == "idle" && !agent.ActivityChanged.IsZero() && paneMatchesAgentOwner(&agent, pane) {
				return &PaneVisit{ServerSocket: pane.ServerSocket, SessionName: current.SessionName, PaneID: pane.PaneID, ViewedAt: agent.ActivityChanged}
			}
		}
	}

	return nil
}

func rankedAgentPane(current CurrentContext, panes []PaneLocation, agents []AgentSession, scope NavigationScope, windows map[string]SessionWindow, selection agentSelection) (AgentVisit, bool) {
	var selected AgentVisit

	found := false

	if selection.kind == selectionUnread {
		selected.Origin = observedCurrentIdleVisit(current, panes, agents)
	}

	for _, pane := range panes {
		if scope == NavigationCurrentSession && !paneInSessionWindows(pane, windows) {
			continue
		}

		for _, agent := range agents {
			if !selection.eligible(current, pane, &agent) {
				continue
			}

			newer := agent.ActivityChanged.After(selected.ActivityChanged)

			older := agent.ActivityChanged.Before(selected.ActivityChanged)
			if !found || selection.kind == selectionWaiting && older || selection.kind != selectionWaiting && newer {
				selected.Pane = pane
				selected.ActivityChanged = agent.ActivityChanged
				found = true
			}
		}
	}

	return selected, found
}

func (s agentSelection) eligible(current CurrentContext, pane PaneLocation, agent *AgentSession) bool {
	if agent.Activity != s.activity() || agent.ActivityChanged.IsZero() || !paneMatchesAgentOwner(agent, pane) {
		return false
	}

	if s.kind != selectionUnread {
		return true
	}

	if pane.PaneID == current.PaneID {
		return false
	}

	for _, visit := range s.visits {
		if visit.PaneID != pane.PaneID || !SameServerSocket(visit.ServerSocket, pane.ServerSocket) || visit.ViewedAt.Before(agent.ActivityChanged) {
			continue
		}

		return false
	}

	return true
}
