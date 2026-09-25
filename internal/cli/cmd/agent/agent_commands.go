package cmdagent

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/session"
)

const annotationOwnedValue = "true"

type agentNavResultJSON struct {
	TargetPaneID string `json:"targetPaneId"`
}

type agentStatusResultJSON struct {
	Total   int `json:"total"`
	Live    int `json:"live"`
	Waiting int `json:"waiting"`
	Failed  int `json:"failed"`
	Unread  int `json:"unread"`
	Idle    int `json:"idle"`
}

var errTargetPaneRequired = errors.New("--target-pane is required")

func addAgentNavigationFlags(cmd *cobra.Command) {
	cmd.Flags().String("socket", "", "Explicit absolute path to tmux server socket")
	cmd.Flags().String("state", "", "Path to the AHT agent registry")
	cmd.Flags().String("client", "", "Explicit tmux client terminal to navigate")
	cmd.Flags().String("pane", "", "Explicit invoking pane ID, for example %1")
	cmd.Flags().String("session", "", "Explicit invoking session name or ID")
	cmd.Flags().String("history", "", "Path to the sesh history state file")
}

func addAgentRegistryFlags(cmd *cobra.Command) {
	cmd.Flags().String("state", "", "Path to the AHT agent registry")
}

func resolveAgentSocket(cmd *cobra.Command) string {
	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath == "" {
		if tmuxEnv := os.Getenv("TMUX"); tmuxEnv != "" {
			parts := strings.Split(tmuxEnv, ",")
			if len(parts) > 0 && parts[0] != "" {
				socketPath = parts[0]
			}
		}
	}

	return socketPath
}

func resolveAgentHistoryPath(cmd *cobra.Command) string {
	historyPath, _ := cmd.Flags().GetString("history")
	if historyPath != "" {
		if abs, err := filepath.Abs(historyPath); err == nil {
			return abs
		}
	}

	return filepath.Join(config.GetXDGPaths().StateHome, "sesh", "history.json")
}

func recordNavigatedAgentView(ctx context.Context, cmd *cobra.Command, target agents.PaneLocation) {
	if target.SessionName == "" || target.PaneID == "" {
		return
	}

	historyPath := resolveAgentHistoryPath(cmd)
	if historyPath == "" {
		return
	}

	ident := session.Identity{
		Origin:           "local",
		ConnectionID:     "",
		Socket:           target.ServerSocket,
		RepositorySource: "",
		Name:             target.SessionName,
	}

	_, _ = session.UpdateState(ctx, historyPath, func(st *session.State) {
		st.RecordAgentView(ident, target.PaneID)
	})
}

func resolveAgentStatePath(cmd *cobra.Command) string {
	statePath, _ := cmd.Flags().GetString("state")
	if statePath == "" {
		return ""
	}

	if abs, err := filepath.Abs(statePath); err == nil {
		return abs
	}

	return statePath
}

func resolveAgentTarget(cmd *cobra.Command) agents.CurrentTarget {
	pane, _ := cmd.Flags().GetString("pane")
	client, _ := cmd.Flags().GetString("client")
	session, _ := cmd.Flags().GetString("session")

	target := agents.CurrentTarget{PaneID: pane, Client: client, SessionID: "", Session: ""}

	if session != "" {
		if strings.HasPrefix(session, "$") {
			target.SessionID = session
		} else {
			target.Session = session
		}
	}

	return target
}

