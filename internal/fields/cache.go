package fields

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/privatefs"
)

const cacheVersion = 1

var errCacheVersion = errors.New("unsupported field cache version")

type CacheEntry struct {
	ChangeKey string    `json:"changeKey"`
	Text      string    `json:"text"`
	At        time.Time `json:"at"`
}

type cacheFile struct {
	Version int                   `json:"version"`
	Entries map[string]CacheEntry `json:"entries"`
}

func CachePath(name string) string {
	return filepath.Join(config.GetXDGPaths().CacheHome, "sesh", "fields", name+".json")
}

func ReadCache(path string) (map[string]CacheEntry, error) {
	dir, name, err := privatefs.OpenParent(path, false)
	if err != nil {
		return nil, fmt.Errorf("open field cache directory: %w", err)
	}

	data, readErr := readPrivateFile(dir, name)
	if err := errors.Join(readErr, dir.Close()); err != nil {
		return nil, err
	}

	return decodeCache(data)
}

func MergeCache(ctx context.Context, path string, updates map[string]CacheEntry) error {
	dir, err := privatefs.Ensure(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("create field cache directory: %w", err)
	}

	name := filepath.Base(path)
	lock, err := dir.Lock(ctx, name+".lock")
	if err != nil {
		return errors.Join(fmt.Errorf("lock field cache: %w", err), dir.Close())
	}

	return errors.Join(mergeCache(dir, name, updates), lock.Close(), dir.Close())
}

func decodeCache(data []byte) (map[string]CacheEntry, error) {
	var cache cacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("decode field cache: %w", err)
	}

	if cache.Version != cacheVersion {
		return nil, fmt.Errorf("%w %d", errCacheVersion, cache.Version)
	}

	return cache.Entries, nil
}

func mergeCache(dir *privatefs.Dir, name string, updates map[string]CacheEntry) error {
	entries := make(map[string]CacheEntry)

	data, err := readPrivateFile(dir, name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err == nil {
		cached, err := decodeCache(data)
		if err != nil {
			return err
		}

		maps.Copy(entries, cached)
	}

	for row, entry := range updates {
		if current, ok := entries[row]; !ok || !entry.At.Before(current.At) {
			entries[row] = entry
		}
	}

	data, err = json.Marshal(cacheFile{Version: cacheVersion, Entries: entries})
	if err != nil {
		return fmt.Errorf("encode field cache: %w", err)
	}

	return publishCache(dir, name, data)
}

func readPrivateFile(dir *privatefs.Dir, name string) ([]byte, error) {
	file, err := dir.File(name, unix.O_RDONLY)
	if err != nil {
		return nil, fmt.Errorf("open field cache: %w", err)
	}

	data, readErr := io.ReadAll(file)
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, fmt.Errorf("read field cache: %w", err)
	}

	return data, nil
}

func publishCache(dir *privatefs.Dir, name string, data []byte) error {
	temporary := "." + name + "-" + rand.Text() + ".tmp"

	file, err := dir.File(temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return fmt.Errorf("create field cache file: %w", err)
	}

	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return errors.Join(fmt.Errorf("write field cache: %w", err), dir.Remove(temporary))
	}

	if err := dir.Rename(temporary, dir, name); err != nil {
		return errors.Join(fmt.Errorf("publish field cache: %w", err), dir.Remove(temporary))
	}

	return nil
}
