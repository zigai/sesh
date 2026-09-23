//go:build linux || darwin

package agents

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/samber/oops"
	"github.com/zigai/aht/pkg/aht"
)

var emptyAgentSession AgentSession

func LoadLiveAgents(ctx context.Context, source SessionSource) ([]AgentSession, error) {
	if source == nil {
		return nil, oops.In("agents").Code("registry_unavailable").With("operation", "load_live_agents").Wrap(ErrRegistryUnavailable)
	}

	if err := ctx.Err(); err != nil {
		return nil, oops.In("agents").Code("operation_canceled").With("operation", "load_live_agents").Wrap(err)
	}

	sessions, err := source.List(ctx, liveSessionsFilter())
	if err != nil {
		return nil, oops.In("agents").Code("registry_read").With("operation", "load_live_agents").Wrap(err)
	}

	return ObservedAgentSessions(sessions), nil
}

func ObservedAgentSessions(sessions []aht.Session) []AgentSession {
	agents := make([]AgentSession, 0, len(sessions))

	for _, session := range sessions {
		agent, ok := observedAgentSession(session)
		if !ok {
			continue
		}

		agents = append(agents, agent)
	}

	return agents
}

func AttentionFor(activity string) string {
	switch activity {
	case string(aht.ActivityWaiting):
		return string(StateWaiting)
	case string(aht.ActivityFailed):
		return string(StateFailed)
	case string(aht.ActivityIdle):
		return "idle"
	default:
		return string(StateNormal)
	}
}

func NeedsAttention(agent AgentSession) bool {
	switch agent.Attention {
	case string(StateWaiting), string(StateFailed), "idle":
		return true
	default:
		return false
	}
}

func observedAgentSession(session aht.Session) (AgentSession, bool) {
	if session.Presence != aht.PresenceLive {
		return emptyAgentSession, false
	}

	if strings.TrimSpace(session.Tmux.PaneID) == "" {
		return emptyAgentSession, false
	}

	if nonInteractiveAgent(session) {
		return emptyAgentSession, false
	}

	return agentSessionFromRegistry(session), true
}

func agentSessionFromRegistry(session aht.Session) AgentSession {
	activity := ""

	if session.Activity != nil {
		activity = string(*session.Activity)
	}

	agent := AgentSession{
		ID:               session.ID,
		Harness:          string(session.Harness),
		Presence:         string(session.Presence),
		Activity:         activity,
		Attention:        AttentionFor(activity),
		ServerSocket:     NormalizeServerSocket(session.Tmux.ServerSocket),
		SessionID:        session.Tmux.SessionID,
		SessionName:      session.Tmux.SessionName,
		WindowID:         session.Tmux.WindowID,
		WindowIndex:      session.Tmux.WindowIndex,
		PaneID:           session.Tmux.PaneID,
		PaneIndex:        session.Tmux.PaneIndex,
		PanePID:          session.Tmux.PanePID,
		Command:          "",
		Interactive:      true,
		CWD:              session.CWD,
		ProjectRoot:      session.ProjectRoot,
		HarnessSessionID: session.SessionID,
		SessionPath:      session.SessionPath,
		ResumeCommand:    slices.Clone(session.ResumeCommand),
		ActivityChanged:  session.ActivityChangedAt,
		UpdatedAt:        session.UpdatedAt,
	}

	if session.Process != nil {
		if agent.PanePID == 0 {
			agent.PanePID = session.Process.PID
		}

		if session.Process.Executable != "" {
			agent.Command = filepath.Base(session.Process.Executable)
		}
	}

	return agent
}