func resolveAgentServer(cmd *cobra.Command) (*agents.Navigator, error) {
	socketPath := resolveAgentSocket(cmd)
	if socketPath == "" {
		return nil, clibase.NewExitError(clibase.ExitCodeUsage, clibase.ErrSocketRequired)
	}

	server, err := integration.Open(socketPath)
	if err != nil {
		return nil, oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	navigator, err := agents.NewNavigator(agents.NavigatorOptions{
		Server:     server,
		StatePath:  resolveAgentStatePath(cmd),
		Target:     resolveAgentTarget(cmd),
		Controller: nil,
		Source:     nil,
	})
	if err != nil {
		return nil, oops.In("cli").With("operation", "agent_navigation").Wrap(err)
	}

	return navigator, nil
}

func newAgentNextCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "next",
		Short: "Switch to next coding agent needing attention",
		Long:  "Switch to the next coding agent pane needing attention (waiting, failed, or idle) and activate its window, session, and pane.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runAgentNext,
	}
	cmd.Annotations = map[string]string{clibase.AnnotationSuccessOwned: annotationOwnedValue}

	addAgentNavigationFlags(cmd)
	cmd.Flags().Bool("local", false, "Only consider panes in the current tmux session")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAgentNext(cmd *cobra.Command, _ []string) error {
	navigator, err := resolveAgentServer(cmd)
	if err != nil {
		return err
	}

	localOnly, _ := cmd.Flags().GetBool("local")

	scope := agents.NavigationGlobal
	if localOnly {
		scope = agents.NavigationCurrentSession
	}

	target, navErr := navigator.NextIdle(commandContextOrBackground(cmd), scope)
	if navErr != nil {
		return oops.In("cli").With("operation", "agent_next").Wrap(navErr)
	}

	recordNavigatedAgentView(commandContextOrBackground(cmd), cmd, target)

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, agentNavResultJSON{TargetPaneID: target.PaneID})
	}

	return clibase.WriteOutput(cmd, "Switched to agent pane %s\n", target.PaneID)
}

func newAgentBackCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "back",
		Short: "Return through agent navigation history",
		Long:  "Return through the agent navigation history stack to the previous pane.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runAgentBack,
	}
	cmd.Annotations = map[string]string{clibase.AnnotationSuccessOwned: annotationOwnedValue}

	addAgentNavigationFlags(cmd)
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAgentBack(cmd *cobra.Command, _ []string) error {
	navigator, err := resolveAgentServer(cmd)
	if err != nil {
		return err
	}

	target, navErr := navigator.Back(commandContextOrBackground(cmd))
	if navErr != nil {
		return oops.In("cli").With("operation", "agent_back").Wrap(navErr)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, agentNavResultJSON{TargetPaneID: target.PaneID})
	}

	return clibase.WriteOutput(cmd, "Returned to pane %s\n", target.PaneID)
}

func newAgentFocusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "focus",
		Short: "Focus one coding agent pane",
		Long:  "Focus a specific pane, activating its window and session for the invoking client.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runAgentFocus,
	}
	cmd.Annotations = map[string]string{clibase.AnnotationSuccessOwned: annotationOwnedValue}

	addAgentNavigationFlags(cmd)
	cmd.Flags().String("target-pane", "", "Pane ID to focus, for example %1")
	cmd.Flags().String("target-session", "", "Session name or ID expected to hold the pane")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAgentFocus(cmd *cobra.Command, _ []string) error {
	navigator, err := resolveAgentServer(cmd)
	if err != nil {
		return err
	}

	paneID, _ := cmd.Flags().GetString("target-pane")
	if paneID == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, oops.In("cli").Code("usage_invalid").With("operation", "agent_focus").Wrap(errTargetPaneRequired))
	}

	targetSession, _ := cmd.Flags().GetString("target-session")

	focus := agents.FocusTarget{PaneID: paneID, SessionID: "", SessionName: ""}

	switch {
	case strings.HasPrefix(targetSession, "$"):
		focus.SessionID = targetSession
	case targetSession != "":
		focus.SessionName = targetSession
	}

	target, focusErr := navigator.Focus(commandContextOrBackground(cmd), focus)
	if focusErr != nil {
		return oops.In("cli").With("operation", "agent_focus").Wrap(focusErr)
	}

	recordNavigatedAgentView(commandContextOrBackground(cmd), cmd, target)

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, agentNavResultJSON{TargetPaneID: target.PaneID})
	}

	return clibase.WriteOutput(cmd, "Focused agent pane %s\n", target.PaneID)
}

func newAgentStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show coding agent attention summary",
		Long:  "Show coding agent attention summary across live sessions, run the realtime window status tracker, or clear its styles.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runAgentStatus,
	}

	addAgentRegistryFlags(cmd)
	clibase.AddJSONFlag(cmd)

	cmd.AddCommand(
		newAgentStatusRunCommand(),
		newAgentStatusClearCommand(),
	)

	return cmd
}

