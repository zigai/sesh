package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
)

func TestLiveCommandOutputArrivesBeforeCompletion(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			testLiveCommandOutput(t, fail)
		})
	}
}

func testLiveCommandOutput(t *testing.T, fail bool) {
	t.Helper()
	cfg, logPath := runtimeConfig(t, "info")

	var stdout, stderr bytes.Buffer

	output := commandBuffer{output: &stdout, buffer: bytes.Buffer{}, failed: false, streaming: false}
	diagnostics := commandBuffer{output: &stderr, buffer: bytes.Buffer{}, failed: false, streaming: false}
	root := &cobra.Command{Use: "sesh"}
	cmd := &cobra.Command{
		Use: "live", Annotations: map[string]string{clibase.AnnotationLiveIO: "true", clibase.AnnotationOperation: "monitor"},
	}
	root.AddCommand(cmd)
	cmd.Flags().String("config", cfg, "")
	cmd.SetContext(t.Context())
	cmd.SetOut(&output)
	cmd.SetErr(&diagnostics)

	runtime := commandRuntime{cmd: root, sink: nil, operation: "", output: &output, diagnostics: &diagnostics}
	if err := runtime.prepare(cmd, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := cmd.OutOrStdout().Write([]byte("private-protocol-payload\n")); err != nil {
		t.Fatal(err)
	}

	if stdout.String() != "private-protocol-payload\n" || output.buffer.Len() != 0 {
		t.Fatal("live output was buffered")
	}

	var cause error

	level, status := "info", "ok"

	if fail {
		cause = errSensitiveRaw
		level, status = "error", "failed"
	}

	if err := runtime.finish(t.Context(), cause); err != nil {
		t.Fatal(err)
	}

	assertRuntimeLog(t, logPath, "cli", "monitor", level, status, "private-protocol-payload", "/private/secret/key", cfg)

	if stderr.Len() != 0 || strings.Contains(stdout.String(), `"level"`) {
		t.Fatal("logging contaminated live output")
	}
}

func TestRequiredFlagsRejectBeforeLiveLogging(t *testing.T) {
	cfg, logPath := runtimeConfig(t, "info")
	cmd := &cobra.Command{Use: "live", Annotations: map[string]string{clibase.AnnotationLiveIO: "true", clibase.AnnotationOperation: "monitor"}}
	cmd.Flags().String("config", cfg, "")
	cmd.Flags().String("socket", "", "")

	if err := cmd.MarkFlagRequired("socket"); err != nil {
		t.Fatal(err)
	}

	cmd.SetContext(t.Context())

	runtime := commandRuntime{cmd: cmd, sink: nil, operation: "", output: nil, diagnostics: nil}
	if err := runtime.prepare(cmd, nil); ExitCode(err) != exitCodeUsage {
		t.Fatalf("missing required flag: %v", err)
	}

	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("usage error opened log: %v", err)
	}
}

type loggedTestError struct{ error }

func (e loggedTestError) OutcomeLogged() bool { return true }
func (e loggedTestError) Unwrap() error       { return e.error }

func TestDomainOutcomeOwnershipRetainsIndependentFailures(t *testing.T) {
	logged := loggedTestError{error: errSensitiveRaw}
	unlogged := os.ErrPermission

	cases := []struct {
		name  string
		cause error
		want  bool
	}{
		{"logged", logged, true},
		{"all logged", errors.Join(logged, logged), true},
		{"cleanup", errors.Join(logged, unlogged), false},
		{"unlogged", unlogged, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandOutcomeLogged(tc.cause); got != tc.want {
				t.Fatalf("ownership = %t, want %t", got, tc.want)
			}
		})
	}
}
