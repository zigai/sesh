//go:build linux || darwin

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/samber/oops"
	"golang.org/x/sys/unix"
)

const maxConfigFileSize int64 = 1 << 20

var ErrFileTooLarge = errors.New("config file exceeds size limit")

type pinnedDir struct {
	fd int
}

func openPinnedDir(dirPath string, create bool) (*pinnedDir, error) {
	cleanPath := filepath.Clean(dirPath)
	if !filepath.IsAbs(cleanPath) {
		abs, err := filepath.Abs(cleanPath)
		if err != nil {
			return nil, oops.In("config").
				Code("config_invalid_path").
				With("operation", "fs", "reason", "resolve_abs_path").
				Wrap(err)
		}

		cleanPath = filepath.Clean(abs)
	}

	rootFd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, oops.In("config").
			Code("config_open_failed").
			With("operation", "fs", "reason", "open_fs_root").
			Wrap(err)
	}

	currFd := rootFd

	for part := range strings.SplitSeq(strings.TrimPrefix(cleanPath, "/"), "/") {
		if part == "" || part == "." {
			continue
		}

		if part == ".." {
			closeErr := unix.Close(currFd)
			symlinkErr := oops.In("config").
				Code("config_symlink").
				With("operation", "fs", "reason", "symlink_traversal_prohibited").
				Wrap(ErrConfigSymlink)

			return nil, errors.Join(symlinkErr, closeErr)
		}

		childFd, err := openChildDir(currFd, part, create)
		if err != nil {
			closeErr := unix.Close(currFd)

			return nil, errors.Join(err, closeErr)
		}

		if closeErr := unix.Close(currFd); closeErr != nil {
			return nil, oops.In("config").
				Code("config_close_failed").
				With("operation", "fs", "reason", "close_directory").
				Wrap(errors.Join(closeErr, unix.Close(childFd)))
		}

		currFd = childFd
	}

	return &pinnedDir{fd: currFd}, nil
}

func checkChildSymlink(parentFd int, part string) error {
	var checkStat unix.Stat_t
	if fstatatErr := unix.Fstatat(parentFd, part, &checkStat, unix.AT_SYMLINK_NOFOLLOW); fstatatErr == nil {
		if checkStat.Mode&unix.S_IFMT == unix.S_IFLNK || checkStat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return oops.In("config").
				Code("config_symlink").
				With("operation", "fs", "reason", "symlink_traversal_prohibited").
				Wrap(ErrConfigSymlink)
		}
	}

	return nil
}

func mkdirChild(parentFd int, part string) error {
	mkdirErr := unix.Mkdirat(parentFd, part, 0o700)
	if mkdirErr == nil {
		if syncErr := unix.Fsync(parentFd); syncErr != nil {
			return oops.In("config").
				Code("config_sync_failed").
				With("operation", "fs", "reason", "sync_parent_directory").
				Wrap(syncErr)
		}

		return nil
	}

	if !errors.Is(mkdirErr, unix.EEXIST) {
		return oops.In("config").
			Code("config_mkdir_failed").
			With("operation", "fs", "reason", "mkdir_failed").
			Wrap(mkdirErr)
	}

	return nil
}

func openChildDir(parentFd int, part string, create bool) (int, error) {
	if err := checkChildSymlink(parentFd, part); err != nil {
		return -1, err
	}

	if create {
		if err := mkdirChild(parentFd, part); err != nil {
			return -1, err
		}
	}

	childFd, openErr := unix.Openat(parentFd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if openErr != nil {
		if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
			return -1, oops.In("config").
				Code("config_symlink").
				With("operation", "fs", "reason", "symlink_traversal_prohibited").
				Wrap(ErrConfigSymlink)
		}

		if errors.Is(openErr, unix.ENOENT) {
			return -1, os.ErrNotExist
		}

		return -1, oops.In("config").
			Code("config_open_failed").
			With("operation", "fs", "reason", "open_directory_component").
			Wrap(openErr)
	}

	var stat unix.Stat_t
	if fstatErr := unix.Fstat(childFd, &stat); fstatErr != nil || (stat.Mode&unix.S_IFMT != unix.S_IFDIR) {
		return -1, oops.In("config").
			Code("config_symlink").
			With("operation", "fs", "reason", "symlink_traversal_prohibited").
			Wrap(errors.Join(ErrConfigSymlink, fstatErr, unix.Close(childFd)))
	}

	return childFd, nil
}

