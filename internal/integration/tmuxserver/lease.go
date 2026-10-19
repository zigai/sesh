//go:build linux || darwin

package tmuxserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/samber/oops"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/privatefs"
)

var (
	ErrEndpointAlias = errors.New("tmux endpoint path must be canonical without symlinks, aliases, or hard-links")
	ErrLeaseRoot     = errors.New("an explicit private lease root directory is required")
)

type Lease struct {
	mu       sync.Mutex
	dir      *privatefs.Dir
	file     *os.File
	closeErr error
	closed   bool
}

// AcquireLease must run before any repository lock is taken, never while one is held, or lock
// ordering inverts and can deadlock.
func AcquireLease(ctx context.Context, leaseRoot string, socket string) (*Lease, error) {
	return acquireLease(ctx, leaseRoot, socket, nil)
}

func acquireLease(ctx context.Context, leaseRoot, socket string, afterLock func() error) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, wrapAcquireError(err)
	}

	if err := validateLeaseRoot(leaseRoot); err != nil {
		return nil, err
	}

	if err := validateEndpoint(socket); err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, wrapAcquireError(err)
	}

	hash := sha256.Sum256([]byte(socket))
	lockName := hex.EncodeToString(hash[:]) + ".lock"

	dir, err := privatefs.Open(leaseRoot, true)
	if err != nil {
		return nil, oops.In("tmux").With("operation", "acquire_lease").Wrap(sanitizePathError(err))
	}

	file, err := dir.Lock(ctx, lockName)
	if err != nil {
		closeErr := dir.Close()

		return nil, wrapAcquireError(errors.Join(err, closeErr))
	}

	if afterLock != nil {
		if err := afterLock(); err != nil {
			return nil, wrapAcquireError(errors.Join(err, file.Close(), dir.Close()))
		}
	}

	if err := validateEndpoint(socket); err != nil {
		closeErr := errors.Join(file.Close(), dir.Close())

		return nil, wrapAcquireError(errors.Join(err, closeErr))
	}

	if err := ctx.Err(); err != nil {
		closeErr := errors.Join(file.Close(), dir.Close())

		return nil, wrapAcquireError(errors.Join(err, closeErr))
	}

	return &Lease{
		mu:       sync.Mutex{},
		dir:      dir,
		file:     file,
		closeErr: nil,
		closed:   false,
	}, nil
}

func validateLeaseRoot(leaseRoot string) error {
	if leaseRoot == "" || strings.Contains(leaseRoot, "\x00") ||
		!filepath.IsAbs(leaseRoot) || filepath.Clean(leaseRoot) != leaseRoot || leaseRoot == "/" {
		return oops.In("tmux").Code("endpoint_required").With("operation", "acquire_lease").Wrap(ErrLeaseRoot)
	}

	return nil
}

func validateEndpoint(socket string) error {
	if socket == "" || strings.Contains(socket, "\x00") ||
		!filepath.IsAbs(socket) || filepath.Clean(socket) != socket || socket == "/" {
		return oops.In("tmux").Code("endpoint_required").With("operation", "acquire_lease").Wrap(ErrEndpoint)
	}

	parentDir := filepath.Dir(socket)

	resolvedParent, err := filepath.EvalSymlinks(parentDir)
	if errors.Is(err, fs.ErrNotExist) {
		// tmux creates the socket directory when a server starts; its absence means no server.
		return oops.In("tmux").Code("server_unavailable").With("operation", "acquire_lease").Wrap(gotmux.ErrNoServer)
	}

	if err != nil {
		return oops.In("tmux").Code("endpoint_required").With("operation", "acquire_lease").Wrap(errors.Join(ErrEndpoint, sanitizePathError(err)))
	}

	if resolvedParent != parentDir {
		return oops.In("tmux").Code("endpoint_required").With("operation", "acquire_lease").Wrap(errors.Join(ErrEndpoint, ErrEndpointAlias))
	}

	return validateExistingSocket(socket)
}

func validateExistingSocket(socket string) error {
	fi, err := os.Lstat(socket)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		return oops.In("tmux").With("operation", "acquire_lease").Wrap(sanitizePathError(err))
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		return oops.In("tmux").Code("endpoint_required").With("operation", "acquire_lease").Wrap(errors.Join(ErrEndpoint, ErrEndpointAlias))
	}

	resolvedSocket, err := filepath.EvalSymlinks(socket)
	if err != nil {
		return oops.In("tmux").Code("endpoint_required").With("operation", "acquire_lease").Wrap(errors.Join(ErrEndpoint, sanitizePathError(err)))
	}

	if resolvedSocket != socket {
		return oops.In("tmux").Code("endpoint_required").With("operation", "acquire_lease").Wrap(errors.Join(ErrEndpoint, ErrEndpointAlias))
	}

	if fi.Mode()&os.ModeSocket == 0 {
		return oops.In("tmux").Code("endpoint_required").With("operation", "acquire_lease").Wrap(ErrEndpoint)
	}

	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return oops.In("tmux").Code("endpoint_required").With("operation", "acquire_lease").Wrap(errors.Join(ErrEndpoint, ErrEndpointAlias))
	}

	return nil
}

func (l *Lease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return l.closeErr
	}

	l.closed = true

	var err error

	if l.file != nil {
		err = errors.Join(err, l.file.Close())
		l.file = nil
	}

	if l.dir != nil {
		err = errors.Join(err, l.dir.Close())
		l.dir = nil
	}

	if err != nil {
		l.closeErr = oops.In("tmux").With("operation", "close_lease").Wrap(sanitizePathError(err))

		return l.closeErr
	}

	return nil
}

func wrapAcquireError(err error) error {
	if err == nil {
		return nil
	}

	sanitized := sanitizePathError(err)

	if errors.Is(err, context.Canceled) {
		return oops.In("tmux").Code("operation_canceled").With("operation", "acquire_lease").Wrap(sanitized)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return oops.In("tmux").Code("operation_timed_out").With("operation", "acquire_lease").Wrap(sanitized)
	}

	return oops.In("tmux").With("operation", "acquire_lease").Wrap(sanitized)
}

func sanitizePathError(err error) error {
	if err == nil {
		return nil
	}

	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		unwrapped := joined.Unwrap()
		sanitized := make([]error, len(unwrapped))

		for i, u := range unwrapped {
			sanitized[i] = sanitizePathError(u)
		}

		return errors.Join(sanitized...)
	}

	if pathErr, ok := errors.AsType[*os.PathError](err); ok {
		return &os.PathError{
			Op:   pathErr.Op,
			Path: "[sanitized]",
			Err:  sanitizePathError(pathErr.Err),
		}
	}

	if linkErr, ok := errors.AsType[*os.LinkError](err); ok {
		return &os.LinkError{
			Op:  linkErr.Op,
			Old: "[sanitized]",
			New: "[sanitized]",
			Err: sanitizePathError(linkErr.Err),
		}
	}

	return err
}
