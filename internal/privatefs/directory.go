//go:build linux || darwin

package privatefs

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var (
	ErrUnsafe   = errors.New("unsafe private storage entry")
	ErrUnlinked = fmt.Errorf("%w: last link removed", ErrUnsafe)
	ErrLimit    = errors.New("private storage inventory limit exceeded")
)

type Dir struct{ file *os.File }

type EntryIdentity struct {
	Inode uint64
	UID   uint32
	Mode  uint64
}

func Open(path string, create bool) (*Dir, error) {
	return openPath(path, create, false, true)
}

func Ensure(path string) (*Dir, error) {
	return openPath(path, true, true, true)
}

func OpenParent(path string, create bool) (*Dir, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, "", ErrUnsafe
	}

	name := filepath.Base(path)
	if !validName(name) {
		return nil, "", ErrUnsafe
	}

	parent := filepath.Dir(path)
	if parent == "/" {
		dir, err := openRoot()

		return dir, name, err
	}

	dir, err := openPath(parent, create, create, false)
	if err != nil {
		return nil, "", err
	}

	return dir, name, nil
}

func openPath(path string, create, parents, privateLeaf bool) (*Dir, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrUnsafe
	}

	path, err := ResolveSystemSymlinks(path)
	if err != nil {
		return nil, err
	}

	dir, err := openRoot()
	if err != nil {
		return nil, err
	}

	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, part := range parts {
		last := index == len(parts)-1
		next, openErr := dir.child(part, create && (last || parents), last && privateLeaf)

		closeErr := dir.Close()
		if err := errors.Join(openErr, closeErr); err != nil {
			if next != nil {
				err = errors.Join(err, next.Close())
			}

			return nil, err
		}

		dir = next
	}

	return dir, nil
}

func openRoot() (*Dir, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}

	return &Dir{file: os.NewFile(uintptr(fd), "/")}, nil
}

func (d *Dir) Child(name string, create bool) (*Dir, error) {
	return d.child(name, create, true)
}

func (d *Dir) CreateDir(name string) (*Dir, error) {
	if !validName(name) {
		return nil, ErrUnsafe
	}

	if err := unix.Mkdirat(int(d.file.Fd()), name, 0o700); err != nil {
		return nil, fmt.Errorf("create new private directory: %w", err)
	}

	if err := d.Sync(); err != nil {
		return nil, err
	}

	return d.child(name, false, true)
}

func (d *Dir) Close() error {
	if err := d.file.Close(); err != nil {
		return fmt.Errorf("close private directory: %w", err)
	}

	return nil
}

func (d *Dir) Sync() error {
	if err := d.file.Sync(); err != nil {
		return fmt.Errorf("sync private directory: %w", err)
	}

	return nil
}

func (d *Dir) File(name string, flags int) (*os.File, error) {
	if !validName(name) || flags&unix.O_TRUNC != 0 {
		return nil, ErrUnsafe
	}

	fd, err := unix.Openat(int(d.file.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open private file: %w", err)
	}

	file := os.NewFile(uintptr(fd), name)
	if err := checkMode(file, false); err != nil {
		return nil, errors.Join(err, file.Close())
	}

	return file, nil
}

func (d *Dir) Names() ([]string, error) {
	return d.names(-1)
}

func (d *Dir) NamesLimit(limit int) ([]string, error) {
	if limit < 0 || limit == math.MaxInt {
		return nil, ErrUnsafe
	}

	names, err := d.names(limit + 1)
	if err != nil {
		return nil, err
	}

	if len(names) > limit {
		return nil, ErrLimit
	}

	return names, nil
}

func (d *Dir) Read(name string, limit int64) ([]byte, error) {
	if limit < 0 || limit == 1<<63-1 {
		return nil, ErrUnsafe
	}

	file, err := d.File(name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}

	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, fmt.Errorf("read private file: %w", err)
	}

	if int64(len(data)) > limit {
		return nil, ErrUnsafe
	}

	return data, nil
}

func (d *Dir) Inspect(name string) (EntryIdentity, error) {
	if !validName(name) {
		return EntryIdentity{}, ErrUnsafe
	}

	var stat unix.Stat_t
	if err := unix.Fstatat(int(d.file.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return EntryIdentity{}, fmt.Errorf("inspect private entry: %w", err)
	}

	return EntryIdentity{Inode: stat.Ino, UID: stat.Uid, Mode: uint64(stat.Mode)}, nil
}

func (d *Dir) Rename(name string, target *Dir, destination string) error {
	if !validName(name) || !validName(destination) {
		return ErrUnsafe
	}

	if err := unix.Renameat(int(d.file.Fd()), name, int(target.file.Fd()), destination); err != nil {
		return fmt.Errorf("rename private entry: %w", err)
	}

	return nil
}

func (d *Dir) Remove(name string) error {
	if !validName(name) {
		return ErrUnsafe
	}

	if err := unix.Unlinkat(int(d.file.Fd()), name, 0); err != nil {
		return fmt.Errorf("remove private file: %w", err)
	}

	return nil
}

func (d *Dir) RemoveDir(name string) error {
	if !validName(name) {
		return ErrUnsafe
	}

	if err := unix.Unlinkat(int(d.file.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove private directory: %w", err)
	}

	return nil
}

func (d *Dir) names(count int) ([]string, error) {
	fd, err := unix.Openat(int(d.file.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open directory listing: %w", err)
	}

	file := os.NewFile(uintptr(fd), ".")

	names, readErr := file.Readdirnames(count)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}

	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, fmt.Errorf("read directory listing: %w", err)
	}

	return names, nil
}

func (d *Dir) child(name string, create, private bool) (*Dir, error) {
	if !validName(name) {
		return nil, ErrUnsafe
	}

	created := false

	if create {
		err := unix.Mkdirat(int(d.file.Fd()), name, 0o700)
		if err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("create private directory: %w", err)
		}

		created = err == nil
		if created {
			if err := d.Sync(); err != nil {
				return nil, err
			}
		}
	}

	fd, err := unix.Openat(int(d.file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open private directory: %w", err)
	}

	file := os.NewFile(uintptr(fd), name)
	if private || created {
		if err := checkMode(file, true); err != nil {
			return nil, errors.Join(err, file.Close())
		}
	}

	return &Dir{file: file}, nil
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

func checkMode(file *os.File, directory bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("inspect private entry: %w", err)
	}

	if int64(stat.Uid) != int64(os.Geteuid()) {
		return ErrUnsafe
	}

	switch {
	case directory:
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != 0o700 {
			return ErrUnsafe
		}
	case stat.Nlink == 0:
		return ErrUnlinked
	case stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Nlink != 1:
		return ErrUnsafe
	}

	return nil
}
