//go:build linux || darwin

package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultMaxLogBytes    int64         = 100 << 20
	defaultLogBackupCount               = 3
	logTailReadChunkSize                = 4096
	defaultLockTimeout    time.Duration = 5 * time.Second
	lockRetryInterval     time.Duration = 5 * time.Millisecond
)

type rotationPolicy struct {
	maxBytes    int64
	backupCount int
}

type rotationState struct {
	path        string
	baseName    string
	policy      rotationPolicy
	preOpenHook func(parentDir *os.File, baseName string)
}

type rollbackSinkFile interface {
	sinkFile
	io.Seeker
	Stat() (fs.FileInfo, error)
	Truncate(size int64) error
}

func defaultRotationPolicy() rotationPolicy {
	return rotationPolicy{
		maxBytes:    defaultMaxLogBytes,
		backupCount: defaultLogBackupCount,
	}
}

func newRotatingSink(file *os.File, path, baseName string, parentDir *os.File, policy rotationPolicy) *Sink {
	sink := newSink(file)
	if policy.maxBytes > 0 {
		sink.path = path
		sink.baseName = baseName
		sink.parentDir = parentDir
		sink.rotation = &rotationState{
			path:        path,
			baseName:    baseName,
			policy:      policy,
			preOpenHook: nil,
		}
	}

	return sink
}

func setLogFileLock(file *os.File, operation int) error {
	if file == nil {
		return nil
	}

	fd := int(file.Fd())
	if operation == unix.LOCK_UN {
		return releaseLogFileLock(fd)
	}

	return acquireBoundedLock(fd, operation)
}

func setLogFileLockContext(ctx context.Context, file *os.File, operation int) error {
	if file == nil {
		return nil
	}

	fd := int(file.Fd())
	if operation == unix.LOCK_UN {
		return releaseLogFileLock(fd)
	}

	return acquireBoundedLockContext(ctx, fd, operation)
}

func releaseLogFileLock(fd int) error {
	for {
		err := unix.Flock(fd, unix.LOCK_UN)
		if err == nil {
			return nil
		}

		if errors.Is(err, unix.EINTR) {
			continue
		}

		return wrapErr(err, "log_lock_failed", "unlock log file")
	}
}

func acquireBoundedLock(fd, operation int) error {
	deadline := time.Now().Add(defaultLockTimeout)

	for {
		err := unix.Flock(fd, operation|unix.LOCK_NB)
		if err == nil {
			return nil
		}

		if errors.Is(err, unix.EINTR) {
			continue
		}

		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			if time.Now().After(deadline) {
				return wrapErr(ErrLockTimeout, "log_lock_failed", "acquire log lock timeout")
			}

			time.Sleep(lockRetryInterval)

			continue
		}

		return wrapErr(err, "log_lock_failed", "update log file lock")
	}
}

func acquireBoundedLockContext(ctx context.Context, fd, operation int) error {
	deadline := time.Now().Add(defaultLockTimeout)

	for {
		if err := ctx.Err(); err != nil {
			return wrapErr(err, "log_lock_failed", "acquire log lock canceled")
		}

		err := unix.Flock(fd, operation|unix.LOCK_NB)
		if err == nil {
			return nil
		}

		if errors.Is(err, unix.EINTR) {
			continue
		}

		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			if retryErr := handleLockRetry(ctx, deadline); retryErr != nil {
				return retryErr
			}

			continue
		}

		return wrapErr(err, "log_lock_failed", "update log file lock")
	}
}

func handleLockRetry(ctx context.Context, deadline time.Time) error {
	if time.Now().After(deadline) {
		return wrapErr(ErrLockTimeout, "log_lock_failed", "acquire log lock timeout")
	}

	select {
	case <-ctx.Done():
		return wrapErr(ctx.Err(), "log_lock_failed", "acquire log lock canceled")
	case <-time.After(lockRetryInterval):
		return nil
	}
}

func (s *Sink) syncActiveFileLocked(currentFile *os.File) (*os.File, error) {
	if s.parentDir == nil || s.baseName == "" {
		return currentFile, nil
	}

	parentFd := int(s.parentDir.Fd())

	currentInfo, statErr := currentFile.Stat()
	if statErr != nil {
		return currentFile, wrapErr(statErr, "log_open_failed", "inspect current log file")
	}

	var pathStat unix.Stat_t

	statErr = unix.Fstatat(parentFd, s.baseName, &pathStat, unix.AT_SYMLINK_NOFOLLOW)
	if statErr != nil {
		if errors.Is(statErr, unix.ENOENT) {
			return s.recreateActiveLog(currentFile)
		}

		return currentFile, wrapErr(statErr, "log_open_failed", "inspect active log path")
	}

	if pathStat.Mode&unix.S_IFMT != unix.S_IFREG {
		return currentFile, wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "inspect active log path")
	}

	if !sameInode(currentInfo, &pathStat) {
		return s.reopenActiveLog(currentFile, &pathStat)
	}

	return currentFile, nil
}

