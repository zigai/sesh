package cmddaemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
)

const testSocketFlag = "--socket"

func newTestRootCommand() *cobra.Command {
	rootCommand := &cobra.Command{
		Use:           "sesh",
		Short:         "Native tmux session persistence",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	rootCommand.PersistentFlags().String("config", "", "Explicit native TOML configuration file")
	rootCommand.PersistentFlags().Bool("no-config", false, "Use built-in settings without reading configuration files")
	rootCommand.AddCommand(NewCommand())

	return rootCommand
}

func TestDaemonServeRequiresExplicitSocket(t *testing.T) {
	t.Parallel()

	cmd := NewCommand()
	cmd.SetArgs([]string{"serve", testSocketFlag, "relative.sock"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.ExecuteContext(t.Context())
	if err == nil || clibase.ExitCode(err) != clibase.ExitCodeUsage {
		t.Fatalf("ExecuteContext error = %v, want usage error", err)
	}
}

func TestDaemonServeDeclaresLiveOperation(t *testing.T) {
	t.Parallel()

	cmd := NewServeCommand()
	if cmd.Annotations[clibase.AnnotationLiveIO] != "true" {
		t.Fatalf("live-io annotation = %q", cmd.Annotations[clibase.AnnotationLiveIO])
	}

	if cmd.Annotations[clibase.AnnotationOperation] != "daemon_serve" {
		t.Fatalf("operation annotation = %q", cmd.Annotations[clibase.AnnotationOperation])
	}
}

func TestDaemonRunRequiresExplicitSocketBeforeOpeningLogind(t *testing.T) {
	t.Parallel()

	cmd := newTestRootCommand()
	cmd.SetArgs([]string{
		"daemon", "run",
		testSocketFlag, "relative.sock",
		"--history", filepath.Join(t.TempDir(), "history.json"),
		"--no-config",
	})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.ExecuteContext(context.Background())
	if err == nil || clibase.ExitCode(err) != clibase.ExitCodeUsage {
		t.Fatalf("ExecuteContext error = %v, want usage error", err)
	}
}

func TestDaemonWaitReadyRequiresExplicitSocket(t *testing.T) {
	t.Parallel()

	cmd := NewCommand()
	cmd.SetArgs([]string{"wait-ready", testSocketFlag, "relative.sock"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.ExecuteContext(t.Context())
	if err == nil || clibase.ExitCode(err) != clibase.ExitCodeUsage {
		t.Fatalf("ExecuteContext error = %v, want usage error", err)
	}
}

func TestDaemonWaitReadyTimesOutWithoutServer(t *testing.T) {
	t.Parallel()

	cmd := NewCommand()
	cmd.SetArgs([]string{
		"wait-ready",
		testSocketFlag, filepath.Join(t.TempDir(), "nonexistent.sock"),
		"--timeout", "50ms",
	})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.ExecuteContext(t.Context())
	if err == nil {
		t.Fatal("wait-ready without answering server must fail")
	}
}
