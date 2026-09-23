//go:build linux || darwin

package logging

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sys/unix"
)

const (
	MaxRetainedLogRecords = 100000

	MaxRetainedLogBytes = 32 << 20

	maxLogRecordBytes         = 4 << 20
	initialRingBufferCapacity = 64
)

type FilterOptions struct {
	Level     zerolog.Level
	HasLevel  bool
	Component string
	Since     time.Time
	HasSince  bool
	Until     time.Time
	HasUntil  bool
	Grep      *regexp.Regexp
}

type ReadOptions struct {
	Lines  int
	Filter FilterOptions
	Follow bool
}

type ringBuffer struct {
	lines    [][]byte
	head     int
	count    int
	size     int
	curBytes int
	maxBytes int
}

type followState struct {
	file     *os.File
	reader   *bufio.Reader
	partial  []byte
	lastSize int64
}

type boundedBuffer struct {
	buf      bytes.Buffer
	maxBytes int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.maxBytes {
		return 0, wrapErr(ErrLogReadLimit, "log_read_limit", "retained records exceed aggregate byte limit")
	}

	n, err := b.buf.Write(p)
	if err != nil {
		return n, wrapErr(err, "log_write_failed", "write bounded buffer")
	}

	return n, nil
}

func (b *boundedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func RetainedPaths(path string) []string {
	paths := make([]string, 0, defaultLogBackupCount+1)
	for index := defaultLogBackupCount; index >= 1; index-- {
		paths = append(paths, rotationPath(path, index))
	}

	return append(paths, path)
}

func OpenReader(path string) (*os.File, error) {
	if path == "" {
		return nil, wrapErr(ErrLogPathEmpty, "log_path_empty", "validate log path")
	}

	parentFile, baseName, err := openPinnedParentDir(path, false)
	if err != nil {
		return nil, err
	}

	file, openErr := openReaderAt(int(parentFile.Fd()), baseName, path)
	if err := errors.Join(openErr, parentFile.Close()); err != nil {
		if file != nil {
			err = errors.Join(err, file.Close())
		}

		return nil, wrapErr(err, "log_open_failed", "open log reader")
	}

	return file, nil
}

func Truncate(ctx context.Context, path string) (retErr error) {
	if err := ctx.Err(); err != nil {
		return wrapErr(err, "log_truncate_failed", "truncate active log canceled")
	}

	if path == "" {
		return wrapErr(ErrLogPathEmpty, "log_path_empty", "validate log path")
	}

	parentFile, baseName, err := openPinnedParentDir(path, false)
	if err != nil {
		if isOnlyNotExist(err) {
			return nil
		}

		return err
	}

	defer func() {
		closeErr := parentFile.Close()
		if closeErr != nil {
			retErr = errors.Join(retErr, wrapErr(closeErr, "log_close_failed", "close pinned parent directory"))
		}
	}()

	return truncateInParent(ctx, parentFile, baseName)
}

func LineMatchesFilter(line []byte, filter FilterOptions) bool {
	if filter.isZero() {
		return true
	}

	if filter.Grep != nil && !filter.Grep.Match(line) {
		return false
	}

	if !filter.hasRecordFilter() {
		return true
	}

	trimmed := bytes.TrimSpace(line)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return false
	}

	var record map[string]any
	if err := json.Unmarshal(trimmed, &record); err != nil {
		return false
	}

	return matchesRecordFilters(record, filter)
}

func StreamLogs(ctx context.Context, path string, opts ReadOptions, out io.Writer) error {
	if opts.Lines < 0 || opts.Lines > MaxRetainedLogRecords {
		return wrapErr(ErrLogReadLimit, "log_read_limit", "validate line limit")
	}

	if err := ctx.Err(); err != nil {
		return wrapErr(err, "log_read_failed", "read initial log records")
	}

	paths := RetainedPaths(path)

	if opts.Lines <= 0 {
		return streamAllLogs(ctx, paths, path, opts, out)
	}

	return streamBoundedLogs(ctx, paths, path, opts, out)
}

func ReadRecords(ctx context.Context, path string, opts ReadOptions) ([][]byte, error) {
	var buf boundedBuffer

	buf.maxBytes = MaxRetainedLogBytes

	if err := StreamLogs(ctx, path, opts, &buf); err != nil {
		return nil, err
	}

	raw := buf.Bytes()
	if len(raw) == 0 {
		return nil, nil
	}

	var records [][]byte

	for line := range bytes.SplitSeq(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}

		records = append(records, append(append([]byte(nil), line...), '\n'))
	}

	return records, nil
}

