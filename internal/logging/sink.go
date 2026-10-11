//go:build linux || darwin

package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sys/unix"
)

const (
	parentDirectoryMode fs.FileMode = 0o700
	logFileMode         fs.FileMode = 0o600
)

type sinkFile interface {
	io.Writer
	io.Closer
}

type Sink struct {
	mu sync.Mutex

	path      string
	baseName  string
	parentDir *os.File
	file      sinkFile
	lockFile  *os.File
	rotation  *rotationState
	logger    zerolog.Logger
	writeErr  error
	closeErr  error
	closed    bool
}

type timestampHook struct{}

func (timestampHook) Run(event *zerolog.Event, _ zerolog.Level, _ string) {
	event.Str("time", time.Now().UTC().Format(time.RFC3339Nano))
}

func Open(path string) (*Sink, error) {
	cleanPath, err := validateInitialPath(path)
	if err != nil {
		return nil, err
	}

	parentFile, baseName, err := openPinnedParentDir(cleanPath, true)
	if err != nil {
		return nil, err
	}

	lockFile, err := acquireProcessLockFile(parentFile, baseName)
	if err != nil {
		closeErr := parentFile.Close()

		return nil, errors.Join(err, closeErr)
	}

	sink, err := setupSinkFiles(parentFile, lockFile, cleanPath, baseName)
	if err != nil {
		unlockErr := setLogFileLock(lockFile, unix.LOCK_UN)
		closeLockErr := lockFile.Close()
		closeParentErr := parentFile.Close()

		return nil, errors.Join(err, unlockErr, closeLockErr, closeParentErr)
	}

	unlockErr := setLogFileLock(lockFile, unix.LOCK_UN)
	if unlockErr != nil {
		closeErr := sink.Close()

		return nil, errors.Join(unlockErr, closeErr)
	}

	return sink, nil
}

func setupSinkFiles(parentFile, lockFile *os.File, cleanPath, baseName string) (*Sink, error) {
	if err := cleanStaleRotationTemp(parentFile, baseName); err != nil {
		return nil, err
	}

	file, err := openOrCreateActiveFile(parentFile, cleanPath, baseName)
	if err != nil {
		return nil, err
	}

	if err := recoverLogTail(file); err != nil {
		closeErr := file.Close()

		return nil, errors.Join(err, closeErr)
	}

	sink := newRotatingSink(file, cleanPath, baseName, parentFile, defaultRotationPolicy())
	sink.lockFile = lockFile

	return sink, nil
}

func validateInitialPath(path string) (string, error) {
	if path == "" {
		return "", wrapErr(ErrLogPathEmpty, "log_path_empty", "validate log path")
	}

	cleanPath := filepath.Clean(path)

	pathInfo, lstatErr := os.Lstat(cleanPath)
	if lstatErr == nil && (pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular()) {
		return "", wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "inspect log file")
	}

	return cleanPath, nil
}

func openPinnedParentDir(path string, create bool) (*os.File, string, error) {
	if path == "" {
		return nil, "", wrapErr(ErrLogPathEmpty, "log_path_empty", "validate log path")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, "", wrapErr(err, "log_open_failed", "resolve absolute log path")
	}

	cleanPath := filepath.Clean(absPath)
	parentPath := filepath.Dir(cleanPath)
	baseName := filepath.Base(cleanPath)

	rootFd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", wrapErr(err, "log_open_failed", "open filesystem root")
	}

	currentFd := rootFd

	for part := range strings.SplitSeq(strings.TrimPrefix(parentPath, "/"), "/") {
		if part == "" || part == "." {
			continue
		}

		nextFd, compErr := openNextParentComponent(currentFd, part, create)
		if compErr != nil {
			return nil, "", compErr
		}

		currentFd = nextFd
	}

	return os.NewFile(uintptr(currentFd), parentPath), baseName, nil
}

