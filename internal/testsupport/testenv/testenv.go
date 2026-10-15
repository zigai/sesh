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

// IsolatedMarker marks a helper process that re-executes the test binary. Its SESH_ and tmux
// variables are fixture signals from the parent test, so [RunIsolated] keeps them and only
// isolates the directories the parent left unset. Add IsolatedMarker+"=1" to a helper's
// environment when it does not inherit the parent's.
const IsolatedMarker = "SESH_TESTENV_ISOLATED"

// integrationRequiredSuffix marks variables such as SESH_LIFECYCLE_INTEGRATION_REQUIRED that turn
// a missing integration dependency from a skip into a failure, so isolation must keep them.
const integrationRequiredSuffix = "_INTEGRATION_REQUIRED"

// sessionVariables point at the developer's live session: tmux sets the TMUX ones inside its panes
// and popups, and a tmux built with systemd support registers every new pane with the user
// manager over the session bus.
var sessionVariables = []string{"TMUX", "TMUX_PANE", "TMUX_POPUP", "DBUS_SESSION_BUS_ADDRESS"}

// directoryVariables name the locations through which sesh, tmux, and systemd reach the
// developer's data, live server, and user service manager.
var directoryVariables = map[string]string{
	"HOME":            "home",
	"XDG_CONFIG_HOME": "config",
	"XDG_DATA_HOME":   "data",
	"XDG_STATE_HOME":  "state",
	"XDG_CACHE_HOME":  "cache",
	"XDG_RUNTIME_DIR": "runtime",
	"TMUX_TMPDIR":     "tmux",
}

// goToolchainVariables keep go subprocesses on the developer's caches and settings, which would
// otherwise move into the isolated home and re-download modules on every run.
var goToolchainVariables = []string{"GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"}

// RunIsolated runs the package tests with $HOME, the XDG directories, and $TMUX_TMPDIR in a
// temporary directory and the live-session variables and every SESH_ variable except the
// integration requirements cleared, so no fallback reaches the live tmux server, the user's
// service manager, or sesh data.
func RunIsolated(m *testing.M) int {
	root, err := os.MkdirTemp("", "sesh-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create isolated test home:", err)

		return 1
	}

	var isolateErr error
	if os.Getenv(IsolatedMarker) == "1" {
		isolateErr = setDirectories(root, true)
	} else {
		isolateErr = isolate(root)
	}

	code := 1

	if isolateErr != nil {
		fmt.Fprintln(os.Stderr, "isolate test environment:", isolateErr)
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
		if strings.HasPrefix(name, "SESH_") && !strings.HasSuffix(name, integrationRequiredSuffix) {
			if err := os.Unsetenv(name); err != nil {
				return fmt.Errorf("clear %s: %w", name, err)
			}
		}
	}

	for _, variable := range sessionVariables {
		if err := os.Unsetenv(variable); err != nil {
			return fmt.Errorf("clear %s: %w", variable, err)
		}
	}

	if err := setDirectories(root, false); err != nil {
		return err
	}

	for variable, value := range toolchain {
		if err := os.Setenv(variable, value); err != nil {
			return fmt.Errorf("set %s: %w", variable, err)
		}
	}

	if err := os.Setenv(IsolatedMarker, "1"); err != nil {
		return fmt.Errorf("mark isolated environment: %w", err)
	}

	return nil
}

// setDirectories points each directory variable at its own directory under root. With keepSet,
// variables that already have a value keep it.
func setDirectories(root string, keepSet bool) error {
	for variable, name := range directoryVariables {
		if keepSet && os.Getenv(variable) != "" {
			continue
		}

		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", variable, err)
		}

		if err := os.Setenv(variable, dir); err != nil {
			return fmt.Errorf("set %s: %w", variable, err)
		}

		if variable == "XDG_CONFIG_HOME" {
			if err := disableGoTelemetry(dir); err != nil {
				return err
			}
		}
	}

	return nil
}

// disableGoTelemetry turns go telemetry off in an isolated config directory. Its default local
// mode starts a detached child that keeps writing there after the go command exits, racing the
// removal of the isolated home.
func disableGoTelemetry(configDir string) error {
	dir := filepath.Join(configDir, "go", "telemetry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create go telemetry directory: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("off"), 0o600); err != nil {
		return fmt.Errorf("turn go telemetry off: %w", err)
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
