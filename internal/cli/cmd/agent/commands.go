package agent

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
)

type statusResultJSON struct {
	Total   int `json:"total"`
	Live    int `json:"live"`
	Waiting int `json:"waiting"`
	Failed  int `json:"failed"`
	Unread  int `json:"unread"`
	Idle    int `json:"idle"`
}

const stateFlag = "agent-state"

// NewCommand returns the group of commands that navigate and report on coding agents.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Navigate between coding agents",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: clibase.RequireSubcommand,
	}

	cmd.AddCommand(
		navigationCommand(action.AgentNext),
		navigationCommand(action.AgentLatest),
		navigationCommand(action.AgentWaiting),
		navigationCommand(action.AgentUnread),
		navigationCommand(action.AgentFailed),
		navigationCommand(action.AgentToggle),
		actioncli.Command(action.AgentBack, actioncli.Options[action.ReturnToAgent, action.FocusedPane]{
			Operation: "", SuccessOwned: false, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.AgentFocus, actioncli.Options[action.FocusAgent, action.FocusedPane]{
			Operation: "", SuccessOwned: false, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.AgentWindowStatusClear, actioncli.Options[action.ClearWindowStatus, action.ClearedWindowStatus]{
			Operation: "", SuccessOwned: false, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		actioncli.Command(action.AgentMerge, actioncli.Options[action.MergeAgentBranch, action.MergedAgentBranch]{
			Operation: "", SuccessOwned: true, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
		}),
		newAgentStatusCommand(),
	)

	return cmd
}

func navigationCommand(definition *action.Definition[action.NavigateAgents, action.FocusedPane]) *cobra.Command {
	return actioncli.Command(definition, actioncli.Options[action.NavigateAgents, action.FocusedPane]{
		Operation: "", SuccessOwned: false, Configure: nil, Prepare: nil, Frontend: nil, Render: nil,
	})
}

func addAgentRegistryFlags(cmd *cobra.Command) {
	cmd.Flags().String(stateFlag, "", "AHT agent registry file [default: agent_status.store_path setting, else AHT's default registry]")
}

func newAgentStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "status",
		Short:       "Show coding agent attention summary",
		Long:        "Show coding agent attention summary across live sessions.",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        runAgentStatus,
		Annotations: map[string]string{clibase.AnnotationOperation: "status"},
	}

	addAgentRegistryFlags(cmd)
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runAgentStatus(cmd *cobra.Command, _ []string) error {
	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return err
	}

	summary, err := agents.Status(commandContextOrBackground(cmd), workspace.AgentStore)
	if err != nil {
		return oops.In("cli").With("operation", "agent_status").Wrap(err)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, statusResultJSON{
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
		Use:         "run",
		Short:       "Run the agent window status tracker in the foreground",
		Long:        "Run the realtime agent window status tracker until interrupted. The tracker streams AHT state and styles tmux windows needing attention.",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        runAgentStatusRun,
		Annotations: map[string]string{clibase.AnnotationOperation: "agent_status_run"},
	}

	addAgentRegistryFlags(cmd)
	clibase.AddSocketFlag(cmd)
	cmd.Flags().Bool("quiet", false, "Silence informational log output")

	return cmd
}

func runAgentStatusRun(cmd *cobra.Command, _ []string) error {
	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return err
	}

	server, err := workspace.OpenTmux()
	if err != nil {
		return err
	}

	cfg := workspace.Config
	quiet, _ := cmd.Flags().GetBool("quiet")
	resolved := cfg.ResolvedAgentStatus()

	styles := agents.NewTmuxStyleController(server, resolved.SetWindowStatusStyle, resolved.SetWindowStatusBellStyle)
	tracker := agents.NewTracker(styles, statusTrackerOptions(cfg, resolved))

	ctx, stop := signal.NotifyContext(commandContextOrBackground(cmd), os.Interrupt, syscall.SIGTERM)
	defer stop()

	watchOptions := statusWatchOptions(resolved, quiet)

	if err := agents.RunStatusTracker(ctx, workspace.AgentSource(), tracker, watchOptions, resolved.ClearOnShutdown); err != nil {
		return oops.In("cli").With("operation", "agent_status_run").Wrap(err)
	}

	return nil
}

func statusTrackerOptions(cfg *config.Config, resolved config.AgentStatusConfig) agents.TrackerOptions {
	opts := agents.DefaultTrackerOptions()
	opts.Palette = cfg.ResolvedPalette()
	opts.ActiveBypass = resolved.ActiveBypass
	opts.ClearFailedOnVisit = resolved.ClearFailedOnVisit

	return opts
}

func statusWatchOptions(resolved config.AgentStatusConfig, quiet bool) agents.WatchOptions {
	opts := agents.DefaultWatchOptions()
	opts.Quiet = quiet || resolved.Quiet

	if initial, err := time.ParseDuration(resolved.InitialBackoff); err == nil && initial > 0 {
		opts.InitialBackoff = initial
	}

	if maxBackoff, err := time.ParseDuration(resolved.MaxBackoff); err == nil && maxBackoff > 0 {
		opts.MaxBackoff = maxBackoff
	}

	if resolved.BackoffFactor > 1 {
		opts.BackoffFactor = resolved.BackoffFactor
	}

	return opts
}

func commandContextOrBackground(cmd *cobra.Command) context.Context {
	return clibase.CommandContext(cmd)
}

func NewDaemonWindowStatusCommand() *cobra.Command {
	cmd := newAgentStatusRunCommand()
	cmd.Use = "agent-window-status"

	return cmd
}
