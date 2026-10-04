package testenv

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolateKeepsGoToolchainLocations(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go command unavailable")
	}

	want, err := goToolchainEnvironment()
	if err != nil {
		t.Fatal(err)
	}

	restoreIsolatedVariables(t)

	root := t.TempDir()
	if err := isolate(root); err != nil {
		t.Fatal(err)
	}

	if home := os.Getenv("HOME"); home != filepath.Join(root, "home") {
		t.Fatalf("HOME = %q, want isolated home", home)
	}

	got, err := goToolchainEnvironment()
	if err != nil {
		t.Fatal(err)
	}

	for _, variable := range goToolchainVariables {
		if got[variable] != want[variable] || strings.HasPrefix(got[variable], root) {
			t.Fatalf("%s = %q after isolation, want %q", variable, got[variable], want[variable])
		}
	}
}

func TestIsolateClearsLiveSessionVariables(t *testing.T) {
	restoreIsolatedVariables(t)

	t.Setenv("TMUX", "/tmp/tmux-1000/default,1234,0")
	t.Setenv("TMUX_PANE", "%7")
	t.Setenv("TMUX_POPUP", "1")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")

	if err := isolate(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	for _, variable := range []string{"TMUX", "TMUX_PANE", "TMUX_POPUP", "DBUS_SESSION_BUS_ADDRESS"} {
		if value, set := os.LookupEnv(variable); set {
			t.Fatalf("%s = %q after isolation, want unset", variable, value)
		}
	}
}

func TestIsolateClearsSeshSettingsAndKeepsIntegrationRequirements(t *testing.T) {
	restoreIsolatedVariables(t)

	t.Setenv("SESH_CONFIG", "/home/example/.config/sesh/config.toml")
	t.Setenv("SESH_EXAMPLE_INTEGRATION_REQUIRED", "1")

	if err := isolate(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	if value, set := os.LookupEnv("SESH_CONFIG"); set {
		t.Fatalf("SESH_CONFIG = %q after isolation, want unset", value)
	}

	if value := os.Getenv("SESH_EXAMPLE_INTEGRATION_REQUIRED"); value != "1" {
		t.Fatalf("SESH_EXAMPLE_INTEGRATION_REQUIRED = %q after isolation, want %q", value, "1")
	}
}

func TestHelperDirectoriesKeepParentValuesAndIsolateMissingOnes(t *testing.T) {
	restoreIsolatedVariables(t)

	parentHome := t.TempDir()
	t.Setenv("HOME", parentHome)

	if err := os.Unsetenv("TMUX_TMPDIR"); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := setDirectories(root, true); err != nil {
		t.Fatal(err)
	}

	if home := os.Getenv("HOME"); home != parentHome {
		t.Fatalf("HOME = %q, want the parent's %q", home, parentHome)
	}

	if dir := os.Getenv("TMUX_TMPDIR"); dir != filepath.Join(root, "tmux") {
		t.Fatalf("TMUX_TMPDIR = %q, want an isolated directory", dir)
	}
}

func TestRemoveIsolatedHomeDeletesReadOnlyTrees(t *testing.T) {
	root := filepath.Join(t.TempDir(), "isolated")
	module := filepath.Join(root, "home", "go", "pkg", "mod", "example.com", "module@v1.0.0")

	if err := os.MkdirAll(module, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.com/module\n"), 0o400); err != nil {
		t.Fatal(err)
	}

	for dir := module; dir != root; dir = filepath.Dir(dir) {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
	}

	if err := removeIsolatedHome(root); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("isolated home remains: %v", err)
	}
}

// restoreIsolatedVariables registers restoration of every variable isolate changes.
func restoreIsolatedVariables(t *testing.T) {
	t.Helper()

	variables := []string{
		"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
		"XDG_RUNTIME_DIR", "TMUX_TMPDIR", IsolatedMarker,
	}
	variables = append(variables, sessionVariables...)
	variables = append(variables, goToolchainVariables...)

	for _, entry := range os.Environ() {
		if name, _, _ := strings.Cut(entry, "="); strings.HasPrefix(name, "SESH_") {
			variables = append(variables, name)
		}
	}

	for _, variable := range variables {
		value, set := os.LookupEnv(variable)
		t.Setenv(variable, value)

		if !set {
			if err := os.Unsetenv(variable); err != nil {
				t.Fatal(err)
			}
		}
	}
}
