package tui

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/privatefs"
)

const maxAgentGitCacheBytes = 1024 * 1024

type agentGitCacheFile struct {
	Version int                           `json:"version"`
	Entries map[string]agentGitCacheEntry `json:"entries"`
}

func agentGitCachePath() string {
	return filepath.Join(config.GetXDGPaths().CacheHome, "sesh", "agent-git.json")
}

func readAgentGitCache(path string) map[string]agentGitCacheEntry {
	dir, name, err := privatefs.OpenParent(path, false)
	if err != nil {
		return nil
	}

	data, readErr := dir.Read(name, maxAgentGitCacheBytes)
	if err := errors.Join(readErr, dir.Close()); err != nil {
		return nil
	}

	var cache agentGitCacheFile
	if err := json.Unmarshal(data, &cache); err != nil || cache.Version != 1 {
		return nil
	}

	return cache.Entries
}

func writeAgentGitCache(path string, entries map[string]agentGitCacheEntry) error {
	data, err := json.Marshal(agentGitCacheFile{Version: 1, Entries: entries})
	if err != nil {
		return oops.In("tui").Code("git_cache_failed").With("operation", "encode").Wrap(err)
	}

	if len(data) > maxAgentGitCacheBytes {
		return nil
	}

	dir, err := privatefs.Ensure(filepath.Dir(path))
	if err != nil {
		return oops.In("tui").Code("git_cache_failed").With("operation", "create_directory").Wrap(err)
	}

	writeErr := replaceCacheUnsynced(dir, filepath.Base(path), data)
	if err := errors.Join(writeErr, dir.Close()); err != nil {
		return oops.In("tui").Code("git_cache_failed").With("operation", "save").Wrap(err)
	}

	return nil
}

func replaceCacheUnsynced(dir *privatefs.Dir, name string, data []byte) error {
	temporary := ".agent-git-" + rand.Text() + ".tmp"

	file, err := dir.File(temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return oops.In("tui").Code("git_cache_failed").With("operation", "create_temp_file").Wrap(err)
	}

	// No fsync: it would stall popup shutdown under disk pressure, and an unreadable cache is rebuilt.
	_, writeErr := file.Write(data)
	closeErr := file.Close()

	if err := errors.Join(writeErr, closeErr); err != nil {
		return errors.Join(err, dir.Remove(temporary))
	}

	if err := dir.Rename(temporary, dir, name); err != nil {
		return errors.Join(err, dir.Remove(temporary))
	}

	return nil
}
