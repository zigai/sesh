package agent

import (
	"context"
	"errors"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/session"
)

type attentionAction uint8

const (
	actionWaiting attentionAction = iota
	actionFailed
	actionUnread
	actionToggle
)

func NewActionWaitingCommand() *cobra.Command {
	return newAttentionAction(actionWaiting, "agent-waiting", "Focus the coding agent waiting longest for input or approval", "Focus the live coding agent with the oldest recorded transition to waiting.")
}

func NewActionFailedCommand() *cobra.Command {
	return newAttentionAction(actionFailed, "agent-failed", "Focus the most recently failed coding agent", "Focus the live coding agent with the most recent recorded transition to failed.")
}

func NewActionUnreadCommand() *cobra.Command {
	return newAttentionAction(actionUnread, "agent-unread", "Focus the most recently idle coding agent with an unvisited result", "Focus the newest idle transition not acknowledged by sesh-recorded visits. The current pane is observed as visited. Repeated invocations advance through unread results. Arbitrary manual pane visits are not tracked.")
}

func NewActionToggleCommand() *cobra.Command {
	return newAttentionAction(actionToggle, "agent-toggle", "Alternate between your working pane and the last agent visited", "Alternate between the working pane and the last agent visited through sesh navigation, independently of agent-back history. A manually selected working pane replaces the remembered working pane. Requires an earlier agent navigation for this client.")
}

func newAttentionAction(action attentionAction, name, short, long string) *cobra.Command {
	cmd := &cobra.Command{
		Use: name, Short: short, Long: long,
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        func(cmd *cobra.Command, _ []string) error { return runAttentionAction(cmd, action) },
		Annotations: map[string]string{clibase.AnnotationOperation: name},
	}
	addAgentNavigationFlags(cmd)
	cmd.Flags().Bool("local", false, "Only consider panes in the current tmux session")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAttentionAction(cmd *cobra.Command, action attentionAction) error {
	navigator, err := resolveAgentServer(cmd)
	if err != nil {
		return err
	}

	local, _ := cmd.Flags().GetBool("local")

	scope := agents.NavigationGlobal
	if local {
		scope = agents.NavigationCurrentSession
	}

	ctx := commandContextOrBackground(cmd)

	target, err := navigateAttentionAction(ctx, cmd, navigator, action, scope)
	if err != nil {
		return oops.In("cli").With("operation", cmd.Name()).Wrap(err)
	}

	if action != actionUnread {
		recordNavigatedAgentView(ctx, cmd, target)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, agentNavResultJSON{TargetPaneID: target.PaneID})
	}

	return nil
}

func navigateAttentionAction(ctx context.Context, cmd *cobra.Command, navigator *agents.Navigator, action attentionAction, scope agents.NavigationScope) (agents.PaneLocation, error) {
	switch action {
	case actionWaiting:
		return navigator.Waiting(ctx, scope)
	case actionFailed:
		return navigator.Failed(ctx, scope)
	case actionUnread:
		return navigateUnread(ctx, cmd, navigator, scope)
	case actionToggle:
		return navigator.Toggle(ctx, scope)
	default:
		return agents.PaneLocation{}, agents.ErrNoTargetPane
	}
}

func navigateUnread(ctx context.Context, cmd *cobra.Command, navigator *agents.Navigator, scope agents.NavigationScope) (agents.PaneLocation, error) {
	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return agents.PaneLocation{}, err
	}

	state, err := session.LoadState(workspace.History)
	if err != nil {
		return agents.PaneLocation{}, oops.In("cli").Code("history_read_failed").With("operation", "agent_unread").Wrap(err)
	}

	visits := make([]agents.PaneVisit, 0, len(state.AgentViewHistory))
	for _, entry := range state.AgentViewHistory {
		if entry.Session.Origin != "local" || entry.Session.ConnectionID != "" {
			continue
		}

		visits = append(visits, agents.PaneVisit{ServerSocket: entry.Session.Socket, SessionName: entry.Session.Name, PaneID: entry.PaneID, ViewedAt: time.Unix(0, entry.ViewedAt)})
	}

	visit, navErr := navigator.UnreadIdle(ctx, scope, visits)
	if navErr != nil && !errors.Is(navErr, agents.ErrNoTargetPane) {
		return agents.PaneLocation{}, navErr
	}

	writeErr := acknowledgeUnread(ctx, workspace.History, visit)

	return visit.Pane, errors.Join(navErr, writeErr)
}

func acknowledgeUnread(ctx context.Context, path string, visit agents.AgentVisit) error {
	if visit.Origin == nil && visit.Pane.PaneID == "" {
		return nil
	}

	_, err := session.UpdateState(ctx, path, func(state *session.State) {
		if visit.Origin != nil {
			origin := visit.Origin
			state.RecordAgentViewAt(session.Identity{Origin: "local", ConnectionID: "", RepositorySource: "", Socket: origin.ServerSocket, Name: origin.SessionName}, origin.PaneID, origin.ViewedAt)
		}

		if visit.Pane.PaneID != "" {
			pane := visit.Pane
			state.RecordAgentViewAt(session.Identity{Origin: "local", ConnectionID: "", RepositorySource: "", Socket: pane.ServerSocket, Name: pane.SessionName}, pane.PaneID, visit.ActivityChanged)
		}
	})
	if err != nil {
		return oops.In("cli").Code("history_write_failed").With("operation", "agent_unread").Wrap(err)
	}

	return nil
}
