package clibase_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/clibase"
)

func endpointCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()

	cmd := &cobra.Command{Use: "endpoint"}
	cmd.Flags().String("config", "", "")
	cmd.Flags().Bool("no-config", true, "")
	clibase.AddSocketFlag(cmd)
	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().String("lease-root", "", clibase.LeaseRootFlagUsage)
	cmd.SetContext(context.Background())

	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}

	return cmd
}

func TestResolveSocketPrefersFlagThenAmbientServerThenTmuxDefault(t *testing.T) {
	tmpdir := t.TempDir()
	t.Setenv("TMUX_TMPDIR", tmpdir)
	t.Setenv("TMUX", "/run/ambient.sock,42,0")

	if got := clibase.ResolveSocket(endpointCommand(t, "--socket", "/run/explicit.sock")); got != "/run/explicit.sock" {
		t.Fatalf("explicit socket = %q", got)
	}

	if got := clibase.ResolveSocket(endpointCommand(t)); got != "/run/ambient.sock" {
		t.Fatalf("ambient socket = %q", got)
	}

	t.Setenv("TMUX", "")

	want := filepath.Join(tmpdir, "tmux-"+strconv.Itoa(os.Getuid()), "default")
	if got := clibase.ResolveSocket(endpointCommand(t)); got != want {
		t.Fatalf("default socket = %q, want %q", got, want)
	}
}

func TestDefaultTmuxSocketIgnoresRelativeTmuxTmpdir(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", "relative")

	want := filepath.Join("/tmp", "tmux-"+strconv.Itoa(os.Getuid()), "default")
	if resolved, err := filepath.EvalSymlinks("/tmp"); err == nil {
		want = filepath.Join(resolved, "tmux-"+strconv.Itoa(os.Getuid()), "default")
	}

	if got := clibase.DefaultTmuxSocket(); got != want {
		t.Fatalf("DefaultTmuxSocket() = %q, want %q", got, want)
	}
}

func TestResolveWorkspacePathsDefaultToDataDirectory(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	workspace, err := clibase.ResolveWorkspace(endpointCommand(t))
	if err != nil {
		t.Fatal(err)
	}

	if workspace.Repository != filepath.Join(dataHome, "sesh", "snapshots") {
		t.Fatalf("default repository = %q", workspace.Repository)
	}

	if workspace.LeaseRoot != filepath.Join(dataHome, "sesh", "leases") {
		t.Fatalf("default lease root = %q", workspace.LeaseRoot)
	}

	explicit, err := clibase.ResolveWorkspace(endpointCommand(t, "--repository", "relative/repo"))
	if err != nil || !filepath.IsAbs(explicit.Repository) || filepath.Base(explicit.Repository) != "repo" {
		t.Fatalf("explicit repository = %q, %v", explicit.Repository, err)
	}
}