func truncateInParent(ctx context.Context, parentFile *os.File, baseName string) (retErr error) {
	parentFd := int(parentFile.Fd())

	hasLog, hasLock, inspectErr := inspectLogFilesToClear(parentFd, baseName)
	if inspectErr != nil {
		return inspectErr
	}

	if !hasLog && !hasLock {
		return nil
	}

	if err := ctx.Err(); err != nil {
		return wrapErr(err, "log_truncate_failed", "truncate active log canceled")
	}

	lockFile, err := acquireProcessLockFileWithContext(ctx, parentFile, baseName)
	if err != nil {
		return err
	}

	defer func() {
		unlockErr := setLogFileLockContext(ctx, lockFile, unix.LOCK_UN)
		closeErr := lockFile.Close()
		retErr = errors.Join(retErr, unlockErr, closeErr)
	}()

	if err := ctx.Err(); err != nil {
		retErr = errors.Join(retErr, wrapErr(err, "log_truncate_failed", "truncate active log canceled"))

		return retErr
	}

	if err := performTruncateDeletes(parentFd, parentFile, baseName); err != nil {
		retErr = errors.Join(retErr, err)

		return retErr
	}

	return retErr
}

func performTruncateDeletes(parentFd int, parentFile *os.File, baseName string) error {
	for index := defaultLogBackupCount; index >= 1; index-- {
		backupName := rotationBackupName(baseName, index)
		if err := removeRegularRotationFile(parentFile, backupName); err != nil {
			return err
		}
	}

	if err := truncateActiveLogFile(parentFd, baseName); err != nil {
		return err
	}

	return cleanStaleRotationTemp(parentFile, baseName)
}

func isOnlyNotExist(err error) bool {
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return false
	}

	type multiUnwrap interface {
		Unwrap() []error
	}

	var mu multiUnwrap
	if errors.As(err, &mu) {
		for _, e := range mu.Unwrap() {
			if !isOnlyNotExist(e) {
				return false
			}
		}

		return true
	}

	type singleUnwrap interface {
		Unwrap() error
	}

	var su singleUnwrap
	if errors.As(err, &su) {
		unwrapped := su.Unwrap()
		if unwrapped != nil {
			return isOnlyNotExist(unwrapped)
		}
	}

	return errors.Is(err, fs.ErrNotExist)
}

func inspectLogFilesToClear(parentFd int, baseName string) (bool, bool, error) {
	var stat unix.Stat_t

	err := unix.Fstatat(parentFd, baseName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		hasLock, lockErr := checkLockFileStat(parentFd, baseName)
		if lockErr != nil {
			return false, false, lockErr
		}

		return true, hasLock, nil
	}

	if !errors.Is(err, unix.ENOENT) {
		return false, false, wrapErr(err, "log_truncate_failed", "inspect active log file")
	}

	for index := defaultLogBackupCount; index >= 1; index-- {
		backupName := rotationBackupName(baseName, index)

		backupErr := unix.Fstatat(parentFd, backupName, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if backupErr == nil {
			hasLock, lockErr := checkLockFileStat(parentFd, baseName)
			if lockErr != nil {
				return false, false, lockErr
			}

			return true, hasLock, nil
		}

		if !errors.Is(backupErr, unix.ENOENT) {
			return false, false, wrapErr(backupErr, "log_truncate_failed", "inspect rotation backup file")
		}
	}

	tempName := baseName + ".rotate.tmp"

	tempErr := unix.Fstatat(parentFd, tempName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if tempErr == nil {
		hasLock, lockErr := checkLockFileStat(parentFd, baseName)
		if lockErr != nil {
			return false, false, lockErr
		}

		return true, hasLock, nil
	}

	if !errors.Is(tempErr, unix.ENOENT) {
		return false, false, wrapErr(tempErr, "log_truncate_failed", "inspect rotation temporary file")
	}

	hasLock, lockErr := checkLockFileStat(parentFd, baseName)
	if lockErr != nil {
		return false, false, lockErr
	}

	return false, hasLock, nil
}

func checkLockFileStat(parentFd int, baseName string) (bool, error) {
	var stat unix.Stat_t

	lockName := baseName + ".lock"

	err := unix.Fstatat(parentFd, lockName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return true, nil
	}

	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}

	return false, wrapErr(err, "log_truncate_failed", "inspect log lock file")
}

func openReaderAt(parentFd int, baseName, path string) (*os.File, error) {
	fd, err := unix.Openat(parentFd, baseName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "open log file for reading")
		}

		return nil, wrapErr(err, "log_open_failed", "open log file for reading")
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, wrapErr(errors.Join(err, unix.Close(fd)), "log_open_failed", "inspect open log file")
	}

	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, wrapErr(errors.Join(ErrLogPathNotRegular, unix.Close(fd)), "log_path_not_regular", "inspect open log file")
	}

	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, unix.O_RDONLY); err != nil {
		return nil, wrapErr(errors.Join(err, unix.Close(fd)), "log_open_failed", "configure open log file flags")
	}

	return os.NewFile(uintptr(fd), path), nil
}

