//go:build linux || darwin

package config

import (
	"errors"
	"path/filepath"

	"github.com/samber/oops"
)

var (
	ErrConfigExists  = errors.New("config already exists")
	ErrConfigSymlink = errors.New("config path is a symlink")
)

type InitOption func(*initOptions)

type initOptions struct {
	path  string
	force bool
}

func WithInitPath(path string) InitOption {
	return func(opts *initOptions) {
		opts.path = path
	}
}

func WithInitForce(force bool) InitOption {
	return func(opts *initOptions) {
		opts.force = force
	}
}

func Init(opts ...InitOption) (string, error) {
	options := &initOptions{
		path:  "",
		force: false,
	}

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
			With("operation", "init", "reason", "resolve_abs_path").
			Wrap(err)
	}

	parentDir := filepath.Dir(absPath)
	fileName := filepath.Base(absPath)

	dir, err := openPinnedDir(parentDir, true)
	if err != nil {
		return "", err
	}

	var initErr error
	if !options.force {
		initErr = dir.CreateExclusive(fileName, defaultConfigFile)
	} else {
		initErr = dir.WriteAtomic(fileName, defaultConfigFile)
	}

	closeErr := dir.Close()
	if err := errors.Join(initErr, closeErr); err != nil {
		return "", err
	}

	return absPath, nil
}