func runAgentStatus(cmd *cobra.Command, _ []string) error {
	summary, err := agents.Status(commandContextOrBackground(cmd), resolveAgentStatePath(cmd))
	if err != nil {
		return oops.In("cli").With("operation", "agent_status").Wrap(err)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, agentStatusResultJSON{
			Total:   summary.Total,
			Live:    summary.Live,
			Waiting: summary.Waiting,
			Failed:  summary.Failed,
			Unread:  summary.Unread,
			Idle:    summary.Idle,
		})
	}

	return clibase.WriteOutput(
		cmd,
		"Agents: %d live (waiting: %d, failed: %d, unread: %d, idle: %d)\n",
		summary.Live,
		summary.Waiting,
		summary.Failed,
		summary.Unread,
		summary.Idle,
	)
}

func newAgentStatusRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the agent window status tracker in the foreground",
		Long:  "Run the realtime agent window status tracker until interrupted. The tracker streams AHT state and styles tmux windows needing attention.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runAgentStatusRun,
	}

	addAgentRegistryFlags(cmd)
	cmd.Flags().String("socket", "", "Explicit absolute path to tmux server socket")
	cmd.Flags().Bool("quiet", false, "Silence informational log output")

	return cmd
}

func runAgentStatusRun(cmd *cobra.Command, _ []string) error {
	socketPath := resolveAgentSocket(cmd)
	if socketPath == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, clibase.ErrSocketRequired)
	}

	server, err := integration.Open(socketPath)
	if err != nil {
		return oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	quiet, _ := cmd.Flags().GetBool("quiet")

	styles := agents.NewTmuxStyleController(server, true, true)
	trackerOpts := agents.DefaultTrackerOptions()
	if cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd); err == nil && cfg != nil {
		resolved := cfg.ResolvedAgentStatus()
		trackerOpts.Palette = cfg.ResolvedPalette()
		trackerOpts.ActiveBypass = resolved.ActiveBypass
		trackerOpts.ClearFailedOnVisit = resolved.ClearFailedOnVisit
		trackerOpts.SetWindowStatusStyle = resolved.SetWindowStatusStyle
		trackerOpts.SetWindowStatusBellStyle = resolved.SetWindowStatusBellStyle
	}
	tracker := agents.NewTracker(styles, trackerOpts)

	ctx, stop := signal.NotifyContext(commandContextOrBackground(cmd), os.Interrupt, syscall.SIGTERM)
	defer stop()

	watchOptions := agents.DefaultWatchOptions()
	watchOptions.Quiet = quiet

	if err := agents.RunStatusTracker(ctx, agents.NewAHTStore(resolveAgentStatePath(cmd)), tracker, watchOptions, true); err != nil {
		return oops.In("cli").With("operation", "agent_status_run").Wrap(err)
	}

	return nil
}

func newAgentStatusClearCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear window status styles applied by sesh",
		Long:  "Restore every window status style sesh applied, leaving styles owned by other tools untouched.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runAgentStatusClear,
	}

	cmd.Flags().String("socket", "", "Explicit absolute path to tmux server socket")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAgentStatusClear(cmd *cobra.Command, _ []string) error {
	socketPath := resolveAgentSocket(cmd)
	if socketPath == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, clibase.ErrSocketRequired)
	}

	server, err := integration.Open(socketPath)
	if err != nil {
		return oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	styles := agents.NewTmuxStyleController(server, true, true)

	if err := styles.ClearOwned(commandContextOrBackground(cmd)); err != nil {
		return oops.In("cli").With("operation", "agent_status_clear").Wrap(err)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, struct {
			Cleared bool `json:"cleared"`
		}{Cleared: true})
	}

	return clibase.WriteOutput(cmd, "Cleared agent window styles\n")
}

func commandContextOrBackground(cmd *cobra.Command) context.Context {
	return clibase.CommandContext(cmd)
}

func NewActionNextCommand() *cobra.Command {
	cmd := newAgentNextCommand()
	cmd.Use = "agent-next"

	return cmd
}

func NewActionBackCommand() *cobra.Command {
	cmd := newAgentBackCommand()
	cmd.Use = "agent-back"

	return cmd
}

func NewActionFocusCommand() *cobra.Command {
	cmd := newAgentFocusCommand()
	cmd.Use = "agent-focus"

	return cmd
}

func NewActionStatusCommand() *cobra.Command {
	cmd := newAgentStatusCommand()
	cmd.Use = "agent-status"

	return cmd
}
