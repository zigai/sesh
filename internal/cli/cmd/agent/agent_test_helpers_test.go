package cmdagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/samber/oops"

	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
)

const (
	testJSONFlag     = "--json"
	testConfigFlag   = "--config"
	testNoConfigFlag = "--no-config"
	actionCmd        = "action"
	exitCodeUsage    = clibase.ExitCodeUsage
)

type testJSONEnvelope struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Command       string                    `json:"command"`
	OK            bool                      `json:"ok"`
	Result        json.RawMessage           `json:"result"`
	Error         *clibase.JSONErrorPayload `json:"error"`
}

func newTestRootCommand(_ context.Context) *cobra.Command {
	rootCommand := &cobra.Command{
		Use:           "sesh",
		Short:         "Native tmux session persistence",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	rootCommand.PersistentFlags().String("config", "", "Explicit native TOML configuration file")
	rootCommand.PersistentFlags().Bool("no-config", false, "Use built-in settings without reading configuration files")

	actionCommand := &cobra.Command{
		Use:  actionCmd,
		Long: "Invoke a named action used by bindings and the picker.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}

			return clibase.UnknownCommandError(cmd, args[0])
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmd.Help(); err != nil {
				return oops.In("cli").With("operation", "show_help").Wrapf(err, "show help")
			}

			return nil
		},
	}

	actionCommand.AddCommand(
		NewActionNextCommand(),
		NewActionBackCommand(),
		NewActionFocusCommand(),
		NewActionStatusCommand(),
	)

	rootCommand.AddCommand(actionCommand)

	return rootCommand
}

func newRootCommand(ctx context.Context) *cobra.Command {
	return newTestRootCommand(ctx)
}

func executeCommand(root *cobra.Command, args ...string) (string, string, error) {
	var (
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	ctx := clibase.ContextWithRawArgs(context.Background(), args)
	root.SetContext(ctx)

	executed, err := root.ExecuteC()
	if err != nil {
		if executed == nil {
			executed = root
		}

		if writeErr := clibase.WriteCommandError(executed, err); writeErr != nil {
			err = errors.Join(err, writeErr)
		}
	}

	return stdout.String(), stderr.String(), err
}

func decodeJSONPayload[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()

	var val T
	if err := json.Unmarshal(raw, &val); err != nil {
		t.Fatalf("failed to unmarshal JSON payload: %v\nraw: %s", err, string(raw))
	}

	return val
}

func ExitCode(err error) int {
	return clibase.ExitCode(err)
}
