package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/samber/oops"
)

const defaultDirMode = 0o755

// SystemConfigPath is the system-wide configuration, layered below the user's.
const SystemConfigPath = "/etc/sesh/config.toml"

var ErrNilConfig = errors.New("config is nil")

type XDGPaths struct {
	ConfigHome string
	DataHome   string
	StateHome  string
	CacheHome  string
}

func GetXDGPaths() XDGPaths {
	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE")
		if home == "" {
			home = "/"
		}
	}

	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}

	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		cacheHome = filepath.Join(home, ".cache")
	}

	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}

	return XDGPaths{
		ConfigHome: configHome,
		DataHome:   dataHome,
		StateHome:  stateHome,
		CacheHome:  cacheHome,
	}
}

func DefaultConfigPath() string {
	return filepath.Join(GetXDGPaths().ConfigHome, "sesh", "config.toml")
}

func GetDataDir(cfg *Config) (string, error) {
	if cfg == nil {
		return "", oops.In("config").Code("config_invalid").With("operation", "get_data_dir").Wrap(ErrNilConfig)
	}

	if cfg.Paths.DataDir != "" {
		return cfg.Paths.DataDir, nil
	}

	return filepath.Join(GetXDGPaths().DataHome, "sesh"), nil
}

func GetLogFile(cfg *Config) (string, error) {
	if cfg == nil {
		return "", oops.In("config").Code("config_invalid").With("operation", "get_log_file").Wrap(ErrNilConfig)
	}

	if cfg.Paths.LogFile != "" {
		return cfg.Paths.LogFile, nil
	}

	return filepath.Join(GetXDGPaths().StateHome, "sesh", "sesh.log"), nil
}

func EnsureDir(path string) error {
	if err := os.MkdirAll(path, defaultDirMode); err != nil {
		return oops.In("config").Code("directory_create_failed").With("operation", "ensure_dir", "path", path).Wrap(err)
	}

	return nil
}
