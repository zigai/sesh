package cmdlogs_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/cli"
	"github.com/zigai/sesh/internal/cli/clibase"
)

const (
	testLogsCmd    = "logs"
	testConfigFlag = "--config"
	exitCodeUsage  = clibase.ExitCodeUsage
	exitCodeCancel = clibase.ExitCodeCancel
)

type logReviewWriter func([]byte) (int, error)

func (w logReviewWriter) Write(p []byte) (int, error) { return w(p) }

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

func executeLogsCommand(t *testing.T, path string, args ...string) (string, string, error) {
	t.Helper()

	return executeLogsCommandWithCancellation(t, false, path, args...)
}

func executeCanceledLogsCommand(t *testing.T, path string, args ...string) (string, string, error) {
	t.Helper()

	return executeLogsCommandWithCancellation(t, true, path, args...)
}

func executeLogsCommandWithCancellation(
	t *testing.T,
	canceled bool,
	path string,
	args ...string,
) (string, string, error) {
	t.Helper()
	t.Setenv("SESH_PATHS_LOG_FILE", path)

	configFile := filepath.Join(t.TempDir(), "config.toml")

	configContent := fmt.Sprintf("[paths]\nlog_file = %q\n", path)
	if err := os.WriteFile(configFile, []byte(configContent), 0o600); err != nil {
		t.Fatalf("WriteFile(config.toml) error = %v", err)
	}

	var stdout, stderr bytes.Buffer

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var output io.Writer = &stdout
	if canceled {
		output = logReviewWriter(func(p []byte) (int, error) {
			n, err := stdout.Write(p)

			cancel()

			return n, clibase.CommandOutputError(err)
		})
	}

	fullArgs := append([]string{"--config", configFile}, args...)

	err := cli.ExecuteApplication(ctx, fullArgs, nil, output, &stderr)

	return stdout.String(), stderr.String(), err
}

func writeLogsTestFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sesh.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	return path
}
