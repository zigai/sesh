package clibase

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/config"
)

const (
	SocketFlagUsage     = "Socket path of the tmux server [default: the current tmux server, else tmux's default socket]"
	RepositoryFlagUsage = "Snapshot repository directory [default: <data dir>/snapshots]"
	LeaseRootFlagUsage  = "Shared tmux endpoint lease directory [default: <data dir>/leases]"
	HistoryFlagUsage    = "Session history state file [default: $XDG_STATE_HOME/sesh/history.json]"
	StatusFileFlagUsage = "Final save status file [default: $XDG_STATE_HOME/sesh/autosave-status.json]"
)

// AddSocketFlag registers the optional --socket flag resolved by [ResolveSocket].
func AddSocketFlag(cmd *cobra.Command) {
	cmd.Flags().String("socket", "", SocketFlagUsage)
}

// ResolveSocket selects the tmux server: --socket, else the server named by $TMUX,
// else tmux's default socket for this user, matching what a plain `tmux` command targets.
func ResolveSocket(cmd *cobra.Command) string {
	if cmd.Flags().Lookup("socket") != nil {
		if socketPath, _ := cmd.Flags().GetString("socket"); socketPath != "" {
			return socketPath
		}
	}

	if socketPath, _, _ := strings.Cut(os.Getenv("TMUX"), ","); socketPath != "" {
		return socketPath
	}

	return DefaultTmuxSocket()
}

// DefaultTmuxSocket returns the socket tmux uses without -L or -S: $TMUX_TMPDIR (or /tmp)/tmux-UID/default.
func DefaultTmuxSocket() string {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" || !filepath.IsAbs(dir) {
		dir = "/tmp"
	}

	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	return filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), "default")
}

// ResolveRepository returns --repository as an absolute path, else the configured snapshot repository.
func ResolveRepository(cmd *cobra.Command) (string, error) {
	return resolveDataPath(cmd, "repository", "snapshots")
}

// ResolveLeaseRoot returns --lease-root as an absolute path, else the configured lease directory.
func ResolveLeaseRoot(cmd *cobra.Command) (string, error) {
	return resolveDataPath(cmd, "lease-root", "leases")
}

func resolveDataPath(cmd *cobra.Command, flag, defaultName string) (string, error) {
	value := ""
	if cmd.Flags().Lookup(flag) != nil {
		value, _ = cmd.Flags().GetString(flag)
	}

	if value == "" {
		cfg, err := LoadCommandConfig(cmd.Context(), cmd)
		if err != nil {
			return "", err
		}

		dataDir, err := config.GetDataDir(cfg)
		if err != nil {
			return "", oops.In("cli").With("operation", "resolve_data_dir").Wrap(err)
		}

		value = filepath.Join(dataDir, defaultName)
	}

	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", oops.In("cli").With("operation", "resolve_data_path").Wrap(err)
	}

	return absolute, nil
}
