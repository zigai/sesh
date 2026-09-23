//go:build linux || darwin

package automation

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/privatefs"
)

const saveLockRetryInterval = 50 * time.Millisecond

func openSaveLockDir(path string) (*privatefs.Dir, error) {
	dir, err := privatefs.Ensure(path)
	if err != nil {
		return nil, oops.In("automation").Code("save_lock_failed").With("operation", "open_save_lock_dir").Wrap(err)
	}

	return dir, nil
}

func acquireSaveLockFile(
	ctx context.Context,
	dir *privatefs.Dir,
	path string,
	name string,
	mode SaveLockMode,
) (*SaveLock, bool, error) {
	file, err := dir.File(name, unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, false, oops.In("automation").Code("save_lock_failed").With("operation", "open_save_lock_file").Wrap(err)
	}

	acquired, err := flockSaveLockContext(ctx, file, mode)
	if err != nil || !acquired {
		return nil, false, errors.Join(err, closeSaveLockFile(file))
	}

	return &SaveLock{
		path: path,
		release: onceRelease(func() error {
			return errors.Join(
				unlockSaveLockFile(file),
				closeSaveLockFile(file),
				closeSaveLockDir(dir),
			)
		}),
	}, true, nil
}

func unlockSaveLockFile(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		return oops.In("automation").Code("save_lock_failed").With("operation", "unlock_save_lock").Wrap(err)
	}

	return nil
}

func closeSaveLockFile(file *os.File) error {
	if err := file.Close(); err != nil {
		return oops.In("automation").Code("save_lock_failed").With("operation", "close_save_lock").Wrap(err)
	}

	return nil
}

func closeSaveLockDir(dir *privatefs.Dir) error {
	if err := dir.Close(); err != nil {
		return oops.In("automation").Code("save_lock_failed").With("operation", "close_save_lock_dir").Wrap(err)
	}

	return nil
}

func flockSaveLockContext(ctx context.Context, file *os.File, mode SaveLockMode) (bool, error) {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return true, nil
		}

		if errors.Is(err, unix.EINTR) {
			continue
		}

		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return false, oops.In("automation").Code("save_lock_failed").With("operation", "flock_save_lock").Wrap(err)
		}

		if mode == SaveLockNonBlocking {
			return false, nil
		}

		timer := time.NewTimer(saveLockRetryInterval)

		select {
		case <-ctx.Done():
			timer.Stop()

			return false, oops.In("automation").Code("save_lock_failed").With("operation", "wait_save_lock").Wrap(ctx.Err())
		case <-timer.C:
		}
	}
}
