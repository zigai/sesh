// Package testenv keeps test processes away from the developer's live tmux server and sesh data.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolatedMarker tells helper subprocesses re-executing the test binary that they inherited an
// isolated environment; their SESH_ variables are fixture signals from the parent test.
const isolatedMarker = "SESH_TESTENV_ISOLATED"

// RunIsolated runs the package tests inside a private home: $HOME, the XDG base directories,
// $XDG_RUNTIME_DIR, and $TMUX_TMPDIR point at a temporary directory, $TMUX and every SESH_
// variable are cleared. Commands that fall back to the ambient tmux server, tmux's default
// socket, or the configured data directory therefore never reach live state.
func RunIsolated(m *testing.M) int {
	if os.Getenv(isolatedMarker) == "1" {
		return m.Run()
	}

	root, err := os.MkdirTemp("", "sesh-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create isolated test home:", err)

		return 1
	}

	code := 1

	if err := isolate(root); err != nil {
		fmt.Fprintln(os.Stderr, "isolate test environment:", err)
	} else {
		code = m.Run()
	}

	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintln(os.Stderr, "remove isolated test home:", err)
	}

	return code
}

func isolate(root string) error {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "SESH_") {
			if err := os.Unsetenv(name); err != nil {
				return fmt.Errorf("clear %s: %w", name, err)
			}
		}
	}

	if err := os.Unsetenv("TMUX"); err != nil {
		return fmt.Errorf("clear TMUX: %w", err)
	}

	directories := map[string]string{
		"HOME":            "home",
		"XDG_CONFIG_HOME": "config",
		"XDG_DATA_HOME":   "data",
		"XDG_STATE_HOME":  "state",
		"XDG_CACHE_HOME":  "cache",
		"XDG_RUNTIME_DIR": "runtime",
		"TMUX_TMPDIR":     "tmux",
	}

	for variable, name := range directories {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", variable, err)
		}

		if err := os.Setenv(variable, dir); err != nil {
			return fmt.Errorf("set %s: %w", variable, err)
		}
	}

	if err := os.Setenv(isolatedMarker, "1"); err != nil {
		return fmt.Errorf("mark isolated environment: %w", err)
	}

	return nil
}
