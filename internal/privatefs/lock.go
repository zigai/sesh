//go:build linux || darwin

package privatefs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const lockRetry = 10 * time.Millisecond

func (d *Dir) Lock(ctx context.Context, name string) (*os.File, error) {
	return d.lock(ctx, name, unix.O_RDWR|unix.O_CREAT)
}

func (d *Dir) LockExisting(ctx context.Context, name string) (*os.File, error) {
	return d.lock(ctx, name, unix.O_RDWR)
}

func (d *Dir) lock(ctx context.Context, name string, flags int) (*os.File, error) {
	file, err := d.File(name, flags)
	if err != nil {
		return nil, err
	}

	if err := acquire(ctx, file); err != nil {
		return nil, errors.Join(err, file.Close())
	}

	return file, nil
}

func acquire(ctx context.Context, file *os.File) error {
	ticker := time.NewTicker(lockRetry)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("acquire private lock: %w", err)
		}

		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}

		if errors.Is(err, unix.EINTR) {
			continue
		}

		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("lock private file: %w", err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for private lock: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
