// Package testenv keeps test processes away from the developer's live tmux server and sesh data.
package testenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolatedMarker tells helper subprocesses re-executing the test binary that they inherited an
// isolated environment; their SESH_ variables are fixture signals from the parent test.
const isolatedMarker = "SESH_TESTENV_ISOLATED"

// goToolchainVariables keep go subprocesses on the developer's caches and settings, which would
// otherwise move into the isolated home and re-download modules on every run.
var goToolchainVariables = []string{"GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"}

// RunIsolated runs the package tests with $HOME, the XDG directories, and $TMUX_TMPDIR in a
// temporary directory and $TMUX and every SESH_ variable cleared, so no fallback reaches the
// live tmux server or sesh data.
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

	if err := removeIsolatedHome(root); err != nil {
		fmt.Fprintln(os.Stderr, "remove isolated test home:", err)
	}

	return code
}

func isolate(root string) error {
	toolchain, err := goToolchainEnvironment()
	if err != nil {
		return err
	}

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

	for variable, value := range toolchain {
		if err := os.Setenv(variable, value); err != nil {
			return fmt.Errorf("set %s: %w", variable, err)
		}
	}

	if err := os.Setenv(isolatedMarker, "1"); err != nil {
		return fmt.Errorf("mark isolated environment: %w", err)
	}

	return nil
}

// goToolchainEnvironment resolves the effective go toolchain locations before isolation changes
// $HOME. Without a go command no test can start a go subprocess, so nothing needs pinning.
func goToolchainEnvironment() (map[string]string, error) {
	values := make(map[string]string, len(goToolchainVariables))

	gocmd, err := exec.LookPath("go")
	if errors.Is(err, exec.ErrNotFound) {
		return values, nil
	}

	if err != nil {
		return nil, fmt.Errorf("find go command: %w", err)
	}

	arguments := append([]string{"env", "-json"}, goToolchainVariables...)

	output, err := exec.CommandContext(context.Background(), gocmd, arguments...).Output()
	if err != nil {
		return nil, fmt.Errorf("resolve go toolchain environment: %w", err)
	}

	if err := json.Unmarshal(output, &values); err != nil {
		return nil, fmt.Errorf("decode go toolchain environment: %w", err)
	}

	return values, nil
}

// removeIsolatedHome deletes the isolated home. Tools such as the go module cache create
// read-only directories that [os.RemoveAll] cannot empty, so a failed removal makes every
// directory writable and retries.
func removeIsolatedHome(root string) error {
	err := os.RemoveAll(root)
	if err == nil {
		return nil
	}

	if writableErr := makeDirectoriesWritable(root); writableErr != nil {
		return errors.Join(err, writableErr)
	}

	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove writable isolated home: %w", err)
	}

	return nil
}

func makeDirectoriesWritable(root string) error {
	scope, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open isolated home: %w", err)
	}

	walkErr := fs.WalkDir(scope.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return err
		}

		return scope.Chmod(path, 0o700)
	})

	return errors.Join(walkErr, scope.Close())
}
