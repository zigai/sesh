package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var (
	errSensitiveRaw         = errors.New("sensitive raw internal path: /private/secret/key")
	errUnmappedHistoryCause = errors.New("open private history dir: unsafe private storage entry")
)

func TestSessionNotFoundErrorBoundary(t *testing.T) {
	t.Parallel()

	missing := &capture.SessionNotFoundError{ID: "missing"}
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"raw capture", missing, "sessionNotFound"},
		{"typed action", action.Wrap(action.CodeNotFound, missing), "notFound"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := jsonErrorCode(test.err); got != test.want {
				t.Fatalf("root error code = %q, want %q", got, test.want)
			}

			if got := clibase.JSONErrorCode(test.err); got != test.want {
				t.Fatalf("command error code = %q, want %q", got, test.want)
			}

			if message := safeCommandErrorMessage(test.err); message != missing.Error() {
				t.Fatalf("root diagnostic = %q, want %q", message, missing.Error())
			}

			if message := clibase.SafeCommandErrorMessage(test.err); message != missing.Error() {
				t.Fatalf("command diagnostic = %q, want %q", message, missing.Error())
			}
		})
	}
}

func TestJSONEnvelopeFormatAndTaxonomy(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	tests := []struct {
		name         string
		args         []string
		wantCode     string
		wantExitCode int
		wantSubstr   string
	}{
		{
			name:         "usage error unknown flag",
			args:         []string{snapshotCmd, "save", testJSONFlag, "--unknown"},
			wantCode:     "usageError",
			wantExitCode: exitCodeUsage,
			wantSubstr:   "unknown flag",
		},
		{
			name: "empty selection is a usage error",
			args: []string{
				snapshotCmd, "save",
				testRepositoryFlag, tempDir,
				testSocketFlag, "/tmp/nonexistent.sock",
				testLeaseRootFlag, filepath.Join(tempDir, "lease"),
				testJSONFlag,
			},
			wantCode:     "usageError",
			wantExitCode: exitCodeUsage,
			wantSubstr:   "invalid command usage",
		},
		{
			name: "endpoint required invalid socket",
			args: []string{
				snapshotCmd, "save",
				testRepositoryFlag, tempDir,
				testSocketFlag, "relative/socket/path",
				testLeaseRootFlag, filepath.Join(tempDir, "lease"),
				"$0",
				testJSONFlag,
			},
			wantCode:     "endpointRequired",
			wantExitCode: exitCodeGeneral,
			wantSubstr:   "tmux socket is required",
		},
		{
			name: "not found read nonexistent repo",
			args: []string{
				snapshotCmd, "list",
				testRepositoryFlag, filepath.Join(tempDir, "missing-repo"),
				testJSONFlag,
			},
			wantCode:     "notFound",
			wantExitCode: exitCodeGeneral,
			wantSubstr:   "repository does not exist",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exec := executeJSON(t, tc.args...)
			assertErrorEnvelope(t, exec, tc.wantCode, tc.wantExitCode, tc.wantSubstr)
		})
	}
}

func assertErrorEnvelope(t *testing.T, exec commandExecution, wantCode string, wantExitCode int, wantSubstr string) {
	t.Helper()

	assertErrorEnvelopeFields(t, exec, wantCode, wantExitCode, wantSubstr)
	assertErrorStreamDiscipline(t, exec)
}

func assertErrorEnvelopeFields(t *testing.T, exec commandExecution, wantCode string, wantExitCode int, wantSubstr string) {
	t.Helper()

	if exec.envelope.OK {
		t.Fatalf("expected error envelope (ok=false), got ok=true: %s", exec.stdout)
	}

	if exec.envelope.SchemaVersion != 2 {
		t.Fatalf("schemaVersion = %d, want 2", exec.envelope.SchemaVersion)
	}

	if exec.envelope.Error == nil {
		t.Fatalf("envelope.Error is nil: stderr=%q", exec.stderr)
	}

	if exec.envelope.Error.Code != wantCode {
		t.Fatalf("error.code = %q, want %q", exec.envelope.Error.Code, wantCode)
	}

	if exec.envelope.Error.ExitCode != wantExitCode {
		t.Fatalf("error.exitCode = %d, want %d", exec.envelope.Error.ExitCode, wantExitCode)
	}

	if !strings.Contains(exec.envelope.Error.Message, wantSubstr) {
		t.Fatalf("error.message = %q, want containing %q", exec.envelope.Error.Message, wantSubstr)
	}
}

func assertErrorStreamDiscipline(t *testing.T, exec commandExecution) {
	t.Helper()

	if exec.stdout != "" {
		t.Fatalf("stdout should be empty on error, got: %q", exec.stdout)
	}

	if exec.stderr == "" {
		t.Fatalf("stderr should receive error JSON envelope")
	}

	if !strings.Contains(exec.stderr, `"result": null`) {
		t.Fatalf("stderr missing explicit \"result\": null: %s", exec.stderr)
	}

	for _, forbidden := range []string{"goroutine ", ".go:", "runtime error:", "/private/"} {
		if strings.Contains(exec.envelope.Error.Message, forbidden) {
			t.Fatalf("error message leaked internal details: %q", exec.envelope.Error.Message)
		}
	}

	var rawMap map[string]any
	if err := json.Unmarshal([]byte(exec.stderr), &rawMap); err != nil {
		t.Fatalf("failed unmarshaling stderr into map: %v", err)
	}

	assertAllKeysCamelCase(t, rawMap, "$")
}