func truncateActiveLogFile(parentFd int, baseName string) error {
	fd, err := unix.Openat(parentFd, baseName, unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}

		if errors.Is(err, unix.ELOOP) {
			return wrapErr(ErrLogPathNotRegular, "log_path_not_regular", "open active log file for truncation")
		}

		return wrapErr(err, "log_truncate_failed", "open active log file for truncation")
	}

	var openStat unix.Stat_t
	if err := unix.Fstat(fd, &openStat); err != nil {
		return wrapErr(errors.Join(err, unix.Close(fd)), "log_truncate_failed", "inspect active log file for truncation")
	}

	if openStat.Mode&unix.S_IFMT != unix.S_IFREG {
		return wrapErr(errors.Join(ErrLogPathNotRegular, unix.Close(fd)), "log_path_not_regular", "inspect active log file for truncation")
	}

	truncErr := unix.Ftruncate(fd, 0)
	closeErr := unix.Close(fd)

	if err := errors.Join(truncErr, closeErr); err != nil {
		return wrapErr(err, "log_truncate_failed", "truncate active log file")
	}

	return nil
}

func (f FilterOptions) isZero() bool {
	return !f.HasLevel && f.Component == "" && !f.HasSince && !f.HasUntil && f.Grep == nil
}

func (f FilterOptions) hasRecordFilter() bool {
	return f.HasLevel || f.Component != "" || f.HasSince || f.HasUntil
}

func matchesRecordFilters(record map[string]any, filter FilterOptions) bool {
	if filter.HasLevel {
		levelStr, _ := record["level"].(string)

		recLevel, err := zerolog.ParseLevel(levelStr)
		if err != nil {
			recLevel = zerolog.InfoLevel
		}

		if recLevel < filter.Level {
			return false
		}
	}

	if filter.Component != "" {
		compStr, _ := record["component"].(string)
		if !strings.EqualFold(compStr, filter.Component) {
			return false
		}
	}

	return matchesTimeFilters(record, filter)
}

func matchesTimeFilters(record map[string]any, filter FilterOptions) bool {
	if !filter.HasSince && !filter.HasUntil {
		return true
	}

	timeStr, _ := record["time"].(string)

	recTime, err := parseRecordTime(timeStr)
	if err != nil {
		return true
	}

	if filter.HasSince && recTime.Before(filter.Since) {
		return false
	}

	if filter.HasUntil && recTime.After(filter.Until) {
		return false
	}

	return true
}

func parseRecordTime(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}

	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, wrapErr(err, "log_read_failed", "parse record time")
	}

	return t, nil
}

func newRingBuffer(size, maxBytes int) *ringBuffer {
	if size <= 0 {
		size = 50
	}

	initialCap := min(size, initialRingBufferCapacity)

	return &ringBuffer{
		lines:    make([][]byte, 0, initialCap),
		head:     0,
		count:    0,
		size:     size,
		curBytes: 0,
		maxBytes: maxBytes,
	}
}

func (r *ringBuffer) Write(line []byte) (int, error) {
	if err := r.add(line); err != nil {
		return 0, err
	}

	return len(line), nil
}

func (r *ringBuffer) add(line []byte) error {
	newLineLen := len(line)

	if len(r.lines) < r.size {
		if r.curBytes+newLineLen > r.maxBytes {
			return wrapErr(ErrLogReadLimit, "log_read_limit", "retained records exceed aggregate byte limit")
		}

		lineCopy := append([]byte(nil), line...)
		r.lines = append(r.lines, lineCopy)
		r.count++
		r.head = len(r.lines) % r.size
		r.curBytes += newLineLen

		return nil
	}

	oldLineLen := len(r.lines[r.head])

	newBytes := r.curBytes - oldLineLen + newLineLen
	if newBytes > r.maxBytes {
		return wrapErr(ErrLogReadLimit, "log_read_limit", "retained records exceed aggregate byte limit")
	}

	lineCopy := append([]byte(nil), line...)
	r.lines[r.head] = lineCopy
	r.head = (r.head + 1) % r.size
	r.curBytes = newBytes

	return nil
}

