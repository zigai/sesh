package clibase

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/app"
)

const (
	SocketFlagUsage     = "Socket path of the tmux server [default: the current tmux server, else tmux's default socket]"
	RepositoryFlagUsage = "Snapshot repository directory [default: <data dir>/snapshots]"
	LeaseRootFlagUsage  = "Shared tmux endpoint lease directory [default: <data dir>/leases]"
	HistoryFlagUsage    = "Session history state file [default: $XDG_STATE_HOME/sesh/history.json]"
	StatusFileFlagUsage = "Final save status file [default: $XDG_STATE_HOME/sesh/autosave-status.json]"
)

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

func ResolveWorkspace(cmd *cobra.Command) (app.Workspace, error) {
	cfg, err := LoadCommandConfig(CommandContext(cmd), cmd)
	if err != nil {
		return app.Workspace{}, err
	}

	workspace, err := app.Resolve(cfg, CommandLocations(cmd))
	if err != nil {
		return app.Workspace{}, oops.In("cli").With("operation", "resolve_workspace").Wrap(err)
	}

	return workspace, nil
}

func CommandLocations(cmd *cobra.Command) app.Locations {
	return app.Locations{
		Socket:          ResolveSocket(cmd),
		Repository:      definedStringFlag(cmd, "repository"),
		LeaseRoot:       definedStringFlag(cmd, "lease-root"),
		History:         definedStringFlag(cmd, "history"),
		FinalSaveStatus: definedStringFlag(cmd, "status-file"),
		AgentStore:      definedStringFlag(cmd, "agent-state"),
	}
}

func definedStringFlag(cmd *cobra.Command, name string) string {
	if cmd.Flags().Lookup(name) == nil {
		return ""
	}

	value, _ := cmd.Flags().GetString(name)

	return value
}