func openNextParentComponent(currentFd int, part string, create bool) (int, error) {
	nextFd, openErr := unix.Openat(currentFd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if openErr != nil {
		if create && errors.Is(openErr, unix.ENOENT) {
			mkErr := unix.Mkdirat(currentFd, part, uint32(parentDirectoryMode))
			if mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
				closeErr := unix.Close(currentFd)

				return -1, errors.Join(wrapErr(mkErr, "log_open_failed", "create directory component"), closeErr)
			}

			nextFd, openErr = unix.Openat(currentFd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}

		if openErr != nil {
			return -1, handleOpenatError(currentFd, part, openErr)
		}
	}

	closeErr := unix.Close(currentFd)
	if closeErr != nil {
		return -1, wrapErr(errors.Join(closeErr, unix.Close(nextFd)), "log_open_failed", "close parent directory component")
	}

	return nextFd, nil
}

func handleOpenatError(currentFd int, part string, openErr error) error {
	var (
		stat   unix.Stat_t
		retErr error
	)

	if fstatErr := unix.Fstatat(currentFd, part, &stat, unix.AT_SYMLINK_NOFOLLOW); fstatErr == nil {
		if stat.Mode&unix.S_IFMT == unix.S_IFLNK || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			retErr = wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "parent directory component is not regular")
		}
	}

	closeErr := unix.Close(currentFd)
	if retErr != nil {
		return errors.Join(retErr, closeErr)
	}

	if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
		return errors.Join(wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "parent directory component is not regular"), closeErr)
	}

	return errors.Join(wrapErr(openErr, "log_open_failed", "open directory component"), closeErr)
}

func acquireProcessLockFile(parentFile *os.File, baseName string) (*os.File, error) {
	lockFile, err := openProcessLockFileDescriptor(parentFile, baseName)
	if err != nil {
		return nil, err
	}

	if err := setLogFileLock(lockFile, unix.LOCK_EX); err != nil {
		closeErr := lockFile.Close()

		return nil, errors.Join(err, closeErr)
	}

	return lockFile, nil
}

func acquireProcessLockFileWithContext(ctx context.Context, parentFile *os.File, baseName string) (*os.File, error) {
	lockFile, err := openProcessLockFileDescriptor(parentFile, baseName)
	if err != nil {
		return nil, err
	}

	if err := setLogFileLockContext(ctx, lockFile, unix.LOCK_EX); err != nil {
		closeErr := lockFile.Close()

		return nil, errors.Join(err, closeErr)
	}

	return lockFile, nil
}

func openProcessLockFileDescriptor(parentFile *os.File, baseName string) (*os.File, error) {
	parentFd := int(parentFile.Fd())
	lockName := baseName + ".lock"

	lockFd, err := unix.Openat(parentFd, lockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "open log lock file")
		}

		return nil, wrapErr(err, "log_lock_failed", "open log lock file")
	}

	var stat unix.Stat_t
	if err := unix.Fstat(lockFd, &stat); err != nil {
		closeErr := unix.Close(lockFd)

		return nil, errors.Join(wrapErr(err, "log_lock_failed", "inspect log lock file"), closeErr)
	}

	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		closeErr := unix.Close(lockFd)

		return nil, errors.Join(wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "inspect log lock file"), closeErr)
	}

	if fs.FileMode(stat.Mode).Perm() != logFileMode {
		if err := unix.Fchmod(lockFd, 0o600); err != nil {
			closeErr := unix.Close(lockFd)

			return nil, errors.Join(wrapErr(err, "log_lock_failed", "set log lock file permissions"), closeErr)
		}
	}

	if _, err := unix.FcntlInt(uintptr(lockFd), unix.F_SETFL, unix.O_RDWR); err != nil {
		closeErr := unix.Close(lockFd)

		return nil, errors.Join(wrapErr(err, "log_lock_failed", "configure log lock file flags"), closeErr)
	}

	lockFile := os.NewFile(uintptr(lockFd), filepath.Join(parentFile.Name(), lockName))

	return lockFile, nil
}

func openOrCreateActiveFile(parentFile *os.File, path, baseName string) (*os.File, error) {
	parentFd := int(parentFile.Fd())

	fd, openErr := unix.Openat(parentFd, baseName, unix.O_CREAT|unix.O_EXCL|unix.O_APPEND|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if openErr == nil {
		return configureActiveFD(fd, path)
	}

	if !errors.Is(openErr, unix.EEXIST) && !errors.Is(openErr, fs.ErrExist) {
		if errors.Is(openErr, unix.ELOOP) {
			return nil, wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "open log file")
		}

		return nil, wrapErr(openErr, "log_open_failed", "create log file")
	}

	return openExisting(parentFile, path, baseName, nil)
}