func (r *ringBuffer) drain() [][]byte {
	if r.count == 0 {
		return nil
	}

	out := make([][]byte, r.count)
	if len(r.lines) < r.size {
		copy(out, r.lines)
		return out
	}

	start := r.head
	for i := range r.count {
		out[i] = r.lines[(start+i)%r.size]
	}

	return out
}

func streamAllLogs(ctx context.Context, paths []string, activePath string, opts ReadOptions, out io.Writer) error {
	state, err := readInitialLogs(ctx, paths, opts.Filter, out)
	if err != nil {
		if opts.Follow && isCleanCancellation(err, ctx) {
			return nil
		}

		return err
	}

	return finishInitialLogs(ctx, state, activePath, opts, out)
}

func streamBoundedLogs(ctx context.Context, paths []string, activePath string, opts ReadOptions, out io.Writer) error {
	ring := newRingBuffer(opts.Lines, MaxRetainedLogBytes)

	state, err := readInitialLogs(ctx, paths, opts.Filter, ring)
	if err != nil {
		if opts.Follow && isCleanCancellation(err, ctx) {
			return nil
		}

		return err
	}

	if err := drainRingRecords(ctx, ring.drain(), opts, out, state); err != nil {
		return err
	}

	return finishInitialLogs(ctx, state, activePath, opts, out)
}

func drainRingRecords(ctx context.Context, lines [][]byte, opts ReadOptions, out io.Writer, state *followState) error {
	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return handleDrainError(err, ctx, opts, state)
		}

		if err := writeLogRecord(out, line); err != nil {
			return handleDrainError(err, ctx, opts, state)
		}

		if err := ctx.Err(); err != nil {
			return handleDrainError(err, ctx, opts, state)
		}
	}

	return nil
}

func handleDrainError(err error, ctx context.Context, opts ReadOptions, state *followState) error {
	closeErr := state.close()
	if opts.Follow && isCleanCancellation(err, ctx) {
		return closeErr
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		err = wrapErr(err, "log_read_failed", "stream bounded records")
	}

	return errors.Join(err, closeErr)
}

func readInitialLogs(ctx context.Context, paths []string, filter FilterOptions, out io.Writer) (*followState, error) {
	state := &followState{file: nil, reader: nil, partial: nil, lastSize: 0}

	for index, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, wrapErr(err, "log_read_failed", "read initial log records")
		}

		file, err := OpenReader(path)
		if isOnlyNotExist(err) {
			continue
		}

		if err != nil {
			return nil, err
		}

		state.file, state.reader = file, bufio.NewReader(file)
		if err := state.drain(ctx, filter, out); err != nil {
			return nil, errors.Join(err, state.close())
		}

		if index == len(paths)-1 {
			return state, nil
		}

		if err := state.close(); err != nil {
			return nil, err
		}
	}

	return state, nil
}

func finishInitialLogs(ctx context.Context, state *followState, path string, opts ReadOptions, out io.Writer) error {
	if !opts.Follow {
		return state.close()
	}

	followErr := followLogFile(ctx, path, opts.Filter, out, state)
	closeErr := state.close()

	if followErr == nil {
		return closeErr
	}

	return errors.Join(followErr, closeErr)
}

func followLogFile(ctx context.Context, path string, filter FilterOptions, out io.Writer, state *followState) error {
	const watchInterval = 50 * time.Millisecond

	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := ctx.Err(); err != nil {
				return nil
			}

			if err := state.step(ctx, path, filter, out); err != nil {
				if isCleanCancellation(err, ctx) {
					return nil
				}

				return err
			}
		}
	}
}

func isCleanCancellation(err error, ctx context.Context) bool {
	if err == nil {
		return true
	}

	if ctx.Err() == nil {
		return false
	}

	return isPureCancellationError(err)
}

func isPureCancellationError(err error) bool {
	if err == nil {
		return false
	}

	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	type multiUnwrap interface {
		Unwrap() []error
	}

	var mu multiUnwrap
	if errors.As(err, &mu) {
		return allPureCancellations(mu.Unwrap())
	}

	type singleUnwrap interface {
		Unwrap() error
	}

	var su singleUnwrap
	if errors.As(err, &su) {
		unwrapped := su.Unwrap()
		if unwrapped != nil {
			return isPureCancellationError(unwrapped)
		}
	}

	return true
}

func allPureCancellations(errs []error) bool {
	for _, e := range errs {
		if !isPureCancellationError(e) {
			return false
		}
	}

	return true
}