func sameInode(fi fs.FileInfo, stat *unix.Stat_t) bool {
	sys, ok := fi.Sys().(*unix.Stat_t)
	if !ok {
		return false
	}

	return sys.Dev == stat.Dev && sys.Ino == stat.Ino
}

func (s *Sink) recreateActiveLog(currentFile *os.File) (*os.File, error) {
	newFile, err := openOrCreateActiveLog(s.parentDir, s.path, s.baseName)
	if err != nil {
		return currentFile, err
	}

	closeErr := currentFile.Close()
	s.file = newFile

	if closeErr != nil {
		return newFile, wrapErr(closeErr, "log_close_failed", "close stale log file handle")
	}

	return newFile, nil
}

func (s *Sink) reopenActiveLog(currentFile *os.File, expectedStat *unix.Stat_t) (*os.File, error) {
	newFile, err := openExisting(s.parentDir, s.path, s.baseName, expectedStat)
	if err != nil {
		return currentFile, err
	}

	closeErr := currentFile.Close()
	s.file = newFile

	if closeErr != nil {
		return newFile, wrapErr(closeErr, "log_close_failed", "close stale log file handle")
	}

	return newFile, nil
}

func openOrCreateActiveLog(parentFile *os.File, path, baseName string) (*os.File, error) {
	return openOrCreateActiveFile(parentFile, path, baseName)
}

func recoverLogTail(file *os.File) error {
	lineStart, complete, err := inspectLogTail(file)
	if err != nil {
		return err
	}

	if complete {
		return nil
	}

	if err := file.Truncate(lineStart); err != nil {
		return wrapErr(err, "log_truncate_failed", "truncate incomplete log record")
	}

	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return wrapErr(err, "log_open_failed", "restore log file position after recovery")
	}

	return nil
}

func isJSONLogObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return false
	}

	return json.Valid(trimmed)
}

func inspectLogTail(file *os.File) (int64, bool, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, false, wrapErr(err, "log_open_failed", "inspect log file for recovery")
	}

	if info.Size() == 0 {
		return 0, true, nil
	}

	var lastByte [1]byte
	if _, err := file.ReadAt(lastByte[:], info.Size()-1); err != nil {
		return 0, false, wrapErr(err, "log_open_failed", "read log file tail")
	}

	lineEnd := info.Size()

	hasTrailingNewline := lastByte[0] == '\n'
	if hasTrailingNewline {
		lineEnd--
	}

	lineStart, err := findLogLineStart(file, lineEnd)
	if err != nil {
		return 0, false, err
	}

	lineLength := lineEnd - lineStart
	if lineLength == 0 {
		return 0, true, nil
	}

	if lineLength > maxLogRecordBytes {
		return 0, false, wrapErr(ErrLogForeign, "log_corrupt", "log record in tail exceeds maximum size")
	}

	line := make([]byte, lineLength)

	n, err := file.ReadAt(line, lineStart)
	if err != nil {
		return 0, false, wrapErr(err, "log_open_failed", "read final log record")
	}

	if int64(n) != lineLength {
		return 0, false, wrapErr(io.ErrUnexpectedEOF, "log_open_failed", "read final log record")
	}

	return inspectFinalRecord(file, line, lineStart, hasTrailingNewline)
}

func inspectFinalRecord(file *os.File, line []byte, lineStart int64, hasTrailingNewline bool) (int64, bool, error) {
	if hasTrailingNewline && isJSONLogObject(line) {
		return lineStart, true, nil
	}

	if lineStart > 0 {
		if err := verifyPrecedingLogRecord(file, lineStart); err != nil {
			return 0, false, err
		}

		return lineStart, false, nil
	}

	if hasTrailingNewline {
		return 0, false, wrapErr(ErrLogForeign, "log_corrupt", "foreign or corrupt log file")
	}

	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("{\"")) {
		return 0, false, nil
	}

	return 0, false, wrapErr(ErrLogForeign, "log_corrupt", "foreign or corrupt log file")
}

func verifyPrecedingLogRecord(file *os.File, lineStart int64) error {
	prevEnd := lineStart - 1

	prevStart, err := findLogLineStart(file, prevEnd)
	if err != nil {
		return err
	}

	prevLength := prevEnd - prevStart
	if prevLength <= 0 || prevLength > maxLogRecordBytes {
		return wrapErr(ErrLogForeign, "log_corrupt", "foreign or corrupt log file")
	}

	prevLine := make([]byte, prevLength)

	pn, err := file.ReadAt(prevLine, prevStart)
	if err != nil || int64(pn) != prevLength {
		return wrapErr(ErrLogForeign, "log_corrupt", "foreign or corrupt log file")
	}

	if !isJSONLogObject(prevLine) {
		return wrapErr(ErrLogForeign, "log_corrupt", "foreign or corrupt log file")
	}

	return nil
}

