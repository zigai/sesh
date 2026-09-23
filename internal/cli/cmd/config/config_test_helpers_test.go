package cmdconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

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
		NewCommand(),
	)

	return rootCommand
}

func newRootCommand(ctx context.Context) *cobra.Command {
	return newTestRootCommand(ctx)
}

func executeApplication(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer) error {
	root := newTestRootCommand(ctx)
	root.SetContext(ctx)
	root.SetArgs(args)

	if input != nil {
		root.SetIn(input)
	}

	if stdout != nil {
		root.SetOut(stdout)
	}

	if stderr != nil {
		root.SetErr(stderr)
	}

	executed, err := root.ExecuteC()
	if err != nil {
		if executed == nil {
			executed = root
		}

		if writeErr := clibase.WriteCommandError(executed, err); writeErr != nil {
			err = errors.Join(err, writeErr)
		}
	}

	return err
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
	if err := json.Unmarshal([]byte(target), &envelope); err != nil {
		t.Fatalf("failed to parse JSON envelope: %v; raw=%q; stderr=%q", err, target, rawErr)
	}

	return commandExecution{
		stdout:   rawOut,
		stderr:   rawErr,
		err:      err,
		envelope: envelope,
	}
}

func decodeJSONPayload[T any](t *testing.T, payload json.RawMessage) T {
	t.Helper()

	var value T
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("failed to decode JSON payload: %v; raw=%s", err, string(payload))
	}

	return value
}