func TestUncertaintyOutranksCanceledInClassification(t *testing.T) {
	joined := errors.Join(store.ErrUncertain, context.Canceled)

	code := jsonErrorCode(joined)
	if code != "publicationUncertain" {
		t.Fatalf("jsonErrorCode(joined) = %q, want publicationUncertain", code)
	}

	exitCode := ExitCode(joined)
	if exitCode != exitCodeGeneral {
		t.Fatalf("ExitCode(joined) = %d, want %d (general)", exitCode, exitCodeGeneral)
	}

	var stderr bytes.Buffer

	root := newRootCommand(t.Context())
	root.SetErr(&stderr)

	if err := WriteCommandError(root, joined); err != nil {
		t.Fatalf("WriteCommandError returned error: %v", err)
	}

	if stderr.Len() == 0 {
		t.Fatal("WriteCommandError suppressed errors.Join(ErrUncertain, context.Canceled)")
	}

	stderr.Reset()

	if err := WriteCommandError(root, context.Canceled); err != nil {
		t.Fatalf("WriteCommandError returned error: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("WriteCommandError did not suppress plain context.Canceled: %s", stderr.String())
	}
}

func TestSafeCommandErrorMessagePrivacy(t *testing.T) {
	oopsErr := oops.In("cli").Public("arbitrary public string with /secret/path").Wrap(errSensitiveRaw)
	msg := safeCommandErrorMessage(oopsErr)

	if strings.Contains(msg, "/secret/path") || strings.Contains(msg, "/private/") {
		t.Fatalf("safeCommandErrorMessage leaked sensitive path: %q", msg)
	}

	if msg != "operation failed" {
		t.Fatalf("unknown error produced %q, want %q", msg, "operation failed")
	}

	if msg := safeCommandErrorMessage(store.ErrUncertain); msg != "snapshot publication durability uncertain" {
		t.Fatalf("ErrUncertain produced %q", msg)
	}

	if msg := safeCommandErrorMessage(errPartialSnapshot); msg != "parity-incomplete save" {
		t.Fatalf("errPartialSnapshot produced %q", msg)
	}
}

func TestHumanErrorMessageReportsUnmappedCause(t *testing.T) {
	wrapped := oops.In("cli").With("operation", "open_history").Wrap(errUnmappedHistoryCause)
	if msg := humanErrorMessage(wrapped); msg != errUnmappedHistoryCause.Error() {
		t.Fatalf("unmapped error produced %q, want its cause %q", msg, errUnmappedHistoryCause.Error())
	}

	if msg := humanErrorMessage(store.ErrUncertain); msg != "snapshot publication durability uncertain" {
		t.Fatalf("ErrUncertain produced %q", msg)
	}
}

func TestJsonErrorCodeTaxonomyMappings(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{store.ErrUncertain, "publicationUncertain"},
		{errPartialSnapshot, "partialSnapshot"},
		{store.ErrRecoveryRequired, "recoveryRequired"},
		{store.ErrCorrupt, "repositoryCorrupt"},
		{store.ErrUnavailable, "snapshotUnavailable"},
		{store.ErrConflict, "repositoryConflict"},
		{capture.ErrChanged, "captureChanged"},
		{capture.ErrSelection, "selectionRequired"},
		{capture.ErrUnsupported, "captureUnsupported"},
		{fmt.Errorf("capture: %w", gotmux.ErrOutputLimit), "outputLimitExceeded"},
		{os.ErrNotExist, "notFound"},
		{os.ErrPermission, "permissionDenied"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "canceled"},
		{errUnknownCommand, "unknownCommand"},
	}

	for _, tc := range cases {
		got := jsonErrorCode(tc.err)
		if got != tc.want {
			t.Errorf("jsonErrorCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestJSONFlagDetectionFollowsFinalParsedValue(t *testing.T) {
	t.Run("json flag as string value produces plain text", func(t *testing.T) {
		assertPlainTextErrorOutput(t, snapshotCmd, "save", testRepositoryFlag, testJSONFlag)
	})

	t.Run("json flag after terminator produces plain text", func(t *testing.T) {
		assertPlainTextErrorOutput(t, snapshotCmd, "save", "--", testJSONFlag)
	})

	t.Run("repeated boolean false wins produces plain text", func(t *testing.T) {
		assertPlainTextErrorOutput(t, snapshotCmd, "save", testJSONFlag, "--json=false", "--unknown")
	})

	t.Run("repeated boolean true wins produces json", func(t *testing.T) {
		exec := executeJSON(t, snapshotCmd, "save", "--json=false", testJSONFlag, "--unknown")
		if !strings.Contains(exec.stderr, "schemaVersion") {
			t.Fatalf("expected JSON error when --json is last, got:\n%s", exec.stderr)
		}

		if exec.envelope.Error == nil || exec.envelope.Error.Code != "usageError" {
			t.Fatalf("expected usageError code: %#v", exec.envelope.Error)
		}
	})

	t.Run("unknown flag before json produces json error", func(t *testing.T) {
		exec := executeJSON(t, snapshotCmd, "save", "--unknown", testJSONFlag)
		if !strings.Contains(exec.stderr, "schemaVersion") {
			t.Fatalf("expected JSON error when --unknown is before --json, got:\n%s", exec.stderr)
		}

		if exec.envelope.Error == nil || exec.envelope.Error.Code != "usageError" {
			t.Fatalf("expected usageError code: %#v", exec.envelope.Error)
		}
	})
}

func assertPlainTextErrorOutput(t *testing.T, args ...string) {
	t.Helper()

	stdout, stderr, err := executeCommand(t, args...)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if strings.Contains(stderr, "schemaVersion") || strings.Contains(stdout, "schemaVersion") {
		t.Fatalf("expected plain text error, got JSON:\n%s", stderr)
	}
}