func findLogLineStart(file *os.File, end int64) (int64, error) {
	var (
		buffer  [logTailReadChunkSize]byte
		scanned int64
	)

	for end > 0 {
		start := max(end-int64(len(buffer)), int64(0))
		chunkSize := int(end - start)
		scanned += int64(chunkSize)

		if scanned > maxLogRecordBytes {
			return 0, wrapErr(ErrLogForeign, "log_corrupt", "log line in tail exceeds maximum size")
		}

		n, err := file.ReadAt(buffer[:chunkSize], start)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, wrapErr(err, "log_open_failed", "read log record boundary")
		}

		for index := n - 1; index >= 0; index-- {
			if buffer[index] == '\n' {
				return start + int64(index) + 1, nil
			}
		}

		end = start
	}

	return 0, nil
}

func writeEventWithRollback(file sinkFile, event []byte) (int, error) {
	recoverable, ok := file.(rollbackSinkFile)
	if !ok {
		n, err := file.Write(event)
		if err != nil {
			return n, wrapErr(err, "log_write_failed", "write event")
		}

		return n, nil
	}

	info, err := recoverable.Stat()
	if err != nil {
		return 0, wrapErr(err, "log_write_failed", "inspect log file before write")
	}

	n, writeErr := file.Write(event)
	if n == len(event) {
		if writeErr != nil {
			return n, wrapErr(writeErr, "log_write_failed", "write event")
		}

		return n, nil
	}

	if writeErr == nil {
		writeErr = io.ErrShortWrite
	}

	if err := recoverable.Truncate(info.Size()); err != nil {
		return n, errors.Join(writeErr, wrapErr(err, "log_write_failed", "restore log file after partial write"))
	}

	if _, err := recoverable.Seek(0, io.SeekEnd); err != nil {
		return n, errors.Join(writeErr, wrapErr(err, "log_write_failed", "restore log file position after partial write"))
	}

	return n, writeErr
}

func (s *Sink) writeEvent(event []byte) (int, error) {
	if s.rotation == nil {
		n, err := writeEventWithRollback(s.file, event)
		if err != nil {
			return n, wrapErr(err, "log_write_failed", "write unrotated log event")
		}

		return n, nil
	}

	file, ok := s.file.(*os.File)
	if !ok {
		return 0, wrapErr(ErrRotatingSinkNoFile, "log_write_failed", "write event")
	}

	lockTarget := file
	if s.lockFile != nil {
		lockTarget = s.lockFile
	}

	if err := setLogFileLock(lockTarget, unix.LOCK_EX); err != nil {
		return 0, err
	}

	activeFile, syncErr := s.syncActiveFileLocked(file)
	if syncErr != nil {
		unlockErr := setLogFileLock(lockTarget, unix.LOCK_UN)

		return 0, errors.Join(syncErr, unlockErr)
	}

	file = activeFile

	newFile, rotationErr := rotateLogIfNeeded(file, s.rotation, s.parentDir, int64(len(event)))
	if rotationErr != nil {
		return s.handleRotationFailure(file, newFile, rotationErr, lockTarget, event)
	}

	if newFile != nil && newFile != file {
		file = newFile
		s.file = newFile
	}

	n, writeErr := writeEventWithRollback(file, event)
	if writeErr != nil {
		writeErr = wrapErr(writeErr, "log_write_failed", "write rotated log event")
	}

	unlockErr := setLogFileLock(lockTarget, unix.LOCK_UN)

	return n, errors.Join(writeErr, unlockErr)
}

func (s *Sink) handleRotationFailure(file, newFile *os.File, rotationErr error, lockTarget *os.File, event []byte) (int, error) {
	if newFile == nil {
		unlockErr := setLogFileLock(lockTarget, unix.LOCK_UN)

		return 0, errors.Join(rotationErr, unlockErr)
	}

	if newFile != file {
		file = newFile
		s.file = newFile
	}

	n, writeErr := writeEventWithRollback(file, event)
	if writeErr != nil {
		writeErr = wrapErr(writeErr, "log_write_failed", "write rotated log event")
	}

	unlockErr := setLogFileLock(lockTarget, unix.LOCK_UN)

	return n, errors.Join(rotationErr, writeErr, unlockErr)
}

func rotateLogIfNeeded(file *os.File, state *rotationState, parentDir *os.File, eventBytes int64) (*os.File, error) {
	info, err := file.Stat()
	if err != nil {
		return file, wrapErr(err, "log_rotation_failed", "inspect log file for rotation")
	}

	if info.Size() == 0 || (eventBytes <= state.policy.maxBytes && info.Size() <= state.policy.maxBytes-eventBytes) {
		return file, nil
	}

	return rotateLog(file, state, parentDir)
}

