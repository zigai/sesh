package clibase_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	prettytable "github.com/jedib0t/go-pretty/v6/table"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/session"
)

var (
	errTestUsage   = errors.New("usage failure")
	errTestUnknown = errors.New("unknown flag: --foo")
	errTestGeneral = errors.New("general error")
)

func TestExitCode(t *testing.T) {
	t.Parallel()

	if code := clibase.ExitCode(nil); code != 0 {
		t.Fatalf("ExitCode(nil) = %d, want 0", code)
	}

	exitErr := clibase.NewExitError(clibase.ExitCodeUsage, errTestUsage)
	if code := clibase.ExitCode(exitErr); code != clibase.ExitCodeUsage {
		t.Fatalf("ExitCode(exitErr) = %d, want %d", code, clibase.ExitCodeUsage)
	}

	if code := clibase.ExitCode(syscall.EPIPE); code != clibase.ExitCodePipe {
		t.Fatalf("ExitCode(EPIPE) = %d, want %d", code, clibase.ExitCodePipe)
	}

	if code := clibase.ExitCode(context.Canceled); code != clibase.ExitCodeCancel {
		t.Fatalf("ExitCode(context.Canceled) = %d, want %d", code, clibase.ExitCodeCancel)
	}

	if code := clibase.ExitCode(errTestUnknown); code != clibase.ExitCodeUsage {
		t.Fatalf("ExitCode(unknown flag) = %d, want %d", code, clibase.ExitCodeUsage)
	}

	if code := clibase.ExitCode(errTestGeneral); code != clibase.ExitCodeGeneral {
		t.Fatalf("ExitCode(general error) = %d, want %d", code, clibase.ExitCodeGeneral)
	}
}

func TestContextWithRawArgs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	args := []string{"foo", "bar"}
	ctxWithArgs := clibase.ContextWithRawArgs(ctx, args)

	got, ok := clibase.RawArgsFromContext(ctxWithArgs)
	if !ok || len(got) != 2 || got[0] != "foo" || got[1] != "bar" {
		t.Fatalf("RawArgsFromContext() = (%v, %t), want (%v, true)", got, ok, args)
	}

	if _, ok := clibase.RawArgsFromContext(context.TODO()); ok {
		t.Fatal("RawArgsFromContext(nil) expected ok=false")
	}
}

func TestTableHelpers(t *testing.T) {
	t.Parallel()

	wrapped := clibase.WrapText("foo bar baz", 5)
	if len(wrapped) == 0 {
		t.Fatal("expected non-empty wrapped text")
	}

	wrappedID := clibase.WrapIdentifier("my-session-name", 5)
	if len(wrappedID) == 0 {
		t.Fatal("expected non-empty wrapped identifier")
	}

	wrappedSess := clibase.WrapSession("my session name", 5)
	if len(wrappedSess) == 0 {
		t.Fatal("expected non-empty wrapped session")
	}

	var buf bytes.Buffer

	cols := []clibase.TableColumn{
		{Heading: "Col1", Fixed: true, MinWidth: 5, Wrap: clibase.WrapText},
		{Heading: "Col2", Fixed: false, MinWidth: 5, Wrap: clibase.WrapText},
	}
	rows := []prettytable.Row{
		{"val1", "val2"},
	}

	if err := clibase.RenderResponsiveTable(&buf, 80, cols, rows, false); err != nil {
		t.Fatalf("RenderResponsiveTable failed: %v", err)
	}

	if buf.Len() == 0 {
		t.Fatal("expected non-empty rendered table")
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "─") || strings.Contains(buf.String(), "│") {
		t.Fatalf("table lacks one thin header rule: %q", buf.String())
	}
}

func TestJSONHelpers(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "test"}
	clibase.AddJSONFlag(cmd)

	if clibase.JSONOutputEnabled(cmd) {
		t.Fatal("JSONOutputEnabled should be false by default")
	}

	_ = cmd.Flags().Set(clibase.JSONFlagName, "true")
	if !clibase.JSONOutputEnabled(cmd) {
		t.Fatal("JSONOutputEnabled should be true after setting flag")
	}

	name := clibase.JSONCommandName(cmd)
	if name != "test" {
		t.Fatalf("JSONCommandName = %q, want 'test'", name)
	}

	code := clibase.JSONErrorCode(session.ErrSessionNotFound)
	if code != "notFound" {
		t.Fatalf("JSONErrorCode(ErrSessionNotFound) = %q, want 'notFound'", code)
	}

	msg := clibase.SafeCommandErrorMessage(session.ErrSessionNotFound)
	if msg != "session not found" {
		t.Fatalf("SafeCommandErrorMessage(ErrSessionNotFound) = %q, want 'session not found'", msg)
	}
}

func TestCommandContextAndConfig(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("config", "", "")
	cmd.Flags().Bool("no-config", true, "")

	ctx := clibase.CommandContext(cmd)
	if ctx == nil {
		t.Fatal("CommandContext returned nil")
	}

	cfg, err := clibase.LoadCommandConfig(ctx, cmd)
	if err != nil {
		t.Fatalf("LoadCommandConfig failed: %v", err)
	}

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	cfg2, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		t.Fatalf("LoadCommandConfig cached failed: %v", err)
	}

	if cfg2 == nil {
		t.Fatal("expected non-nil cached config")
	}

	tempDir := t.TempDir()
	invalidConfig := filepath.Join(tempDir, "invalid.toml")
	_ = os.WriteFile(invalidConfig, []byte("invalid = = = toml"), 0o600)

	invalidCmd := &cobra.Command{Use: "invalid"}
	invalidCmd.Flags().String("config", invalidConfig, "")
	invalidCmd.Flags().Bool("no-config", false, "")

	_, err = clibase.LoadCommandConfig(context.Background(), invalidCmd)
	if err == nil {
		t.Fatal("expected error loading invalid config, got nil")
	}

	if clibase.JSONErrorCode(err) != "configurationFailed" {
		t.Fatalf("JSONErrorCode(err) = %q, want 'configurationFailed'", clibase.JSONErrorCode(err))
	}
}
