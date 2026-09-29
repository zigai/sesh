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
		"XDG_RUNTIME_DIR", "TMUX_TMPDIR", "TMUX", isolatedMarker,
	}
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
