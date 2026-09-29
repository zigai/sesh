package cmdsetup

import (
	"context"
	"errors"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/setup"
)

var errSetupConflicts = errors.New("some tmux keys are already bound")

type setupBindingJSON struct {
	Key      string   `json:"key"`
	Purpose  string   `json:"purpose"`
	Program  []string `json:"program"`
	Status   string   `json:"status"`
	Skipped  bool     `json:"skipped"`
	Conflict bool     `json:"conflict"`
}

type setupApplyResultJSON struct {
	Table    string             `json:"table"`
	Applied  int                `json:"applied"`
	Skipped  int                `json:"skipped"`
	Bindings []setupBindingJSON `json:"bindings"`
}

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Render or apply sesh environment scaffolding",
		Long: "Render or apply sesh environment scaffolding.\n\n" +
			"Setup prints configuration blocks and owned service definitions by default and only writes files when --apply is given. " +
			"Agent integrations are installed by AHT, never by duplicated hook installation.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: clibase.RequireSubcommand,
	}

	cmd.AddCommand(
		newSetupTmuxCommand(),
		newSetupAgentsCommand(),
		newSetupRefreshCommand(),
		newSetupAutosaveCommand(),
		newSetupRemoteCommand(),
		newSetupCoordinatorCommand(),
	)

	return cmd
}

func NewSetupCommand() *cobra.Command {
	return NewCommand()
}

func newSetupTmuxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tmux",
		Short: "Print or apply the sesh tmux key bindings",
		Long:  "Print or apply the sesh tmux key bindings.\n\nWithout --apply, the configuration block is printed and no tmux server is contacted. With --apply, the same bindings are registered through tmux, skipping keys that are already bound unless --force is given.",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  runSetupTmux,
	}

	cmd.Flags().Bool("apply", false, "Register the bindings on the target tmux server")
	cmd.Flags().Bool("force", false, "Overwrite keys that are already bound")
	clibase.AddSocketFlag(cmd)
	cmd.Flags().String("table", setup.DefaultTable, "Key table to target")
	cmd.Flags().String("next-key", "", "Override the configured key for the next-attention agent action")
	cmd.Flags().String("local-next-key", "", "Override the configured key for the current-session next-agent action")
	cmd.Flags().String("back-key", "", "Override the configured key for the agent navigation history action")
	cmd.Flags().String("picker-key", setup.DefaultPickerKey, "Key for the interactive session picker")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func resolveSetupBindings(cmd *cobra.Command) ([]setup.Binding, string, error) {
	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return nil, "", err
	}

	keys := setup.Keys{
		Next:      config.TmuxKeyName(cfg.AgentSessions.NextIdleKey),
		LocalNext: config.TmuxKeyName(cfg.AgentSessions.NextIdleLocalKey),
		Back:      config.TmuxKeyName(cfg.AgentSessions.NavigationBackKey),
		Picker:    setup.DefaultPickerKey,
	}

	for flag, target := range map[string]*string{
		"next-key":       &keys.Next,
		"local-next-key": &keys.LocalNext,
		"back-key":       &keys.Back,
		"picker-key":     &keys.Picker,
	} {
		if !cmd.Flags().Changed(flag) {
			continue
		}

		value, err := cmd.Flags().GetString(flag)
		if err != nil {
			return nil, "", oops.In("cli").With("operation", "read_setup_flag").Wrap(err)
		}

		*target = value
	}

	table, err := cmd.Flags().GetString("table")
	if err != nil {
		return nil, "", oops.In("cli").With("operation", "read_setup_flag").Wrap(err)
	}

	if err := setup.ValidateTable(table); err != nil {
		return nil, "", clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	bindings, err := setup.Bindings(keys)
	if err != nil {
		return nil, "", clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	return bindings, table, nil
}

func runSetupTmux(cmd *cobra.Command, _ []string) error {
	bindings, table, err := resolveSetupBindings(cmd)
	if err != nil {
		return err
	}

	apply, _ := cmd.Flags().GetBool("apply")
	if !apply {
		return renderSetupBlock(cmd, bindings, table)
	}

	return applySetupBindings(cmd, bindings, table)
}

func renderSetupBlock(cmd *cobra.Command, bindings []setup.Binding, table string) error {
	block := setup.RenderBlock(bindings, table)

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, setupApplyResultJSON{
			Table:    table,
			Applied:  0,
			Skipped:  0,
			Bindings: describeSetupBindings(bindings, nil),
		})
	}

	return clibase.WriteOutput(cmd, "%s", block)
}

