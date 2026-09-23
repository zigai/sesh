//go:build darwin

package lifecycle

import (
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/samber/oops"
)

const (
	LaunchdStopTimeoutGrace    = 5 * time.Second
	LaunchdAgentsDirectoryName = "LaunchAgents"
)

func LaunchAgentsDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", oops.In("lifecycle").Code("directory_resolve_failed").With("operation", "launch_agents_directory").Wrap(err)
	}

	return filepath.Join(home, "Library", LaunchdAgentsDirectoryName), nil
}

func ObserveFacts(inhibitorAvailable bool) Facts {
	manager := "none"

	if _, err := os.Stat(filepath.Join("/Library", "LaunchDaemons")); err == nil {
		manager = "launchd"
	}

	return Facts{
		GOOS:               runtime.GOOS,
		Manager:            manager,
		InhibitorAvailable: inhibitorAvailable,
		ManagedTmuxService: false,
		SameUserManager:    false,
		StopOrderDeclared:  false,
		SaverAfterTmux:     false,
	}
}

func PlatformGaps() []string {
	return []string{
		"an early logout or power-off notification requires an AppKit workspace notification on an owned main-thread run loop",
		"launchd stop ordering does not guarantee the final save runs before the saved processes exit",
		"sleep and wake handling is not implemented",
		"no native macOS runtime evidence exists for the managed tmux supervision path",
	}
}
