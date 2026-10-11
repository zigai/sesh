package config

import (
	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
)

type initResultJSON struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage native sesh configuration",
		Long: "Manage native sesh configuration.\n\n" +
			"Settings start with built-in defaults. System configuration at /etc/sesh/config.toml " +
			"and the XDG user file override them in that order. " +
			"SESH_ environment values override files. --config or SESH_CONFIG selects one file instead of the discovered files; " +
			"--config takes precedence. --no-config skips files but still applies SESH_ values. " +
			"An absent system or user file is allowed; use 'sesh config init' to create the user file.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: clibase.RequireSubcommand,
	}

	cmd.AddCommand(newConfigInitCommand())

	return cmd
}

func newConfigInitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the documented native configuration template",
		Long: "Create the documented native configuration template.\n\n" +
			"The default target is $XDG_CONFIG_HOME/sesh/config.toml, falling back to ~/.config/sesh/config.toml. " +
			"Use --path to select a different target. The inherited --config flag is not read by this command.",
		Args: clibase.UsageArgs(cobra.NoArgs),
		Annotations: map[string]string{
			clibase.AnnotationOperation:      "config_init",
			clibase.AnnotationSkipConfigLoad: "true",
		},
		RunE: runConfigInit,
	}

	cmd.Flags().String("path", "", "Configuration file to create [default: $XDG_CONFIG_HOME/sesh/config.toml]")
	cmd.Flags().Bool("force", false, "Replace an existing regular configuration file")
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runConfigInit(cmd *cobra.Command, _ []string) error {
	path, err := cmd.Flags().GetString("path")
	if err != nil {
		return oops.In("cli").Code("config_init_failed").With("operation", "read_config_init_path").Wrap(err)
	}

	force, err := cmd.Flags().GetBool("force")
	if err != nil {
		return oops.In("cli").Code("config_init_failed").With("operation", "read_config_init_force").Wrap(err)
	}

	initializedPath, err := config.Init(
		config.WithInitPath(path),
		config.WithInitForce(force),
	)
	if err != nil {
		return oops.In("cli").Code("config_init_failed").With("operation", "initialize_configuration").Wrap(err)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, initResultJSON{
			Path:   initializedPath,
			Status: "initialized",
		})
	}

	return clibase.WriteOutput(cmd, "Initialized configuration at %s.\n", initializedPath)
}