func (d *pinnedDir) Close() error {
	if d == nil || d.fd < 0 {
		return nil
	}

	err := unix.Close(d.fd)
	d.fd = -1

	if err != nil {
		return oops.In("config").
			Code("config_close_failed").
			With("operation", "fs", "reason", "close_directory").
			Wrap(err)
	}

	return nil
}

func (d *pinnedDir) Sync() error {
	if d == nil || d.fd < 0 {
		return nil
	}

	if err := unix.Fsync(d.fd); err != nil {
		return oops.In("config").
			Code("config_sync_failed").
			With("operation", "fs", "reason", "sync_directory").
			Wrap(err)
	}

	return nil
}

func (d *pinnedDir) ReadFile(name string, maxBytes int64) ([]byte, error) {
	if name == "" || strings.Contains(name, "/") {
		return nil, oops.In("config").
			Code("config_invalid_path").
			With("operation", "load", "reason", "invalid_file_name").
			Wrap(ErrConfigSymlink)
	}

	file, err := openRegularFile(d.fd, name, maxBytes)
	if err != nil {
		return nil, err
	}

	data, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	closeErr := file.Close()

	if combinedErr := errors.Join(readErr, closeErr); combinedErr != nil {
		return nil, oops.In("config").
			Code("config_read_failed").
			With("operation", "load", "reason", "read_failed").
			Wrap(combinedErr)
	}

	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, oops.In("config").
			Code("config_file_too_large").
			With("operation", "load", "reason", "file_too_large").
			Wrap(ErrFileTooLarge)
	}

	return data, nil
}

func openRegularFile(dirFd int, name string, maxBytes int64) (*os.File, error) {
	var preStat unix.Stat_t
	if fstatatErr := unix.Fstatat(dirFd, name, &preStat, unix.AT_SYMLINK_NOFOLLOW); fstatatErr == nil {
		if preStat.Mode&unix.S_IFMT == unix.S_IFLNK || preStat.Mode&unix.S_IFMT != unix.S_IFREG {
			return nil, oops.In("config").
				Code("config_symlink").
				With("operation", "load", "reason", "symlink_traversal_prohibited").
				Wrap(ErrConfigSymlink)
		}
	}

	fileFd, err := unix.Openat(dirFd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, oops.In("config").
				Code("config_symlink").
				With("operation", "load", "reason", "symlink_traversal_prohibited").
				Wrap(ErrConfigSymlink)
		}

		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}

		return nil, oops.In("config").
			Code("config_read_failed").
			With("operation", "load", "reason", "open_config_file").
			Wrap(err)
	}

	var stat unix.Stat_t
	if fstatErr := unix.Fstat(fileFd, &stat); fstatErr != nil {
		return nil, oops.In("config").
			Code("config_stat_failed").
			With("operation", "load", "reason", "stat_failed").
			Wrap(errors.Join(fstatErr, unix.Close(fileFd)))
	}

	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, oops.In("config").
			Code("config_symlink").
			With("operation", "load", "reason", "symlink_traversal_prohibited").
			Wrap(errors.Join(ErrConfigSymlink, unix.Close(fileFd)))
	}

	if maxBytes > 0 && stat.Size > maxBytes {
		return nil, oops.In("config").
			Code("config_file_too_large").
			With("operation", "load", "reason", "file_too_large").
			Wrap(errors.Join(ErrFileTooLarge, unix.Close(fileFd)))
	}

	return os.NewFile(uintptr(fileFd), name), nil
}

