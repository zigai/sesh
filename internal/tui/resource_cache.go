package tui

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/tui/providers"
)

const maxResourceCacheBytes = 4 * 1024 * 1024

type resourceCacheFile struct {
	Version int                      `json:"version"`
	Sample  providers.ResourceSample `json:"sample"`
}

func resourceCachePath() string {
	return filepath.Join(config.GetXDGPaths().CacheHome, "sesh", "resource-sample.json")
}

func readResourceCache(path string) providers.ResourceSample {
	var empty providers.ResourceSample

	dir, name, err := privatefs.OpenParent(path, false)
	if err != nil {
		return empty
	}

	data, readErr := dir.Read(name, maxResourceCacheBytes)
	if err := errors.Join(readErr, dir.Close()); err != nil {
		return empty
	}

	var cache resourceCacheFile
	if err := json.Unmarshal(data, &cache); err != nil || cache.Version != 1 {
		return empty
	}

	return cache.Sample
}

func writeResourceCache(path string, sample providers.ResourceSample) error {
	data, err := json.Marshal(resourceCacheFile{Version: 1, Sample: sample})
	if err != nil {
		return fmt.Errorf("encode resource cache: %w", err)
	}

	if len(data) > maxResourceCacheBytes {
		return nil
	}

	dir, err := privatefs.Ensure(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("create resource cache directory: %w", err)
	}

	name := ".resource-" + rand.Text() + ".tmp"

	file, createErr := dir.File(name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if createErr != nil {
		return errors.Join(createErr, dir.Close())
	}

	_, writeErr := file.Write(data)

	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return errors.Join(err, dir.Remove(name), dir.Close())
	}

	if err := dir.Rename(name, dir, filepath.Base(path)); err != nil {
		return errors.Join(err, dir.Remove(name), dir.Close())
	}

	if err := dir.Close(); err != nil {
		return fmt.Errorf("close resource cache directory: %w", err)
	}

	return nil
}