func (s *followState) close() error {
	var err error
	if s.file != nil {
		err = s.file.Close()
	}

	s.file, s.reader, s.partial, s.lastSize = nil, nil, nil, 0

	if err != nil {
		return wrapErr(err, "log_read_failed", "close log reader")
	}

	return nil
}

func (s *followState) drain(ctx context.Context, filter FilterOptions, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return wrapErr(err, "log_read_failed", "inspect log reader")
	}

	info, err := s.file.Stat()
	if err != nil {
		return wrapErr(err, "log_read_failed", "inspect log reader")
	}

	if info.Size() < s.lastSize {
		if _, err := s.file.Seek(0, io.SeekStart); err != nil {
			return wrapErr(err, "log_read_failed", "rewind truncated log reader")
		}

		s.reader.Reset(s.file)
		s.partial = nil
	}

	partial, err := drainFollowLines(ctx, s.reader, filter, out, s.partial)
	if err != nil {
		return err
	}

	s.partial = partial

	position, err := s.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return wrapErr(err, "log_read_failed", "inspect log reader position")
	}

	s.lastSize = position

	return nil
}

func (s *followState) step(ctx context.Context, path string, filter FilterOptions, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return wrapErr(err, "log_read_failed", "step follow reader")
	}

	if s.file != nil {
		if err := s.drain(ctx, filter, out); err != nil {
			return err
		}
	}

	if err := ctx.Err(); err != nil {
		return wrapErr(err, "log_read_failed", "step follow reader")
	}

	if err := s.refresh(path); err != nil {
		return err
	}

	if s.file == nil {
		return nil
	}

	return s.drain(ctx, filter, out)
}

func (s *followState) refresh(path string) error {
	current, err := OpenReader(path)
	if isOnlyNotExist(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if s.file == nil {
		s.file, s.reader = current, bufio.NewReader(current)
		return nil
	}

	previousInfo, previousErr := s.file.Stat()

	currentInfo, currentErr := current.Stat()
	if err := errors.Join(previousErr, currentErr); err != nil {
		return wrapErr(errors.Join(err, current.Close()), "log_read_failed", "inspect rotated log reader")
	}

	if os.SameFile(previousInfo, currentInfo) {
		if err := current.Close(); err != nil {
			return wrapErr(err, "log_read_failed", "close unchanged log reader")
		}

		return nil
	}

	closeErr := s.close()
	s.file, s.reader = current, bufio.NewReader(current)

	return closeErr
}

func writeLogRecord(out io.Writer, line []byte) error {
	n, err := out.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}

	if err != nil {
		return wrapErr(err, "log_read_failed", "write log output")
	}

	return nil
}

func drainFollowLines(ctx context.Context, reader *bufio.Reader, filter FilterOptions, out io.Writer, partial []byte) ([]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return partial, wrapErr(err, "log_read_failed", "read log stream")
		}

		line, rem, done, err := readNextCompleteLine(reader, partial)
		partial = rem

		if err != nil {
			return nil, wrapErr(err, "log_read_failed", "read log stream")
		}

		if len(line) > 0 && LineMatchesFilter(line, filter) {
			if err := ctx.Err(); err != nil {
				return partial, wrapErr(err, "log_read_failed", "read log stream")
			}

			if err := writeLogRecord(out, line); err != nil {
				return nil, err
			}
		}

		if err := ctx.Err(); err != nil {
			return partial, wrapErr(err, "log_read_failed", "read log stream")
		}

		if done {
			return partial, nil
		}
	}
}

func readNextCompleteLine(reader *bufio.Reader, partial []byte) ([]byte, []byte, bool, error) {
	frag, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		err = nil
	}

	if len(frag) == 0 {
		if errors.Is(err, io.EOF) {
			return nil, partial, true, nil
		}

		if err != nil {
			return nil, nil, false, wrapErr(err, "log_read_failed", "read line bytes")
		}

		return nil, partial, false, nil
	}

	if len(frag) > maxLogRecordBytes-len(partial) {
		return nil, nil, false, wrapErr(ErrLogRecordTooLong, "log_read_failed", "log record exceeds maximum line length")
	}

	partial = append(partial, frag...)

	if !bytes.HasSuffix(partial, []byte{'\n'}) {
		if errors.Is(err, io.EOF) {
			return nil, partial, true, nil
		}

		if err != nil {
			return nil, nil, false, wrapErr(err, "log_read_failed", "read line bytes")
		}

		return nil, partial, false, nil
	}

	line := partial

	return line, nil, false, nil
}
