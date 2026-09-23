//go:build linux || darwin

package automation

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/samber/oops"
)

const SaveLockName = "autosave.lock"

const (
	SaveLockNonBlocking SaveLockMode = iota
	SaveLockBlocking
)

var (
	ErrSaveLockBusy      = errors.New("autosave lock is held by another process")
	ErrSaveLockPathEmpty = errors.New("autosave lock path is empty")
)

type SaveLockMode int

type SaveLock struct {
	path    string
	release func() error
}

func (l *SaveLock) Path() string {
	if l == nil {
		return ""
	}

	return l.path
}

// Release unlocks and closes the owned descriptor. The lock file itself is
// never removed: unlinking an flock-guarded path races with other holders.
func (l *SaveLock) Release() error {
	if l == nil || l.release == nil {
		return nil
	}

	return l.release()
}

func SaveLockPath() string {
	runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR"))
	if runtimeDir == "" {
		runtimeDir = filepath.Join(os.TempDir(), "sesh-"+currentUserIDForPath())
	}

	return filepath.Join(runtimeDir, "sesh", SaveLockName)
}

func AcquireSaveLock(ctx context.Context, path string, mode SaveLockMode) (*SaveLock, bool, error) {
	if path == "" {
		return nil, false, oops.In("automation").Code("save_lock_failed").With("operation", "acquire_save_lock").Wrap(ErrSaveLockPathEmpty)
	}

	dir, err := openSaveLockDir(filepath.Dir(path))
	if err != nil {
		return nil, false, err
	}

	lock, acquired, err := acquireSaveLockFile(ctx, dir, path, filepath.Base(path), mode)
	if err != nil || !acquired {
		return lock, acquired, errors.Join(err, dir.Close())
	}

	return lock, true, nil
}

func TryAcquireSaveLock(ctx context.Context, path string) (*SaveLock, error) {
	lock, acquired, err := AcquireSaveLock(ctx, path, SaveLockNonBlocking)
	if err != nil {
		return nil, err
	}

	if !acquired {
		return nil, oops.In("automation").Code("save_lock_busy").With("operation", "try_acquire_save_lock").Wrap(ErrSaveLockBusy)
	}

	return lock, nil
}

func currentUserIDForPath() string {
	if current, err := user.Current(); err == nil && current != nil {
		if id := userIdentityPathComponent(current.Uid); id != "" {
			return id
		}
	}

	for _, key := range []string{"UID", "USER", "USERNAME", "HOME"} {
		if id := userIdentityPathComponent(os.Getenv(key)); id != "" {
			return id
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		if id := userIdentityPathComponent(home); id != "" {
			return id
		}
	}

	return "process-" + strconv.Itoa(os.Getpid())
}

func userIdentityPathComponent(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}

	if _, err := strconv.ParseUint(identity, 10, 64); err == nil {
		return identity
	}

	digest := sha256.Sum256([]byte(identity))

	return fmt.Sprintf("user-%x", digest[:8])
}

func onceRelease(release func() error) func() error {
	return sync.OnceValue(release)
}