func describeSetupBindings(bindings []setup.Binding, conflicts map[string]bool) []setupBindingJSON {
	described := make([]setupBindingJSON, 0, len(bindings))

	for _, binding := range bindings {
		conflict := conflicts[binding.Key]
		status := "planned"

		switch {
		case conflict:
			status = "skipped"
		case conflicts != nil:
			status = "applied"
		}

		described = append(described, setupBindingJSON{
			Key:      binding.Key,
			Purpose:  binding.Purpose,
			Program:  binding.Program,
			Status:   status,
			Skipped:  conflict,
			Conflict: conflict,
		})
	}

	return described
}

func applySetupBindings(cmd *cobra.Command, bindings []setup.Binding, table string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	server, err := integration.Open(clibase.ResolveSocket(cmd))
	if err != nil {
		return oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	existing, err := existingBindingKeys(ctx, server, table)
	if err != nil {
		return err
	}

	force, _ := cmd.Flags().GetBool("force")

	applied, conflicts, err := registerSetupBindings(ctx, server, bindings, table, existing, force)
	if err != nil {
		return err
	}

	return renderSetupApplyResult(cmd, table, applied, conflicts, bindings)
}

func registerSetupBindings(
	ctx context.Context,
	server *gotmux.Server,
	bindings []setup.Binding,
	table string,
	existing map[string]bool,
	force bool,
) (int, map[string]bool, error) {
	conflicts := make(map[string]bool)
	applied := 0

	for _, binding := range bindings {
		if existing[binding.Key] && !force {
			conflicts[binding.Key] = true

			continue
		}

		sequence, err := binding.CommandSequence()
		if err != nil {
			return 0, nil, oops.In("cli").With("operation", "build_binding").Wrap(err)
		}

		bindOptions := gotmux.BindOptions{Repeat: false, Note: binding.Purpose, ClearNote: false}

		if err := server.Bind(ctx, gotmux.KeyTable(table), gotmux.Key(binding.Key), sequence, bindOptions); err != nil {
			return 0, nil, oops.In("cli").With("operation", "apply_binding").Wrap(err)
		}

		applied++
	}

	return applied, conflicts, nil
}

func existingBindingKeys(ctx context.Context, server *gotmux.Server, table string) (map[string]bool, error) {
	bindings, err := server.BindingsWith(ctx, gotmux.BindingsOptions{
		Table:      gotmux.KeyTable(table),
		Key:        "",
		FirstMatch: false,
		NotesOnly:  false,
		Prefix:     "",
	})
	if err != nil {
		return nil, oops.In("cli").With("operation", "list_bindings").Wrap(err)
	}

	keys := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		keys[string(binding.Key)] = true
	}

	return keys, nil
}

func renderSetupApplyResult(cmd *cobra.Command, table string, applied int, conflicts map[string]bool, bindings []setup.Binding) error {
	payload := setupApplyResultJSON{
		Table:    table,
		Applied:  applied,
		Skipped:  len(conflicts),
		Bindings: describeSetupBindings(bindings, conflicts),
	}

	conflictErr := setupConflictError(len(conflicts))

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, payload, conflictErr)
	}

	if err := clibase.WriteOutput(cmd, "Applied %d binding(s) in table %q; skipped %d existing key(s)\n", applied, table, len(conflicts)); err != nil {
		return err
	}

	return conflictErr
}

func setupConflictError(skipped int) error {
	if skipped == 0 {
		return nil
	}

	return clibase.NewExitError(clibase.ExitCodeGeneral, errSetupConflicts)
}
