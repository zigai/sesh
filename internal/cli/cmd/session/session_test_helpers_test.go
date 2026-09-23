package cmdsession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
)

const (
	testRepositoryFlag = "--repository"
	testSocketFlag     = "--socket"
	testLeaseRootFlag  = "--lease-root"
	testSessionFlag    = "--session"
	testJSONFlag       = "--json"
	testConfigFlag     = "--config"
	testNoConfigFlag   = "--no-config"
)

var camelCaseKeyPattern = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)

type testJSONEnvelope struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Command       string                    `json:"command"`
	OK            bool                      `json:"ok"`
	Result        json.RawMessage           `json:"result"`
	Error         *clibase.JSONErrorPayload `json:"error"`
}

type commandExecution struct {
	stdout   string
	stderr   string
	err      error
	envelope testJSONEnvelope
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

	rootCommand.AddCommand(
		NewListCommand(),
		NewHistoryCommand(),
	)

	actionCommand := &cobra.Command{
		Use:  "action",
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
		NewActionSessionNewCommand(),
		NewActionSessionRenameCommand(),
		NewActionSessionKillCommand(),
		NewActionSessionDeleteCommand(),
		NewActionSessionSwitchCommand(),
	)

	rootCommand.AddCommand(actionCommand)

	return rootCommand
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

func executeJSON(t *testing.T, args ...string) commandExecution {
	t.Helper()

	root := newTestRootCommand(t.Context())

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

	rawOut := stdout.String()
	rawErr := stderr.String()

	target := rawOut
	if target == "" {
		target = rawErr
	}

	var envelope testJSONEnvelope
	if target != "" {
		_ = json.Unmarshal([]byte(target), &envelope)
	}

	return commandExecution{
		stdout:   rawOut,
		stderr:   rawErr,
		err:      err,
		envelope: envelope,
	}
}

func decodeJSONPayload[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()

	var val T
	if err := json.Unmarshal(raw, &val); err != nil {
		t.Fatalf("failed to unmarshal JSON payload: %v\nraw: %s", err, string(raw))
	}

	return val
}

func assertAllKeysCamelCase(t *testing.T, val any, path string) {
	t.Helper()

	switch v := val.(type) {
	case map[string]any:
		for k, child := range v {
			if !camelCaseKeyPattern.MatchString(k) {
				t.Fatalf("key %q at path %q is not lowerCamelCase", k, path)
			}

			assertAllKeysCamelCase(t, child, path+"."+k)
		}
	case []any:
		for i, child := range v {
			assertAllKeysCamelCase(t, child, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}