func cleanStaleRotationTemp(parentFile *os.File, baseName string) error {
	parentFd := int(parentFile.Fd())
	tempName := baseName + ".rotate.tmp"

	var stat unix.Stat_t

	err := unix.Fstatat(parentFd, tempName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}

		return wrapErr(err, "log_rotation_failed", "inspect log rotation temporary file")
	}

	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return wrapErr(ErrRotationPathNotRegular, "log_path_not_regular", "inspect log rotation temporary file")
	}

	if err := unix.Unlinkat(parentFd, tempName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return wrapErr(err, "log_rotation_failed", "remove stale rotation temporary file")
	}

	return nil
}

func rotateLog(file *os.File, state *rotationState, parentDir *os.File) (*os.File, error) {
	if err := cleanStaleRotationTemp(parentDir, state.baseName); err != nil {
		return file, err
	}

	if state.policy.backupCount <= 0 {
		return truncateActiveLog(file)
	}

	if err := shiftRotationBackups(parentDir, state.baseName, state.policy.backupCount); err != nil {
		return file, err
	}

	parentFd := int(parentDir.Fd())
	destName := rotationBackupName(state.baseName, 1)

	var stat unix.Stat_t

	statErr := unix.Fstatat(parentFd, destName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if statErr == nil && stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return file, wrapErr(ErrRotationPathNotRegular, "log_path_not_regular", "inspect destination log backup")
	}

	if err := unix.Renameat(parentFd, state.baseName, parentFd, destName); err != nil {
		return file, wrapErr(err, "log_rotation_failed", "rename active log to backup")
	}

	if state.preOpenHook != nil {
		state.preOpenHook(parentDir, state.baseName)
	}

	newFile, err := openOrCreateActiveLog(parentDir, state.path, state.baseName)
	if err != nil {
		return nil, err
	}

	closeErr := file.Close()
	if closeErr != nil {
		closeErr = wrapErr(closeErr, "log_close_failed", "close rotated active log")
	}

	if closeErr != nil {
		return newFile, closeErr
	}

	return newFile, nil
}

func truncateActiveLog(file *os.File) (*os.File, error) {
	if err := file.Truncate(0); err != nil {
		return file, wrapErr(err, "log_rotation_failed", "truncate active log after rotation")
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return file, wrapErr(err, "log_rotation_failed", "seek to start after rotation")
	}

	return file, nil
}

func shiftRotationBackups(parentDir *os.File, baseName string, backupCount int) error {
	parentFd := int(parentDir.Fd())
	oldestBackup := rotationBackupName(baseName, backupCount)

	if err := removeRegularRotationFile(parentDir, oldestBackup); err != nil {
		return err
	}

	for index := backupCount - 1; index >= 1; index-- {
		sourceName := rotationBackupName(baseName, index)

		var stat unix.Stat_t

		err := unix.Fstatat(parentFd, sourceName, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}

		if err != nil {
			return wrapErr(err, "log_rotation_failed", "inspect rotated log backup")
		}

		if stat.Mode&unix.S_IFMT != unix.S_IFREG {
			return wrapErr(ErrRotationPathNotRegular, "log_path_not_regular", "inspect rotated log backup")
		}

		destName := rotationBackupName(baseName, index+1)

		var destStat unix.Stat_t

		destErr := unix.Fstatat(parentFd, destName, &destStat, unix.AT_SYMLINK_NOFOLLOW)
		if destErr == nil && destStat.Mode&unix.S_IFMT != unix.S_IFREG {
			return wrapErr(ErrRotationPathNotRegular, "log_path_not_regular", "inspect destination log backup")
		}

		if err := unix.Renameat(parentFd, sourceName, parentFd, destName); err != nil {
			return wrapErr(err, "log_rotation_failed", "shift rotated log backup")
		}
	}

	return nil
}

func removeRegularRotationFile(parentDir *os.File, name string) error {
	parentFd := int(parentDir.Fd())

	var stat unix.Stat_t

	err := unix.Fstatat(parentFd, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}

	if err != nil {
		return wrapErr(err, "log_rotation_failed", "inspect log rotation path")
	}

	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return wrapErr(ErrRotationPathNotRegular, "log_path_not_regular", "inspect log rotation path")
	}

	if err := unix.Unlinkat(parentFd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return wrapErr(err, "log_rotation_failed", "remove old log rotation file")
	}

	return nil
}

func rotationBackupName(baseName string, index int) string {
	return fmt.Sprintf("%s.%d", baseName, index)
}

func rotationPath(path string, index int) string {
	return fmt.Sprintf("%s.%d", path, index)
}
