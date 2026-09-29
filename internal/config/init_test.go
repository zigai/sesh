package config_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/zigai/sesh/internal/config"
)

func TestInitCreatesCommentedDefaultConfig(t *testing.T) {
	tempDir := t.TempDir()
	targetPath := filepath.Join(tempDir, "sesh", "config.toml")

	path, err := config.Init(config.WithInitPath(targetPath))
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if path != targetPath {
		t.Fatalf("Init() returned %q, want %q", path, targetPath)
	}

	fi, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("stat targetPath: %v", err)
	}

	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file permissions = %o, want 0600", fi.Mode().Perm())
	}

	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read targetPath: %v", err)
	}

	expected, err := os.ReadFile("default.toml")
	if err != nil {
		t.Fatalf("read default.toml: %v", err)
	}

	if !bytes.Equal(content, expected) {
		t.Fatal("Init() content did not match default.toml")
	}
}

func TestInitRejectsExistingFileWithoutForce(t *testing.T) {
	tempDir := t.TempDir()
	targetPath := filepath.Join(tempDir, "config.toml")

	if _, err := config.Init(config.WithInitPath(targetPath)); err != nil {
		t.Fatalf("first Init() error = %v", err)
	}

	_, err := config.Init(config.WithInitPath(targetPath))
	if err == nil {
		t.Fatal("second Init() error = nil, want config already exists error")
	}

	if !errors.Is(err, config.ErrConfigExists) && !errors.Is(err, os.ErrExist) {
		t.Fatalf("second Init() error = %v, want ErrConfigExists or ErrExist", err)
	}
}

func TestInitOverwritesWithForce(t *testing.T) {
	tempDir := t.TempDir()
	targetPath := filepath.Join(tempDir, "config.toml")

	if _, err := config.Init(config.WithInitPath(targetPath)); err != nil {
		t.Fatalf("first Init() error = %v", err)
	}

	if err := os.WriteFile(targetPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("corrupting config: %v", err)
	}

	if _, err := config.Init(config.WithInitPath(targetPath), config.WithInitForce(true)); err != nil {
		t.Fatalf("Init(force=true) error = %v", err)
	}

	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading forced config: %v", err)
	}

	if bytes.Equal(content, []byte("corrupt")) {
		t.Fatal("forced Init() did not overwrite corrupt file")
	}
}

func TestInitRejectsSymlinkTarget(t *testing.T) {
	tempDir := t.TempDir()

	realFile := filepath.Join(tempDir, "real.toml")
	if err := os.WriteFile(realFile, []byte("real"), 0o600); err != nil {
		t.Fatal(err)
	}

	symlinkTarget := filepath.Join(tempDir, "symlink.toml")
	if err := os.Symlink(realFile, symlinkTarget); err != nil {
		t.Fatal(err)
	}

	_, err := config.Init(config.WithInitPath(symlinkTarget))
	if err == nil || !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Init() error = %v, want ErrConfigSymlink", err)
	}

	content, err := os.ReadFile(realFile)
	if err != nil || string(content) != "real" {
		t.Fatalf("real file was altered through symlink: %v", err)
	}
}

func TestInitRejectsSymlinkDir(t *testing.T) {
	tempDir := t.TempDir()

	realDir := filepath.Join(tempDir, "real_dir")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}

	symlinkDir := filepath.Join(tempDir, "symlink_dir")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(symlinkDir, "config.toml")

	_, err := config.Init(config.WithInitPath(target))
	if err == nil || !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Init() error = %v, want ErrConfigSymlink for directory", err)
	}
}

func TestInitConcurrentCalls(t *testing.T) {
	tempDir := t.TempDir()
	targetPath := filepath.Join(tempDir, "concurrent", "config.toml")

	const concurrency = 8

	var wg sync.WaitGroup

	errs := make([]error, concurrency)

	for i := range concurrency {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			_, errs[idx] = config.Init(config.WithInitPath(targetPath))
		}(i)
	}

	wg.Wait()

	successCount := 0
	existCount := 0

	for _, err := range errs {
		switch {
		case err == nil:
			successCount++
		case errors.Is(err, config.ErrConfigExists), errors.Is(err, os.ErrExist):
			existCount++
		default:
			t.Errorf("unexpected error in concurrent init: %v", err)
		}
	}

	if successCount != 1 {
		t.Fatalf("successCount = %d, want exactly 1", successCount)
	}

	if existCount != concurrency-1 {
		t.Fatalf("existCount = %d, want %d", existCount, concurrency-1)
	}

	fi, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatal(err)
	}

	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 0600", fi.Mode().Perm())
	}
}

