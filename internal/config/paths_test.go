package config_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/config"
)

func TestGetDataDirRejectsNilConfig(t *testing.T) {
	if _, err := config.GetDataDir(nil); !errors.Is(err, config.ErrNilConfig) {
		t.Fatalf("GetDataDir(nil) error = %v, want %v", err, config.ErrNilConfig)
	}
}

func TestGetDataDirUsesDataHomeByDefault(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	cfg := config.DefaultConfig()

	got, err := config.GetDataDir(cfg)
	if err != nil {
		t.Fatalf("GetDataDir() error = %v", err)
	}

	want := filepath.Join(dataHome, "sesh")
	if got != want {
		t.Fatalf("GetDataDir() = %q, want %q", got, want)
	}
}

func TestGetDataDirPreservesExplicitPath(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "custom-data")
	cfg := config.DefaultConfig()
	cfg.Paths.DataDir = custom

	got, err := config.GetDataDir(cfg)
	if err != nil {
		t.Fatalf("GetDataDir() error = %v", err)
	}

	if got != custom {
		t.Fatalf("GetDataDir() = %q, want %q", got, custom)
	}
}

func TestDefaultConfigPathUsesConfigHome(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	got := config.DefaultConfigPath()

	want := filepath.Join(configHome, "sesh", "config.toml")
	if got != want {
		t.Fatalf("DefaultConfigPath() = %q, want %q", got, want)
	}
}

func TestGetXDGPathsHonorsExplicitHomes(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "data")
	stateHome := filepath.Join(root, "state")
	cacheHome := filepath.Join(root, "cache")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	paths := config.GetXDGPaths()
	if paths.ConfigHome != configHome {
		t.Errorf("ConfigHome = %q, want %q", paths.ConfigHome, configHome)
	}

	if paths.DataHome != dataHome {
		t.Errorf("DataHome = %q, want %q", paths.DataHome, dataHome)
	}

	if paths.StateHome != stateHome {
		t.Errorf("StateHome = %q, want %q", paths.StateHome, stateHome)
	}

	if paths.CacheHome != cacheHome {
		t.Errorf("CacheHome = %q, want %q", paths.CacheHome, cacheHome)
	}
}

func TestGetXDGPathsUsesStandardDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", filepath.Join(t.TempDir(), "profile"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	paths := config.GetXDGPaths()

	want := config.XDGPaths{
		ConfigHome: filepath.Join(home, ".config"),
		DataHome:   filepath.Join(home, ".local", "share"),
		StateHome:  filepath.Join(home, ".local", "state"),
		CacheHome:  filepath.Join(home, ".cache"),
	}
	if paths != want {
		t.Fatalf("GetXDGPaths() = %#v, want %#v", paths, want)
	}
}

func TestGetXDGPathsFallsBackToUserProfile(t *testing.T) {
	profile := t.TempDir()
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", profile)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	paths := config.GetXDGPaths()
	if want := filepath.Join(profile, ".local", "state"); paths.StateHome != want {
		t.Fatalf("StateHome = %q, want %q", paths.StateHome, want)
	}
}

func TestGetXDGPathsFallsBackToRoot(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	paths := config.GetXDGPaths()
	if want := filepath.Join("/", ".local", "state"); paths.StateHome != want {
		t.Fatalf("StateHome = %q, want %q", paths.StateHome, want)
	}
}

func TestGetLogFileRejectsNilConfig(t *testing.T) {
	if _, err := config.GetLogFile(nil); !errors.Is(err, config.ErrNilConfig) {
		t.Fatalf("GetLogFile(nil) error = %v, want %v", err, config.ErrNilConfig)
	}
}

func TestGetLogFileUsesStateHomeByDefault(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	cfg := config.DefaultConfig()
	if cfg.Paths.LogFile != "" {
		t.Fatalf("DefaultConfig().Paths.LogFile = %q, want empty", cfg.Paths.LogFile)
	}

	got, err := config.GetLogFile(cfg)
	if err != nil {
		t.Fatalf("GetLogFile() error = %v", err)
	}

	want := filepath.Join(stateHome, "sesh", "sesh.log")
	if got != want {
		t.Fatalf("GetLogFile() = %q, want %q", got, want)
	}
}

func TestGetLogFilePreservesExplicitPath(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{
			name: "absolute",
			path: filepath.Join(t.TempDir(), "logs", "custom.jsonl"),
		},
		{
			name: "relative",
			path: "var/../logs/custom.jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Paths.LogFile = tt.path

			got, err := config.GetLogFile(cfg)
			if err != nil {
				t.Fatalf("GetLogFile() error = %v", err)
			}

			if got != tt.path {
				t.Fatalf("GetLogFile() = %q, want exact configured path %q", got, tt.path)
			}
		})
	}
}
