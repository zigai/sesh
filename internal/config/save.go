//go:build linux || darwin

package config

import (
	"errors"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/samber/oops"
)

type SaveOption func(*saveOptions)

type saveOptions struct {
	path string
}

type savedConfig struct {
	*Config

	Hooks map[string]any `toml:"hooks"`
}

func WithSavePath(path string) SaveOption {
	return func(o *saveOptions) {
		o.path = path
	}
}

func Save(cfg *Config, opts ...SaveOption) (string, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	options := &saveOptions{path: ""}
	for _, opt := range opts {
		opt(options)
	}

	targetPath := options.path
	if targetPath == "" {
		targetPath = DefaultConfigPath()
	}

	absPath, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return "", oops.In("config").
			Code("config_invalid_path").
			With("operation", "save", "reason", "resolve_abs_path").
			Wrap(err)
	}

	parentDir := filepath.Dir(absPath)
	fileName := filepath.Base(absPath)

	dir, err := openPinnedDir(parentDir, true)
	if err != nil {
		return "", err
	}

	data, err := marshalConfig(cfg)
	if err != nil {
		return "", errors.Join(err, dir.Close())
	}

	writeErr := dir.WriteAtomic(fileName, data)
	closeErr := dir.Close()

	if err := errors.Join(writeErr, closeErr); err != nil {
		return "", err
	}

	return absPath, nil
}

func marshalConfig(cfg *Config) ([]byte, error) {
	serialized := cfg.Clone()
	if serialized.AgentSessions.Status != nil {
		resolved := cfg.ResolvedAgentStatus()
		serialized.AgentSessions.Status = &resolved
	}

	data, err := toml.Marshal(savedConfig{
		Config: serialized,
		Hooks: map[string]any{
			"timeout":             cfg.Hooks.Timeout,
			"session_switch_pre":  hookValues(cfg.Hooks.SessionSwitchPre),
			"session_switch_post": hookValues(cfg.Hooks.SessionSwitchPost),
			"session_attach_pre":  hookValues(cfg.Hooks.SessionAttachPre),
			"session_attach_post": hookValues(cfg.Hooks.SessionAttachPost),
			"tmux_startup_post":   hookValues(cfg.Hooks.TmuxStartupPost),
		},
	})
	if err != nil {
		return nil, oops.In("config").Code("config_marshal_failed").With("operation", "save", "reason", "marshal_failed").Wrap(err)
	}

	return data, nil
}

func hookValues(commands []HookCommand) []any {
	values := make([]any, len(commands))
	for i := range commands {
		values[i] = commands[i].value()
	}

	return values
}
