package cmdsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/logging"
	"github.com/zigai/sesh/internal/setup"
)

const (
	testSocketFlag   = "--socket"
	testJSONFlag     = "--json"
	testConfigFlag   = "--config"
	testNoConfigFlag = "--no-config"
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

	rootCommand.AddCommand(NewCommand())

	remoteCmd := &cobra.Command{Use: "remote"}
	monitorCmd := &cobra.Command{Use: "monitor"}
	remoteCmd.AddCommand(monitorCmd)
	rootCommand.AddCommand(remoteCmd)

	actionCmd := &cobra.Command{Use: "action"}
	pickerOpenCmd := &cobra.Command{
		Use:   "picker-open",
		Short: "Open the configured tmux popup for a key binding",
	}
	pickerOpenCmd.Flags().Bool("tmux", true, "Open inside a tmux popup")
	actionCmd.AddCommand(pickerOpenCmd)
	rootCommand.AddCommand(actionCmd)

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

func isolatedSetupHome(t *testing.T) {
	t.Helper()

	t.Setenv("TMUX", "")

	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
}

func runtimeConfig(t *testing.T, level string) (string, string) {
	t.Helper()
	base := t.TempDir()
	path, logPath := filepath.Join(base, "config.toml"), filepath.Join(base, "private-log.jsonl")

	data := fmt.Sprintf("[settings]\nlog_level = %q\n[paths]\nlog_file = %q\n", level, logPath)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return path, logPath
}

func singleRuntimeEnvelope(t *testing.T, data []byte) testJSONEnvelope {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(data))

	var env testJSONEnvelope
	if err := decoder.Decode(&env); err != nil {
		t.Fatalf("invalid JSON %q: %v", data, err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("extra output %q: %v", data, err)
	}

	return env
}

//nolint:cyclop,gocognit // Complex CLI refresh execution test fixture
func executeSetupRefresh(t *testing.T, manager setup.ServiceManager, arguments ...string) commandExecution {
	t.Helper()

	args := append([]string{"setup", "refresh", "--program", "/private-program-canary/sesh", testJSONFlag}, arguments...)
	root := newTestRootCommand(t.Context())

	cmd, _, err := root.Find([]string{"setup", "refresh"})
	if err != nil {
		t.Fatal(err)
	}

	cmd.RunE = func(cmd *cobra.Command, _ []string) error { return runSetupRefreshWithManager(cmd, manager) }

	var (
		stdout bytes.Buffer
		stderr bytes.Buffer
	)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	ctx := clibase.ContextWithRawArgs(t.Context(), args)
	root.SetContext(ctx)

	var (
		sink      *logging.Sink
		operation string
		targetCmd *cobra.Command
	)

	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		targetCmd = cmd

		operation = cmd.Annotations[clibase.AnnotationOperation]
		if operation == "" {
			return nil
		}

		cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
		if err != nil {
			return err
		}

		level, err := zerolog.ParseLevel(cfg.Settings.LogLevel)
		if err != nil {
			return oops.In("cli").With("operation", "configure_logging").Wrap(err)
		}

		if level == zerolog.Disabled {
			logger := zerolog.Nop()
			cmd.SetContext(logger.WithContext(cmd.Context()))

			return nil
		}

		logPath, err := config.GetLogFile(cfg)
		if err != nil {
			return oops.In("cli").With("operation", "configure_logging").Wrap(err)
		}

		logPath, err = filepath.Abs(logPath)
		if err != nil {
			return oops.In("cli").With("operation", "configure_logging").Wrap(err)
		}

		sink, err = logging.Open(logPath)
		if err != nil {
			return oops.In("cli").Code("logging_failed").With("operation", "configure_logging").Wrap(err)
		}

		sink.SetLevel(level)
		logger := sink.Logger()
		cmd.SetContext(logger.WithContext(cmd.Context()))

		msg := "snapshot command started"
		if cmd.Parent() == cmd.Root() {
			msg = "command started"
		}

		zerolog.Ctx(cmd.Context()).Info().Str("component", "cli").Str("operation", operation).Str("status", "started").Msg(msg)

		return nil
	}

	executed, runErr := root.ExecuteContextC(ctx)
	if executed == nil {
		executed = root
	}

	if runErr != nil {
		if writeErr := clibase.WriteCommandError(executed, runErr); writeErr != nil {
			runErr = errors.Join(runErr, writeErr)
		}
	}

	if sink != nil {
		logger := sink.Logger()
		logCtx := logger.WithContext(ctx)
		failMsg := "snapshot command failed"
		okMsg := "command finished"

		if targetCmd != nil && targetCmd.Parent() == targetCmd.Root() {
			failMsg = "command failed"
			okMsg = "command finished"
		}

		if runErr != nil {
			zerolog.Ctx(logCtx).Error().Str("component", "cli").Str("operation", operation).Str("status", "failed").Str("reason", clibase.JSONErrorCode(runErr)).Msg(failMsg)
		} else {
			zerolog.Ctx(logCtx).Info().Str("component", "cli").Str("operation", operation).Str("status", "ok").Msg(okMsg)
		}

		_ = sink.Close()
	}

	encoded := stdout.Bytes()
	if len(encoded) == 0 {
		encoded = stderr.Bytes()
	}

	return commandExecution{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		err:      runErr,
		envelope: singleRuntimeEnvelope(t, encoded),
	}
}

func assertRuntimeLog(t *testing.T, path, component, operation, level, status string, private ...string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range private {
		if bytes.Contains(data, []byte(value)) {
			t.Fatal("private value in log")
		}
	}

	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected start and one outcome: %s", data)
	}

	for i, line := range lines {
		wantLevel, wantStatus := "info", "started"
		if i == 1 {
			wantLevel, wantStatus = level, status
		}

		assertRuntimeRecord(t, line, component, operation, wantLevel, wantStatus)
	}
}

func assertRuntimeRecord(t *testing.T, line []byte, component, operation, level, status string) {
	t.Helper()

	var record map[string]any
	if err := json.Unmarshal(line, &record); err != nil {
		t.Fatal(err)
	}

	if record["component"] != component || record["operation"] != operation {
		t.Fatalf("wrong boundary: %s", line)
	}

	if record["level"] != level || record["status"] != status {
		t.Fatalf("wrong event: %s", line)
	}

	if status != "started" && status != "ok" && record["reason"] == nil && record["phase"] == nil && record["code"] == nil {
		t.Fatalf("missing fixed reason: %s", line)
	}

	assertRuntimeLogFields(t, record)
}

func assertRuntimeLogFields(t *testing.T, record map[string]any) {
	t.Helper()

	for key := range record {
		switch key {
		case "time", "level", "component", "operation", "status", "message", "reason", "phase", "code", "sessions", "windows", "panes", "error_code":
		case "verified":
			if _, ok := record[key].(bool); !ok || record["operation"] != "restore" || record["component"] != "snapshot" {
				t.Fatalf("invalid verification field: %#v", record)
			}
		case "terminal_text_omitted":
			if _, ok := record[key].(float64); !ok || record["operation"] != "save" || record["component"] != "snapshot" {
				t.Fatalf("invalid terminal text count: %#v", record)
			}
		default:
			t.Fatalf("unexpected log field: %s", key)
		}
	}
}
