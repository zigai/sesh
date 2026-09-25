//go:build linux || darwin

package remote

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
)

const (
	privateDirectoryMode = 0o700
	privateFileMode      = 0o600
	temporaryNameBytes   = 8
)

var (
	ErrUnsafePath               = errors.New("unsafe remote storage path")
	errPrivateDirectoryRequired = errors.New("remote storage directory must be an absolute cleaned path")
)

func EnsureDir(path string) error {
	dir, err := privatefs.Ensure(path)
	if err != nil {
		return oops.In("remote").Code("remote_storage_failed").With("operation", "ensure_dir").Wrapf(err, "securing remote storage directory")
	}

	if err := dir.Close(); err != nil {
		return oops.In("remote").Code("remote_storage_failed").With("operation", "ensure_dir").Wrapf(err, "closing remote storage directory")
	}

	return nil
}

func openPrivateDir(path string, create bool) (*privatefs.Dir, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errPrivateDirectoryRequired
	}

	if create {
		dir, err := privatefs.Ensure(path)
		if err != nil {
			return nil, fmt.Errorf("ensuring private directory: %w", err)
		}

		return dir, nil
	}

	dir, err := privatefs.Open(path, false)
	if err != nil {
		return nil, fmt.Errorf("opening private directory: %w", err)
	}

	return dir, nil
}

func writeFileAtomic(dir *privatefs.Dir, name string, data []byte) error {
	temporary, err := temporaryName(name)
	if err != nil {
		return err
	}

	file, err := dir.File(temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return fmt.Errorf("creating private temporary file: %w", err)
	}

	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()

	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		removeErr := dir.Remove(temporary)

		return errors.Join(fmt.Errorf("writing private file: %w", err), removeErr)
	}

	if err := dir.Rename(temporary, dir, name); err != nil {
		removeErr := dir.Remove(temporary)

		return errors.Join(fmt.Errorf("publishing private file: %w", err), removeErr)
	}

	if err := dir.Sync(); err != nil {
		return fmt.Errorf("syncing private directory: %w", err)
	}

	return nil
}

func removeFile(dir *privatefs.Dir, name string) error {
	err := dir.Remove(name)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}

	return fmt.Errorf("removing private file: %w", err)
}

func temporaryName(name string) (string, error) {
	suffix := make([]byte, temporaryNameBytes)
	if _, err := io.ReadFull(rand.Reader, suffix); err != nil {
		return "", fmt.Errorf("generating temporary name: %w", err)
	}

	return "." + name + "." + hex.EncodeToString(suffix) + ".tmp", nil
}
