//go:build linux || darwin

package picker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const sessionNamesFile = "sesh-session-names.json"

type sessionNameStamp struct {
	Inode    uint64 `json:"inode"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

// storedSessionName lets a new picker process continue reading each transcript where the last one
// stopped. Every open still reads what was appended since, so the stored name cannot go stale.
type storedSessionName struct {
	Offset   int64            `json:"offset"`
	Name     string           `json:"name"`
	HasInfo  bool             `json:"hasInfo"`
	Skipping bool             `json:"skipping"`
	Stamp    sessionNameStamp `json:"stamp"`
}

func newSessionNameStamp(info os.FileInfo) sessionNameStamp {
	stamp := sessionNameStamp{
		Inode:    0,
		Size:     info.Size(),
		Modified: info.ModTime().UnixNano(),
	}

	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		stamp.Inode = stat.Ino
	}

	return stamp
}

// sessionNamesPath is empty without a memory-backed runtime directory: the store exists to avoid
// disk work, so it is not worth keeping on disk.
func sessionNamesPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return ""
	}

	return filepath.Join(dir, sessionNamesFile)
}

// loadSessionNames treats an unreadable store as empty, which only costs a full read of each
// transcript.
func loadSessionNames(path string) map[string]sessionNameState {
	if path == "" {
		return nil
	}

	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var stored map[string]storedSessionName
	if json.Unmarshal(encoded, &stored) != nil {
		return nil
	}

	cache := make(map[string]sessionNameState, len(stored))
	for transcript, entry := range stored {
		cache[transcript] = sessionNameState{
			offset:   entry.Offset,
			name:     entry.Name,
			hasInfo:  entry.HasInfo,
			skipping: entry.Skipping,
			stamp:    entry.Stamp,
			stamped:  true,
		}
	}

	return cache
}

func saveSessionNames(path string, cache map[string]sessionNameState) error {
	if path == "" {
		return nil
	}

	stored := make(map[string]storedSessionName, len(cache))
	for transcript, state := range cache {
		if state.stamped {
			stored[transcript] = storedSessionName{
				Offset:   state.offset,
				Name:     state.name,
				HasInfo:  state.hasInfo,
				Skipping: state.skipping,
				Stamp:    state.stamp,
			}
		}
	}

	encoded, err := json.Marshal(stored)
	if err != nil {
		return err
	}

	file, err := os.CreateTemp(filepath.Dir(path), sessionNamesFile+".*")
	if err != nil {
		return err
	}

	_, writeErr := file.Write(encoded)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		_ = os.Remove(file.Name())

		return err
	}

	if err := os.Rename(file.Name(), path); err != nil {
		_ = os.Remove(file.Name())

		return err
	}

	return nil
}