func TestLoadWithoutConfigDoesNotTouchDisk(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg == nil {
		t.Fatal("Load() returned nil config")
	}

	defaultPath := filepath.Join(tempDir, "sesh", "config.toml")
	if _, err := os.Lstat(defaultPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() created file on disk: %v", err)
	}
}

func TestInitRejectsAncestorDirSymlink(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	realGrandparent := filepath.Join(tempDir, "real_gp")
	if err := os.MkdirAll(realGrandparent, 0o700); err != nil {
		t.Fatal(err)
	}

	symlinkGrandparent := filepath.Join(tempDir, "symlink_gp")
	if err := os.Symlink(realGrandparent, symlinkGrandparent); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	_, err := config.Init(config.WithInitPath(filepath.Join(symlinkGrandparent, "nested", "config.toml")))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Init() error = %v, want %v", err, config.ErrConfigSymlink)
	}
}

func TestInitRejectsSymlinkLeafWithoutChangingTarget(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	configPath := filepath.Join(tempDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}

	targetFile := filepath.Join(tempDir, "protected_target.txt")
	if err := os.WriteFile(targetFile, []byte("important-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(targetFile, configPath); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	_, err := config.Init(config.WithInitPath(configPath), config.WithInitForce(true))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Init() error = %v, want %v", err, config.ErrConfigSymlink)
	}

	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "important-data" {
		t.Fatalf("targetFile was overwritten: %q, want important-data", string(data))
	}
}

func TestInitRejectsSymlinkParentDirectory(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	realParent := filepath.Join(tempDir, "real_parent")
	if err := os.MkdirAll(realParent, 0o700); err != nil {
		t.Fatal(err)
	}

	parentDir := filepath.Join(tempDir, "parent_dir")
	if err := os.Symlink(realParent, parentDir); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	configPath := filepath.Join(parentDir, "config.toml")

	_, err := config.Init(config.WithInitPath(configPath))
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("Init() error = %v, want %v", err, config.ErrConfigSymlink)
	}
}

func TestInitNoClobberDoesNotOverwriteExistingFile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")

	initialContent := []byte("# existing config\n[theme]\nprimary = \"#999999\"\n")
	if err := os.WriteFile(configPath, initialContent, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Init(config.WithInitPath(configPath), config.WithInitForce(false))
	if !errors.Is(err, config.ErrConfigExists) {
		t.Fatalf("Init() error = %v, want %v", err, config.ErrConfigExists)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != string(initialContent) {
		t.Fatalf("existing file was modified: got %q, want %q", string(data), string(initialContent))
	}
}

func TestConcurrentInitWinnerIsFullValidConfig(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "concurrent_init.toml")

	const goroutines = 16

	var wg sync.WaitGroup

	errs := make([]error, goroutines)
	paths := make([]string, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			p, err := config.Init(config.WithInitPath(configPath), config.WithInitForce(false))
			errs[idx] = err
			paths[idx] = p
		}(i)
	}

	wg.Wait()

	winnerCount := 0

	for _, err := range errs {
		if err == nil {
			winnerCount++
		} else if !errors.Is(err, config.ErrConfigExists) {
			t.Fatalf("unexpected error from concurrent Init: %v", err)
		}
	}

	if winnerCount != 1 {
		t.Fatalf("winnerCount = %d, want exactly 1", winnerCount)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) == 0 {
		t.Fatal("config file on disk is empty")
	}

	cfg, err := config.Load(config.WithExplicitPath(configPath))
	if err != nil {
		t.Fatalf("Load(concurrent_init) failed: %v", err)
	}

	if cfg.Theme.Primary != "#24c8ff" {
		t.Errorf("Theme.Primary = %q, want #24c8ff", cfg.Theme.Primary)
	}
}