func (d *pinnedDir) CreateExclusive(name string, data []byte) error {
	if name == "" || strings.Contains(name, "/") {
		return oops.In("config").
			Code("config_invalid_path").
			With("operation", "init", "reason", "invalid_file_name").
			Wrap(ErrConfigSymlink)
	}

	tmpName, err := d.stageTempFile(name, data)
	if err != nil {
		return err
	}

	// Linkat provides atomic no-clobber publication: fails with EEXIST if destination already exists.
	// Never fallback to rename: another creator's valid configuration must not be overwritten.
	linkErr := unix.Linkat(d.fd, tmpName, d.fd, name, 0)
	unlinkErr := unix.Unlinkat(d.fd, tmpName, 0)

	if linkErr != nil {
		if errors.Is(linkErr, unix.EEXIST) {
			return errors.Join(
				oops.In("config").
					Code("config_exists").
					With("operation", "init", "reason", "config_already_exists").
					Wrap(ErrConfigExists),
				unlinkErr,
			)
		}

		if errors.Is(linkErr, unix.ELOOP) {
			return errors.Join(
				oops.In("config").
					Code("config_symlink").
					With("operation", "init", "reason", "symlink_traversal_prohibited").
					Wrap(ErrConfigSymlink),
				unlinkErr,
			)
		}

		return errors.Join(
			oops.In("config").
				Code("config_publish_failed").
				With("operation", "init", "reason", "publish_failed").
				Wrap(linkErr),
			unlinkErr,
		)
	}

	if unlinkErr != nil {
		return oops.In("config").
			Code("config_cleanup_failed").
			With("operation", "init", "reason", "cleanup_failed").
			Wrap(unlinkErr)
	}

	return d.Sync()
}

func (d *pinnedDir) WriteAtomic(name string, data []byte) error {
	if name == "" || strings.Contains(name, "/") {
		return oops.In("config").
			Code("config_invalid_path").
			With("operation", "save", "reason", "invalid_file_name").
			Wrap(ErrConfigSymlink)
	}

	tmpName, err := d.stageTempFile(name, data)
	if err != nil {
		return err
	}

	var stat unix.Stat_t
	if fstatErr := unix.Fstatat(d.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); fstatErr == nil {
		if stat.Mode&unix.S_IFMT == unix.S_IFLNK || stat.Mode&unix.S_IFMT != unix.S_IFREG {
			unlinkErr := unix.Unlinkat(d.fd, tmpName, 0)

			return errors.Join(
				oops.In("config").
					Code("config_symlink").
					With("operation", "save", "reason", "symlink_traversal_prohibited").
					Wrap(ErrConfigSymlink),
				unlinkErr,
			)
		}
	}

	if renameErr := unix.Renameat(d.fd, tmpName, d.fd, name); renameErr != nil {
		unlinkErr := unix.Unlinkat(d.fd, tmpName, 0)

		return errors.Join(
			oops.In("config").
				Code("config_publish_failed").
				With("operation", "save", "reason", "publish_failed").
				Wrap(renameErr),
			unlinkErr,
		)
	}

	return d.Sync()
}

func (d *pinnedDir) stageTempFile(name string, data []byte) (string, error) {
	var stat unix.Stat_t
	if fstatErr := unix.Fstatat(d.fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); fstatErr == nil {
		if stat.Mode&unix.S_IFMT == unix.S_IFLNK || stat.Mode&unix.S_IFMT != unix.S_IFREG {
			return "", oops.In("config").
				Code("config_symlink").
				With("operation", "fs", "reason", "symlink_traversal_prohibited").
				Wrap(ErrConfigSymlink)
		}
	}

	tmpName := fmt.Sprintf(".%s.tmp-%d-%d", name, os.Getpid(), time.Now().UnixNano())

	tmpFd, err := unix.Openat(d.fd, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return "", oops.In("config").
			Code("config_temp_failed").
			With("operation", "fs", "reason", "temp_create_failed").
			Wrap(err)
	}

	file := os.NewFile(uintptr(tmpFd), tmpName)
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()

	if combinedErr := errors.Join(writeErr, syncErr, closeErr); combinedErr != nil {
		unlinkErr := unix.Unlinkat(d.fd, tmpName, 0)

		return "", errors.Join(
			oops.In("config").
				Code("config_write_failed").
				With("operation", "fs", "reason", "write_failed").
				Wrap(combinedErr),
			unlinkErr,
		)
	}

	return tmpName, nil
}
