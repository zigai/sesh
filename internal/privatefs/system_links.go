//go:build linux || darwin

package privatefs

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	maxSystemLinkHops = 40
	linkBufferSize    = 4096
)

var ErrSystemLinkLoop = fmt.Errorf("%w: too many system symbolic links", ErrUnsafe)

type linkWalk struct {
	fd         int
	resolved   string
	pending    []string
	trusted    func(*unix.Stat_t) bool
	trustedDir bool
	hops       int
}

// ResolveSystemSymlinks rewrites an absolute path so it no longer passes through
// symbolic links that only root can change, such as /var on macOS. It stops at
// the first directory or link root does not control and keeps the rest of the
// path unchanged, so callers still reject every other symbolic link.
func ResolveSystemSymlinks(path string) (string, error) {
	return resolveTrustedLinks(path, systemOwned)
}

func systemOwned(stat *unix.Stat_t) bool {
	if stat.Uid != 0 {
		return false
	}

	return stat.Mode&unix.S_IFMT == unix.S_IFLNK || stat.Mode&0o022 == 0
}

func resolveTrustedLinks(path string, trusted func(*unix.Stat_t) bool) (string, error) {
	if !filepath.IsAbs(path) {
		return path, nil
	}

	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("open filesystem root: %w", err)
	}

	walk := linkWalk{
		fd:         fd,
		resolved:   "/",
		pending:    strings.Split(path, "/"),
		trusted:    trusted,
		trustedDir: false,
		hops:       0,
	}
	defer walk.close()

	walk.trustedDir = walk.statTrusted()

	for walk.trustedDir && len(walk.pending) > 0 {
		if err := walk.step(); err != nil {
			return "", err
		}
	}

	return walk.result()
}

func (w *linkWalk) step() error {
	name := w.pending[0]
	if name == "" || name == "." {
		w.pending = w.pending[1:]

		return nil
	}

	if name == ".." {
		w.pending = w.pending[1:]
		w.descend("..", filepath.Dir(w.resolved))

		return nil
	}

	var stat unix.Stat_t
	if err := unix.Fstatat(w.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			w.trustedDir = false

			return nil
		}

		return fmt.Errorf("inspect system path component: %w", err)
	}

	switch {
	case stat.Mode&unix.S_IFMT == unix.S_IFDIR:
		w.pending = w.pending[1:]
		w.descend(name, filepath.Join(w.resolved, name))

		return nil
	case stat.Mode&unix.S_IFMT == unix.S_IFLNK && w.trusted(&stat):
		return w.follow(name)
	default:
		w.trustedDir = false

		return nil
	}
}

func (w *linkWalk) descend(name, resolved string) {
	w.resolved = resolved
	w.trustedDir = false

	fd, err := unix.Openat(w.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}

	w.close()
	w.fd = fd
	w.trustedDir = w.statTrusted()
}

func (w *linkWalk) follow(name string) error {
	w.hops++
	if w.hops > maxSystemLinkHops {
		return ErrSystemLinkLoop
	}

	buffer := make([]byte, linkBufferSize)

	size, err := unix.Readlinkat(w.fd, name, buffer)
	if err != nil {
		return fmt.Errorf("read system symbolic link: %w", err)
	}

	if size == len(buffer) {
		return fmt.Errorf("%w: system symbolic link target too long", ErrUnsafe)
	}

	target := string(buffer[:size])
	w.pending = append(strings.Split(target, "/"), w.pending[1:]...)

	if filepath.IsAbs(target) {
		w.descend("/", "/")
	}

	return nil
}

func (w *linkWalk) statTrusted() bool {
	var stat unix.Stat_t
	if err := unix.Fstat(w.fd, &stat); err != nil {
		return false
	}

	return w.trusted(&stat)
}

func (w *linkWalk) result() (string, error) {
	if slices.Contains(w.pending, "..") {
		return "", fmt.Errorf("%w: system symbolic link leaves the path", ErrUnsafe)
	}

	return filepath.Join(append([]string{w.resolved}, w.pending...)...), nil
}

func (w *linkWalk) close() {
	_ = unix.Close(w.fd)
}
