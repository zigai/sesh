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

// IsolatedMarker set to "1" makes [RunIsolated] keep a helper's SESH_ and tmux variables and only
// isolate unset directories; add it when a helper does not inherit the parent's environment.
const IsolatedMarker = "SESH_TESTENV_ISOLATED"

const (
	integrationRequiredSuffix = "_INTEGRATION_REQUIRED"
	goldenUpdatePrefix        = "SESH_UPDATE_"
)

var sessionVariables = []string{"TMUX", "TMUX_PANE", "TMUX_POPUP", "DBUS_SESSION_BUS_ADDRESS"}

var directoryVariables = map[string]string{
	"HOME":            "home",
	"XDG_CONFIG_HOME": "config",
	"XDG_DATA_HOME":   "data",
	"XDG_STATE_HOME":  "state",
	"XDG_CACHE_HOME":  "cache",
	"XDG_RUNTIME_DIR": "runtime",
	"TMUX_TMPDIR":     "tmux",
}

var goToolchainVariables = []string{"GOPATH", "GOMODCACHE", "GOCACHE", "GOENV"}

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
		if strings.HasPrefix(name, "SESH_") && !isTestControl(name) {
			if err := os.Unsetenv(name); err != nil {
				return fmt.Errorf("clear %s: %w", name, err)
			}
		}
	}

	// A tmux built with systemd support registers every new pane with the user manager over the
	// session bus.
	for _, variable := range sessionVariables {
		if err := os.Unsetenv(variable); err != nil {
			return fmt.Errorf("clear %s: %w", variable, err)
		}
	}

	if err := setDirectories(root, false); err != nil {
		return err
	}

	// Inside the isolated home, go would re-download modules on every run.
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

func isTestControl(name string) bool {
	return strings.HasSuffix(name, integrationRequiredSuffix) || strings.HasPrefix(name, goldenUpdatePrefix)
}

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

func disableGoTelemetry(configDir string) error {
	dir := filepath.Join(configDir, "go", "telemetry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create go telemetry directory: %w", err)
	}

	// Local mode starts a detached child that keeps writing here after go exits, racing the
	// removal of the isolated home.
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("off"), 0o600); err != nil {
		return fmt.Errorf("turn go telemetry off: %w", err)
	}

	return nil
}

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

func removeIsolatedHome(root string) error {
	err := os.RemoveAll(root)
	if err == nil {
		return nil
	}

	// The go module cache creates read-only directories that RemoveAll cannot empty.
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