func configureActiveFD(fd int, path string) (*os.File, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		closeErr := unix.Close(fd)

		return nil, errors.Join(wrapErr(err, "log_open_failed", "inspect active log file"), closeErr)
	}

	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		closeErr := unix.Close(fd)

		return nil, errors.Join(wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "inspect active log file"), closeErr)
	}

	if fs.FileMode(stat.Mode).Perm() != logFileMode {
		if err := unix.Fchmod(fd, 0o600); err != nil {
			closeErr := unix.Close(fd)

			return nil, errors.Join(wrapErr(err, "log_open_failed", "set active log file permissions"), closeErr)
		}
	}

	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, unix.O_APPEND|unix.O_RDWR); err != nil {
		closeErr := unix.Close(fd)

		return nil, errors.Join(wrapErr(err, "log_open_failed", "configure active log file flags"), closeErr)
	}

	return os.NewFile(uintptr(fd), path), nil
}

func openExisting(parentFile *os.File, path, baseName string, expectedStat *unix.Stat_t) (*os.File, error) {
	parentFd := int(parentFile.Fd())

	var pathStat unix.Stat_t

	statErr := unix.Fstatat(parentFd, baseName, &pathStat, unix.AT_SYMLINK_NOFOLLOW)
	if statErr != nil {
		return nil, wrapErr(statErr, "log_open_failed", "inspect existing log file before open")
	}

	if pathStat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "inspect existing log file")
	}

	fd, err := unix.Openat(parentFd, baseName, unix.O_APPEND|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "open log file")
		}

		return nil, wrapErr(err, "log_open_failed", "open log file")
	}

	file, err := configureActiveFD(fd, path)
	if err != nil {
		return nil, err
	}

	if expectedStat != nil {
		var fileStat unix.Stat_t
		if err := unix.Fstat(fd, &fileStat); err != nil {
			closeErr := file.Close()

			return nil, errors.Join(wrapErr(err, "log_open_failed", "inspect open log file"), closeErr)
		}

		if fileStat.Dev != expectedStat.Dev || fileStat.Ino != expectedStat.Ino {
			closeErr := file.Close()

			return nil, errors.Join(wrapErr(ErrLogPathChanged, "log_path_changed", "inspect open log file"), closeErr)
		}
	}

	return file, nil
}

func newSink(file sinkFile) *Sink {
	sink := &Sink{
		mu:        sync.Mutex{},
		path:      "",
		baseName:  "",
		parentDir: nil,
		file:      file,
		lockFile:  nil,
		rotation:  nil,
		logger:    zerolog.Logger{},
		writeErr:  nil,
		closeErr:  nil,
		closed:    false,
	}
	sink.logger = zerolog.New(sink).
		Level(zerolog.InfoLevel).
		Hook(timestampHook{})

	return sink
}

func (s *Sink) Logger() zerolog.Logger {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.logger
}

func (s *Sink) SetLevel(level zerolog.Level) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logger = s.logger.Level(level)
}

func (s *Sink) Write(p []byte) (int, error) {
	if !validJSONEvent(p) {
		err := wrapErr(ErrLogInvalidRecord, "log_write_failed", "invalid or partial JSON log event")

		s.mu.Lock()
		s.writeErr = errors.Join(s.writeErr, err)
		s.mu.Unlock()

		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		err := wrapErr(os.ErrClosed, "log_write_failed", "write to closed log sink")
		s.writeErr = errors.Join(s.writeErr, err)

		return len(p), nil
	}

	n, err := s.writeEvent(p)
	if err != nil {
		s.writeErr = errors.Join(s.writeErr, wrapErr(err, "log_write_failed", "write log event"))
	} else if n != len(p) {
		s.writeErr = errors.Join(s.writeErr, wrapErr(io.ErrShortWrite, "log_write_failed", "write log event"))
	}

	return len(p), nil
}

func validJSONEvent(p []byte) bool {
	if len(p) < 3 || p[len(p)-1] != '\n' {
		return false
	}

	trimmed := bytes.TrimSpace(p)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return false
	}

	return json.Valid(trimmed)
}

func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errors.Join(s.closeErr, s.writeErr)
	}

	s.closed = true

	var fileCloseErr error

	if s.file != nil {
		if err := s.file.Close(); err != nil {
			fileCloseErr = wrapErr(err, "log_close_failed", "close log file")
		}
	}

	var lockCloseErr error

	if s.lockFile != nil {
		if err := s.lockFile.Close(); err != nil {
			lockCloseErr = wrapErr(err, "log_lock_failed", "close log lock file")
		}
	}

	var parentCloseErr error

	if s.parentDir != nil {
		if err := s.parentDir.Close(); err != nil {
			parentCloseErr = wrapErr(err, "log_close_failed", "close pinned parent directory")
		}
	}

	s.closeErr = errors.Join(s.writeErr, fileCloseErr, lockCloseErr, parentCloseErr)

	return s.closeErr
}
