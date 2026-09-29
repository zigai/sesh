package cmdlogs_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/cli"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/logging"
)

func TestCanceledLogClearPreservesContents(t *testing.T) {
	cfg, path := runtimeConfig(t, "info")
	t.Setenv("SESH_PATHS_LOG_FILE", path)

	record := formattedInfoRecord + "\n"
	if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(ctx, []string{testConfigFlag, cfg, testLogsCmd, "--clear"}, nil, &stdout, &stderr)
	if !errors.Is(err, context.Canceled) || stdout.Len() != 0 {
		t.Fatalf("canceled clear accepted: %v, %q", err, stdout.String())
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != record {
		t.Fatalf("canceled clear modified log: %v, %q", readErr, data)
	}
}

func TestLogsValidateLineBoundBeforeConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(t.Context(), []string{
		testConfigFlag, filepath.Join(t.TempDir(), "missing.toml"), testLogsCmd,
		"--lines", strconv.Itoa(logging.MaxRetainedLogRecords + 1),
	}, nil, &stdout, &stderr)
	if clibase.ExitCode(err) != exitCodeUsage || stdout.Len() != 0 {
		t.Fatalf("line bound not validated first: %v, %q, %q", err, stdout.String(), stderr.String())
	}
}

func TestLogsExplicitFalseAllAllowsLineLimit(t *testing.T) {
	path := writeLogsTestFile(t, formattedInfoRecord+"\n")

	stdout, stderr, err := executeLogsCommand(t, path, testLogsCmd, "--all=false", "--lines", "1", "--json")
	if err != nil || stdout != formattedInfoRecord+"\n" || stderr != "" {
		t.Fatalf("explicit false option: %q, %q, %v", stdout, stderr, err)
	}
}

func TestLogsRuntimeEmitsBeforeCancellation(t *testing.T) {
	cfg, path := runtimeConfig(t, "info")
	t.Setenv("SESH_PATHS_LOG_FILE", path)

	record := formattedInfoRecord + "\n"
	if err := os.WriteFile(path, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	emitted := false
	writer := logReviewWriter(func(p []byte) (int, error) {
		if ctx.Err() != nil || string(p) != record {
			t.Errorf("record not emitted while running: %q, %v", p, ctx.Err())
		}

		emitted = true

		cancel()

		return len(p), nil
	})

	var stderr bytes.Buffer

	err := cli.ExecuteApplication(ctx, []string{testConfigFlag, cfg, testLogsCmd, "--watch", "--json"}, nil, writer, &stderr)
	if err != nil || !emitted || stderr.Len() != 0 {
		t.Fatalf("watch runtime: emitted=%v, %v, %q", emitted, err, stderr.String())
	}
}
