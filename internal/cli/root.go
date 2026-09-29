package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/zigai/sesh/internal/cli/clibase"

	cmdautosave "github.com/zigai/sesh/internal/cli/cmd/autosave"
	cmdconfig "github.com/zigai/sesh/internal/cli/cmd/config"
	cmddaemon "github.com/zigai/sesh/internal/cli/cmd/daemon"
	cmddoctor "github.com/zigai/sesh/internal/cli/cmd/doctor"
	cmdlogs "github.com/zigai/sesh/internal/cli/cmd/logs"
	cmdpicker "github.com/zigai/sesh/internal/cli/cmd/picker"
	cmdremote "github.com/zigai/sesh/internal/cli/cmd/remote"
	cmdsession "github.com/zigai/sesh/internal/cli/cmd/session"
	cmdsetup "github.com/zigai/sesh/internal/cli/cmd/setup"
	cmdsnapshot "github.com/zigai/sesh/internal/cli/cmd/snapshot"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

var (
	version               = "dev"
	commit                = "none"
	date                  = "unknown"
	disableCommandSorting sync.Once
)

var hookEvents = []string{
	session.EventSessionSwitchPre,
	session.EventSessionSwitchPost,
	session.EventSessionAttachPre,
	session.EventSessionAttachPost,
	session.EventTmuxStartupPost,
}

var (
	errUnknownHookEvent  = errors.New("unknown hook event")
	errNoHooksConfigured = errors.New("no hooks configured for event")
	errHelpShorthand     = errors.New("-h is not a help flag; use --help")
)

type hookCommandJSON struct {
	Event    string `json:"event"`
	Commands int    `json:"commands"`
}

type hooksResultJSON struct {
	TimeoutSeconds int               `json:"timeoutSeconds"`
	Events         []hookCommandJSON `json:"events"`
}

type hookRunResultJSON struct {
	Event     string `json:"event"`
	Attempted int    `json:"attempted"`
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
	TimedOut  int    `json:"timedOut"`
	Status    string `json:"status"`
}

type keyBindingJSON struct {
	Name string `json:"name"`
	Bind string `json:"bind"`
}

func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := executeApplication(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}

	stop()

	if err != nil {
		os.Exit(ExitCode(err))
	}
}

func NewRootCommand(ctx context.Context) *cobra.Command {
	return newRootCommand(ctx)
}

func newRootCommand(ctx context.Context) *cobra.Command {
	disableCommandSorting.Do(func() {
		cobra.EnableCommandSorting = false
	})

	rootCommand := &cobra.Command{
		Use:           "sesh",
		Short:         "Native tmux session persistence",
		SilenceUsage:  true,
		SilenceErrors: true,
		CompletionOptions: cobra.CompletionOptions{
			HiddenDefaultCmd: true,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			showVersion, err := cmd.Flags().GetBool("version")
			if err != nil {
				return oops.In("cli").With("operation", "read_version_flag").Wrapf(err, "read version flag")
			}

			if showVersion {
				_, err := fmt.Fprintf(
					cmd.OutOrStdout(),
					"sesh %s (commit: %s, built: %s)\n",
					version,
					commit,
					date,
				)
				if err != nil {
					return oops.In("cli").With("operation", "print_version").Wrapf(err, "print version")
				}

				return nil
			}

			if err := cmd.Help(); err != nil {
				return oops.In("cli").With("operation", "show_help").Wrapf(err, "show help")
			}

			return nil
		},
	}
	rootCommand.Flags().BoolP("version", "V", false, "Print version")
	rootCommand.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		if errors.Is(err, pflag.ErrHelp) {
			return NewExitError(exitCodeUsage, errHelpShorthand)
		}

		return err
	})
	rootCommand.PersistentFlags().String("config", "", "Explicit native TOML configuration file")
	rootCommand.PersistentFlags().Bool("no-config", false, "Use built-in settings without reading configuration files")
	rootCommand.AddCommand(
		cmdpicker.NewPickCommand(ctx, cmdremote.DefaultPickerRemoteFactory),
		cmdsession.NewListCommand(),
		cmdsnapshot.NewCommand(),
		cmdautosave.NewCommand(),
		cmdsession.NewHistoryCommand(),
		cmdremote.NewCommand(),
		newKeysCommand(),
		cmdlogs.NewCommand(),
		newHooksCommand(),
		cmdconfig.NewCommand(),
		cmdsetup.NewCommand(),
		cmddaemon.NewCommand(),
		newActionCommand(ctx),
		cmddoctor.NewCommand(),
	)
	rootCommand.InitDefaultHelpCmd()
	rootCommand.InitDefaultCompletionCmd()
	configureCommandHelp(rootCommand)

	return rootCommand
}

func newHooksCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Inspect and run configured lifecycle hooks",
		Long:  "Inspect and run configured lifecycle hooks.\n\nHook configuration is validated against the same limits the workflow enforces. Nothing runs unless an explicit event is requested.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHooksList(cmd)
		},
	}

	cmd.Flags().Bool("validate", false, "Validate hook configuration without running anything")
	addJSONFlag(cmd)

	cmd.AddCommand(newHooksRunCommand())

	return cmd
}

func newHooksRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <event>",
		Short: "Run the hooks configured for one lifecycle event",
		Long:  "Run the hooks configured for one lifecycle event.\n\nEach hook has its own deadline; a failing hook is reported and does not silently stop the remaining ones. Cancellation stops subsequent hooks.",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE:  runHooksRun,
	}

	cmd.Flags().String("session", "", "Session name passed to the hook environment")
	cmd.Flags().String("prev-session", "", "Previous session name passed to the hook environment")
	cmd.Flags().String("source", "cli", "Invocation source passed to the hook environment")
	addJSONFlag(cmd)

	return cmd
}

func loadHookConfig(cmd *cobra.Command) (*config.Config, error) {
	cfg, err := loadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

func hookEventSummary(cfg *config.Config) []hookCommandJSON {
	summary := make([]hookCommandJSON, 0, len(hookEvents))

	for _, event := range hookEvents {
		summary = append(summary, hookCommandJSON{
			Event:    event,
			Commands: len(session.CommandsForHookEvent(cfg.Hooks, event)),
		})
	}

	return summary
}

func hookTimeoutSeconds(cfg *config.Config) int {
	resolved, err := session.ResolveHookTimeout(cfg.Hooks.Timeout, "")
	if err != nil {
		return int(session.DefaultHookTimeout.Seconds())
	}

	return int(resolved.Seconds())
}

func runHooksList(cmd *cobra.Command) error {
	cfg, err := loadHookConfig(cmd)
	if err != nil {
		return err
	}

	validate, _ := cmd.Flags().GetBool("validate")

	if validate {
		for _, event := range hookEvents {
			if err := session.ValidateHookConfig(cfg.Hooks, event); err != nil {
				return oops.In("cli").With("operation", "validate_hooks", "event", event).Wrap(err)
			}
		}
	}

	summary := hookEventSummary(cfg)
	timeout := hookTimeoutSeconds(cfg)

	if jsonOutputEnabled(cmd) {
		return writeJSONResult(cmd, hooksResultJSON{TimeoutSeconds: timeout, Events: summary})
	}

	var out []byte

	out = append(out, fmt.Sprintf("Timeout: %ds\n", timeout)...)

	for _, event := range summary {
		out = append(out, fmt.Sprintf("%-22s %d\n", event.Event, event.Commands)...)
	}

	return writeOutput(cmd, "%s", string(out))
}

func runHooksRun(cmd *cobra.Command, args []string) error {
	hookConfig, event, err := prepareHookRun(cmd, args[0])
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	result, runErr := session.RunHooks(ctx, hookConfig, event)

	if runErr == nil && (result.Failed > 0 || result.TimedOut > 0) {
		runErr = result.Err()
	}

	return renderHookRun(cmd, event.Name, result, runErr)
}

func prepareHookRun(cmd *cobra.Command, eventName string) (config.HookConfig, session.HookEvent, error) {
	action, phase := session.HookEventDefaults(eventName)
	if action == "" {
		return config.HookConfig{}, session.HookEvent{}, NewExitError(exitCodeUsage, errUnknownHookEvent)
	}

	cfg, err := loadHookConfig(cmd)
	if err != nil {
		return config.HookConfig{}, session.HookEvent{}, err
	}

	if err := session.ValidateHookConfig(cfg.Hooks, eventName); err != nil {
		return config.HookConfig{}, session.HookEvent{}, oops.In("cli").With("operation", "validate_hooks", "event", eventName).Wrap(err)
	}

	if len(session.CommandsForHookEvent(cfg.Hooks, eventName)) == 0 {
		return config.HookConfig{}, session.HookEvent{}, NewExitError(exitCodeGeneral, errNoHooksConfigured)
	}

	return cfg.Hooks, hookEventFromFlags(cmd, eventName, action, phase), nil
}

func renderHookRun(cmd *cobra.Command, eventName string, result session.HookResult, runErr error) error {
	if jsonOutputEnabled(cmd) {
		payload := hookRunResultJSON{
			Event:     eventName,
			Attempted: result.Attempted,
			Completed: result.Completed,
			Failed:    result.Failed,
			TimedOut:  result.TimedOut,
			Status:    hookRunStatus(result),
		}

		if runErr != nil {
			return writeJSONResultError(cmd, payload, runErr)
		}

		return writeJSONResult(cmd, payload)
	}

	summary := fmt.Sprintf(
		"Hooks %s: attempted %d, completed %d, failed %d, timed out %d (%s)\n",
		eventName, result.Attempted, result.Completed, result.Failed, result.TimedOut, hookRunStatus(result),
	)

	if runErr != nil {
		if _, err := fmt.Fprint(cmd.ErrOrStderr(), summary); err != nil {
			return errors.Join(err, runErr)
		}

		return oops.In("cli").With("operation", "run_hooks", "event", eventName).Wrap(runErr)
	}

	return writeOutput(cmd, "%s", summary)
}

func hookEventFromFlags(cmd *cobra.Command, eventName, action, phase string) session.HookEvent {
	sessionName, _ := cmd.Flags().GetString("session")
	prevSession, _ := cmd.Flags().GetString("prev-session")
	source, _ := cmd.Flags().GetString("source")

	return session.HookEvent{
		Name:          eventName,
		Phase:         phase,
		Action:        action,
		PrevSession:   prevSession,
		Session:       sessionName,
		PrevSessionID: "",
		SessionID:     "",
		Source:        source,
	}
}

func hookRunStatus(result session.HookResult) string {
	switch {
	case result.Failed > 0 || result.TimedOut > 0:
		return "degraded"

	case result.Completed == result.Attempted && result.Attempted > 0:
		return "ok"
	default:
		return "partial"
	}
}

func newKeysCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keys",
		Short: "List configured key bindings",
		Long:  "List configured key bindings.\n\nBindings are read from configuration; this command does not modify any tmux key table. Use 'sesh setup tmux' to register the sesh tmux bindings.",
		Args:  usageArgs(cobra.NoArgs),
		RunE:  func(cmd *cobra.Command, _ []string) error { return runKeysList(cmd) },
	}

	addJSONFlag(cmd)

	return cmd
}

