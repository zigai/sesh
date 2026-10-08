package setup

import (
	"context"
	"errors"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/setup"
)

var errSetupConflicts = errors.New("some tmux keys are already bound")

type setupBindingJSON struct {
	Key      string   `json:"key"`
	Table    string   `json:"table"`
	Action   string   `json:"action"`
	Purpose  string   `json:"purpose"`
	Program  []string `json:"program"`
	Status   string   `json:"status"`
	Skipped  bool     `json:"skipped"`
	Conflict bool     `json:"conflict"`
}

type setupApplyResultJSON struct {
	Applied  int                `json:"applied"`
	Skipped  int                `json:"skipped"`
	Bindings []setupBindingJSON `json:"bindings"`
}

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Render or apply tmux bindings, agent integrations and services",
		Long: "Render or apply tmux bindings, agent integrations and services.\n\n" +
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
		newSetupServicesCommand(),
	)

	return cmd
}

func newSetupTmuxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "tmux",
		Short:       "Print or apply the sesh tmux key bindings",
		Long:        "Print or apply the sesh tmux key bindings configured by [[keys.bind]].\n\nWithout --apply, the configuration block is printed and no tmux server is contacted. With --apply, the same bindings are registered through tmux, skipping keys that are already bound unless --force is given.",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		RunE:        runSetupTmux,
		Annotations: map[string]string{clibase.AnnotationOperation: "setup_tmux"},
	}

	cmd.Flags().Bool("apply", false, "Register the bindings on the target tmux server")
	cmd.Flags().Bool("force", false, "Overwrite keys that are already bound")
	clibase.AddSocketFlag(cmd)
	clibase.AddJSONFlag(cmd)

	return cmd
}

func resolveSetupBindings(cmd *cobra.Command) ([]setup.Binding, error) {
	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return nil, err
	}

	bindings, err := action.Bindings(cfg)
	if err != nil {
		return nil, err
	}

	rendered, err := setup.Bindings(bindings)
	if err != nil {
		return nil, oops.In("cli").Code("config_invalid").With("operation", "render_tmux_bindings").Wrap(err)
	}

	return rendered, nil
}

func runSetupTmux(cmd *cobra.Command, _ []string) error {
	bindings, err := resolveSetupBindings(cmd)
	if err != nil {
		return err
	}

	apply, _ := cmd.Flags().GetBool("apply")
	if !apply {
		return renderSetupBlock(cmd, bindings)
	}

	return applySetupBindings(cmd, bindings)
}

func renderSetupBlock(cmd *cobra.Command, bindings []setup.Binding) error {
	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, setupApplyResultJSON{
			Applied:  0,
			Skipped:  0,
			Bindings: describeSetupBindings(bindings, nil),
		})
	}

	return clibase.WriteOutput(cmd, "%s", setup.RenderBlock(bindings))
}

func describeSetupBindings(bindings []setup.Binding, conflicts map[string]bool) []setupBindingJSON {
	described := make([]setupBindingJSON, 0, len(bindings))

	for _, binding := range bindings {
		conflict := conflicts[bindingSlot(binding)]
		status := "planned"

		switch {
		case conflict:
			status = "skipped"
		case conflicts != nil:
			status = "applied"
		}

		described = append(described, setupBindingJSON{
			Key:      binding.Key,
			Table:    binding.Table,
			Action:   binding.Action,
			Purpose:  binding.Purpose,
			Program:  binding.Program,
			Status:   status,
			Skipped:  conflict,
			Conflict: conflict,
		})
	}

	return described
}

func applySetupBindings(cmd *cobra.Command, bindings []setup.Binding) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	server, err := integration.Open(clibase.ResolveSocket(cmd))
	if err != nil {
		return oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	existing, err := existingBindingKeys(ctx, server, bindings)
	if err != nil {
		return err
	}

	force, _ := cmd.Flags().GetBool("force")

	applied, conflicts, err := registerSetupBindings(ctx, server, bindings, existing, force)
	if err != nil {
		return err
	}

	return renderSetupApplyResult(cmd, applied, conflicts, bindings)
}

func bindingSlot(binding setup.Binding) string {
	return binding.Table + " " + binding.Key
}

func registerSetupBindings(
	ctx context.Context,
	server *gotmux.Server,
	bindings []setup.Binding,
	existing map[string]bool,
	force bool,
) (int, map[string]bool, error) {
	conflicts := make(map[string]bool)
	applied := 0

	for _, binding := range bindings {
		if existing[bindingSlot(binding)] && !force {
			conflicts[bindingSlot(binding)] = true

			continue
		}

		sequence, err := binding.CommandSequence()
		if err != nil {
			return 0, nil, oops.In("cli").With("operation", "build_binding").Wrap(err)
		}

		bindOptions := gotmux.BindOptions{Repeat: false, Note: binding.Purpose, ClearNote: false}

		if err := server.Bind(ctx, gotmux.KeyTable(binding.Table), gotmux.Key(binding.Key), sequence, bindOptions); err != nil {
			return 0, nil, oops.In("cli").With("operation", "apply_binding").Wrap(err)
		}

		applied++
	}

	return applied, conflicts, nil
}

func existingBindingKeys(ctx context.Context, server *gotmux.Server, bindings []setup.Binding) (map[string]bool, error) {
	keys := make(map[string]bool)
	listed := make(map[string]bool)

	for _, binding := range bindings {
		if listed[binding.Table] {
			continue
		}

		listed[binding.Table] = true

		found, err := server.FindBindings(ctx, gotmux.BindingsOptions{
			Table:      gotmux.KeyTable(binding.Table),
			Key:        "",
			FirstMatch: false,
			NotesOnly:  false,
			Prefix:     "",
		})
		if err != nil {
			return nil, oops.In("cli").With("operation", "list_bindings").Wrap(err)
		}

		for _, existing := range found {
			keys[binding.Table+" "+config.TmuxKeyName(string(existing.Key))] = true
		}
	}

	return keys, nil
}

func renderSetupApplyResult(cmd *cobra.Command, applied int, conflicts map[string]bool, bindings []setup.Binding) error {
	payload := setupApplyResultJSON{
		Applied:  applied,
		Skipped:  len(conflicts),
		Bindings: describeSetupBindings(bindings, conflicts),
	}

	conflictErr := setupConflictError(len(conflicts))

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, payload, conflictErr)
	}

	if err := clibase.WriteOutput(cmd, "Applied %d binding(s); skipped %d existing key(s)\n", applied, len(conflicts)); err != nil {
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
