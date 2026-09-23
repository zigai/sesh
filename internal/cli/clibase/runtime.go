package clibase

import (
	"context"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/config"
)

type commandConfigKey struct{}

type ConfigurationError struct {
	err error
}

type CommandResultError struct {
	error
}

type SaveBoundaryError struct {
	err error
}

type RestoreBoundaryError struct {
	err error
}

func (e *ConfigurationError) Error() string {
	return e.err.Error()
}

func (e *ConfigurationError) Unwrap() error {
	return e.err
}

func (e *CommandResultError) Unwrap() error { return e.error }

func (e *CommandResultError) CommandResultError() bool { return true }

func (e *SaveBoundaryError) Error() string {
	return e.err.Error()
}

func (e *SaveBoundaryError) Unwrap() error {
	return e.err
}

func (e *RestoreBoundaryError) Error() string {
	return e.err.Error()
}

func (e *RestoreBoundaryError) Unwrap() error {
	return e.err
}

func (e *RestoreBoundaryError) RestoreOutcomeLogged() bool {
	return true
}

func NewCommandResultError(err error) error {
	if err == nil {
		return nil
	}

	return &CommandResultError{error: err}
}

func NewConfigurationError(err error) error {
	if err == nil {
		return nil
	}

	return &ConfigurationError{err: err}
}

func NewSaveBoundaryError(err error) error {
	if err == nil {
		return nil
	}

	return &SaveBoundaryError{err: err}
}

func NewRestoreBoundaryError(err error) error {
	if err == nil {
		return nil
	}

	return &RestoreBoundaryError{err: err}
}

func CommandContext(cmd *cobra.Command) context.Context {
	if cmd == nil {
		return context.Background()
	}

	ctx := cmd.Context()
	if ctx == nil {
		return context.Background()
	}

	return ctx
}

func LoadCommandConfig(ctx context.Context, cmd *cobra.Command) (*config.Config, error) {
	if cfg, ok := ctx.Value(commandConfigKey{}).(*config.Config); ok {
		return cfg.Clone(), nil
	}

	path, _ := cmd.Flags().GetString("config")
	noConfig, _ := cmd.Flags().GetBool("no-config")

	options := []config.LoadOption{config.WithExplicitPath(path), config.WithInput(cmd.InOrStdin())}
	if noConfig {
		options = append(options, config.WithoutConfig())
	}

	cfg, err := config.Load(options...)
	if err != nil {
		return nil, &ConfigurationError{err: oops.In("cli").Code("config_failed").With("operation", "load_configuration").Wrap(err)}
	}

	cmd.SetContext(context.WithValue(ctx, commandConfigKey{}, cfg.Clone()))

	return cfg, nil
}