func keyBindings(cfg *config.Config) []keyBindingJSON {
	keys := cfg.Keys

	bindings := []keyBindingJSON{
		{Name: "create", Bind: keys.Create},
		{Name: "rename", Bind: keys.Rename},
		{Name: "kill", Bind: keys.Kill},
		{Name: "delete", Bind: keys.Delete},
		{Name: "snapshot", Bind: keys.Snapshot},
		{Name: "help", Bind: keys.Help},
		{Name: "cycle_mode", Bind: keys.CycleMode},
		{Name: "agent_sort", Bind: keys.AgentSort},
		{Name: "page_up", Bind: keys.PageUp},
		{Name: "page_down", Bind: keys.PageDown},
		{Name: "top", Bind: keys.Top},
		{Name: "bottom", Bind: keys.Bottom},
		{Name: "select_1", Bind: keys.Select1},
		{Name: "select_2", Bind: keys.Select2},
		{Name: "select_3", Bind: keys.Select3},
		{Name: "select_4", Bind: keys.Select4},
		{Name: "select_5", Bind: keys.Select5},
		{Name: "select_6", Bind: keys.Select6},
		{Name: "select_7", Bind: keys.Select7},
		{Name: "select_8", Bind: keys.Select8},
		{Name: "select_9", Bind: keys.Select9},
		{Name: "select_0", Bind: keys.Select0},
	}

	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Name < bindings[j].Name })

	return bindings
}

func runKeysList(cmd *cobra.Command) error {
	cfg, err := loadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	bindings := keyBindings(cfg)

	if jsonOutputEnabled(cmd) {
		return writeJSONResult(cmd, struct {
			Bindings []keyBindingJSON `json:"bindings"`
		}{Bindings: bindings})
	}

	var out []byte

	for _, binding := range bindings {
		out = append(out, fmt.Sprintf("%-14s %s\n", binding.Name, binding.Bind)...)
	}

	return writeOutput(cmd, "%s", string(out))
}
