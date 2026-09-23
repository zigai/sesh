//go:build linux || darwin

package logging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sys/unix"
)

func TestRetainedPathsOrdersBackupsOldestFirst(t *testing.T) {
	t.Parallel()

	path := filepath.Join("state", "sesh.log")

	want := []string{
		filepath.Join("state", "sesh.log.3"),
		filepath.Join("state", "sesh.log.2"),
		filepath.Join("state", "sesh.log.1"),
		filepath.Join("state", "sesh.log"),
	}
	if got := RetainedPaths(path); !slices.Equal(got, want) {
		t.Fatalf("RetainedPaths() = %v, want %v", got, want)
	}
}

func TestOpenReaderRejectsSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	target := filepath.Join(root, "target.log")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	path := filepath.Join(root, "sesh.log")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	file, err := OpenReader(path)
	if file != nil {
		_ = file.Close()
		t.Fatalf("OpenReader() unexpectedly returned open file: %v", file)
	}

	if !errors.Is(err, ErrLogPathNotRegular) {
		t.Fatalf("OpenReader() error = %v, want %v", err, ErrLogPathNotRegular)
	}
}

func TestTruncateClearsActiveAndRemovesRotatedBackups(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "sesh.log")

	for _, retainedPath := range RetainedPaths(path) {
		if err := os.WriteFile(retainedPath, []byte("entry\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", retainedPath, err)
		}
	}

	if err := Truncate(t.Context(), path); err != nil {
		t.Fatalf("Truncate() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}

	if info.Size() != 0 {
		t.Fatalf("active log size = %d, want 0", info.Size())
	}

	for _, retainedPath := range RetainedPaths(path) {
		if retainedPath == path {
			continue
		}

		if _, err := os.Lstat(retainedPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rotated backup %s error = %v, want not exist", retainedPath, err)
		}
	}
}

func TestTruncateRejectsSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	target := filepath.Join(root, "target.log")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	path := filepath.Join(root, "sesh.log")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	if err := Truncate(t.Context(), path); !errors.Is(err, ErrLogPathNotRegular) {
		t.Fatalf("Truncate() error = %v, want %v", err, ErrLogPathNotRegular)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if string(data) != "private" {
		t.Fatalf("target data = %q, want private", string(data))
	}
}

func TestLineMatchesFilter(t *testing.T) {
	t.Parallel()

	lineInfo := []byte(`{"time":"2026-09-20T12:00:00Z","level":"info","component":"cli","message":"hello world"}` + "\n")
	lineWarn := []byte(`{"time":"2026-09-20T12:05:00Z","level":"warn","component":"tmux","message":"warning event"}` + "\n")
	lineError := []byte(`{"time":"2026-09-20T12:10:00Z","level":"error","component":"store","message":"error occurred"}` + "\n")

	t1, _ := time.Parse(time.RFC3339, "2026-09-20T12:02:00Z")
	t2, _ := time.Parse(time.RFC3339, "2026-09-20T12:08:00Z")

	levelFilter := FilterOptions{Level: zerolog.WarnLevel, HasLevel: true}
	if LineMatchesFilter(lineInfo, levelFilter) {
		t.Error("info should not match warn filter")
	}

	if !LineMatchesFilter(lineWarn, levelFilter) {
		t.Error("warn should match warn filter")
	}

	if !LineMatchesFilter(lineError, levelFilter) {
		t.Error("error should match warn filter")
	}

	compFilter := FilterOptions{Component: "tmux"}
	if LineMatchesFilter(lineInfo, compFilter) {
		t.Error("cli should not match tmux component filter")
	}

	if !LineMatchesFilter(lineWarn, compFilter) {
		t.Error("tmux should match tmux component filter")
	}

	timeFilter := FilterOptions{Since: t1, HasSince: true, Until: t2, HasUntil: true}
	if LineMatchesFilter(lineInfo, timeFilter) {
		t.Error("lineInfo before Since should not match")
	}

	if !LineMatchesFilter(lineWarn, timeFilter) {
		t.Error("lineWarn between Since and Until should match")
	}

	if LineMatchesFilter(lineError, timeFilter) {
		t.Error("lineError after Until should not match")
	}

	grepFilter := FilterOptions{Grep: regexp.MustCompile("warning")}
	if LineMatchesFilter(lineInfo, grepFilter) {
		t.Error("lineInfo should not match grep filter")
	}

	if !LineMatchesFilter(lineWarn, grepFilter) {
		t.Error("lineWarn should match grep filter")
	}
}

func TestStreamLogsBoundedLines(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bounded.log")

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	logger := sink.Logger()
	for i := range 15 {
		logger.Info().Int("seq", i).Msg("event")
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	var buf bytes.Buffer

	err = StreamLogs(context.Background(), path, ReadOptions{Lines: 5}, &buf)
	if err != nil {
		t.Fatalf("StreamLogs() error = %v", err)
	}

	records := decodeJSONLines(t, buf.Bytes())
	if len(records) != 5 {
		t.Fatalf("got %d records, want 5", len(records))
	}

	for i, rec := range records {
		expectedSeq := float64(10 + i)
		if rec["seq"] != expectedSeq {
			t.Errorf("record %d seq = %v, want %v", i, rec["seq"], expectedSeq)
		}
	}
}

func TestStreamLogsFiltering(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "filter.log")

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	logger := sink.Logger()
	logger.Info().Str("component", "cli").Msg("info msg")
	logger.Warn().Str("component", "tmux").Msg("warn msg")
	logger.Error().Str("component", "tmux").Msg("error msg")

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	var buf bytes.Buffer

	opts := ReadOptions{
		Filter: FilterOptions{
			Level:     zerolog.WarnLevel,
			HasLevel:  true,
			Component: "tmux",
		},
	}
	if err := StreamLogs(context.Background(), path, opts, &buf); err != nil {
		t.Fatalf("StreamLogs() error = %v", err)
	}

	records := decodeJSONLines(t, buf.Bytes())
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}

	if recordString(t, records[0], "message") != "warn msg" {
		t.Errorf("records[0] message = %q, want 'warn msg'", recordString(t, records[0], "message"))
	}

	if recordString(t, records[1], "message") != "error msg" {
		t.Errorf("records[1] message = %q, want 'error msg'", recordString(t, records[1], "message"))
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n, err := s.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("write sync buffer: %w", err)
	}

	return n, nil
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.buf.String()
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]byte(nil), s.buf.Bytes()...)
}

func TestStreamLogsFollowAppends(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "follow.log")

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	logger := sink.Logger()
	logger.Info().Msg("initial")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var buf syncBuffer

	done := make(chan error, 1)
	go func() {
		done <- StreamLogs(ctx, path, ReadOptions{Follow: true}, &buf)
	}()

	for range 50 {
		if strings.Contains(buf.String(), "initial") {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	logger.Info().Msg("followed-event")

	for range 50 {
		if strings.Contains(buf.String(), "followed-event") {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StreamLogs() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StreamLogs did not terminate after context cancel")
	}

	_ = sink.Close()

	if !strings.Contains(buf.String(), "followed-event") {
		t.Fatalf("buf does not contain followed-event: %q", buf.String())
	}
}

func TestStreamLogsFollowAcrossCopyTruncate(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "follow_rotate.log")
	sink := openTestRotatingSink(t, path, rotationPolicy{maxBytes: 256, backupCount: 2})
	logger := sink.Logger()
	logger.Info().Msg("first-event")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var buf syncBuffer

	done := make(chan error, 1)

	go func() {
		done <- StreamLogs(ctx, path, ReadOptions{Follow: true}, &buf)
	}()

	for range 50 {
		if strings.Contains(buf.String(), "first-event") {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	for i := range 5 {
		logger.Info().Int("seq", i).Str("pad", strings.Repeat("y", 60)).Msg("rotate")
	}

	logger.Info().Msg("post-rotation-event")

	for range 50 {
		if strings.Contains(buf.String(), "post-rotation-event") {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StreamLogs() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StreamLogs did not terminate after cancel")
	}

	_ = sink.Close()

	if !strings.Contains(buf.String(), "post-rotation-event") {
		t.Fatalf("buf does not contain post-rotation-event: %q", buf.String())
	}
}

func TestStreamLogsFollowAcrossRenameRotation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "follow_rename.log")
	if err := os.WriteFile(path, []byte(`{"time":"2026-09-20T12:00:00Z","level":"info","message":"before-rename"}`+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var buf syncBuffer

	done := make(chan error, 1)

	go func() {
		done <- StreamLogs(ctx, path, ReadOptions{Follow: true}, &buf)
	}()

	for range 50 {
		if strings.Contains(buf.String(), "before-rename") {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	renamedPath := path + ".1"
	if err := os.Rename(path, renamedPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"time":"2026-09-20T12:01:00Z","level":"info","message":"after-rename"}`+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	for range 50 {
		if strings.Contains(buf.String(), "after-rename") {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StreamLogs() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StreamLogs did not terminate after cancel")
	}

	if !strings.Contains(buf.String(), "after-rename") {
		t.Fatalf("buf does not contain after-rename: %q", buf.String())
	}
}

func TestReadRecords(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "records.log")

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	logger := sink.Logger()
	logger.Info().Msg("rec1")
	logger.Info().Msg("rec2")

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	records, err := ReadRecords(t.Context(), path, ReadOptions{})
	if err != nil {
		t.Fatalf("ReadRecords() error = %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}

	for _, rec := range records {
		if !bytes.HasSuffix(rec, []byte{'\n'}) {
			t.Errorf("record does not end with newline: %q", rec)
		}
	}
}

func TestTruncateRejectsSymlinkLockFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	logPath := filepath.Join(root, "active.log")
	targetLock := filepath.Join(root, "target.lock")
	symlinkLock := logPath + ".lock"

	if err := os.WriteFile(targetLock, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(targetLock, symlinkLock); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	err := Truncate(t.Context(), logPath)
	if err == nil || !errors.Is(err, ErrLogPathNotRegular) {
		t.Fatalf("Truncate() error = %v, want ErrLogPathNotRegular for symlink lock", err)
	}

	data, err := os.ReadFile(targetLock)
	if err != nil || string(data) != "protected" {
		t.Fatalf("target.lock was modified: %v, data = %q", err, string(data))
	}
}

func TestFollowDrainsBeforeRotation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "follow_drain.log")
	sink := openTestRotatingSink(t, path, rotationPolicy{maxBytes: 500, backupCount: 2})
	logger := sink.Logger()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var buf syncBuffer

	done := make(chan error, 1)

	go func() {
		done <- StreamLogs(ctx, path, ReadOptions{Follow: true}, &buf)
	}()

	logger.Info().Str("msg", "event-before-rotate").Msg("e1")

	for range 50 {
		if strings.Contains(buf.String(), "event-before-rotate") {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	logger.Info().Str("pad", strings.Repeat("z", 450)).Msg("trigger-rotation")

	for range 50 {
		if strings.Contains(buf.String(), "trigger-rotation") {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	logger.Info().Str("msg", "event-after-rotate").Msg("e2")

	for range 50 {
		if strings.Contains(buf.String(), "event-after-rotate") {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	<-done

	_ = sink.Close()

	output := buf.String()
	if !strings.Contains(output, "event-before-rotate") {
		t.Errorf("missing event-before-rotate in follow output: %s", output)
	}

	if !strings.Contains(output, "trigger-rotation") {
		t.Errorf("missing trigger-rotation in follow output: %s", output)
	}

	if !strings.Contains(output, "event-after-rotate") {
		t.Errorf("missing event-after-rotate in follow output: %s", output)
	}
}

func TestReaderRejectsOversizedLines(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "oversized.log")

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	chunk := bytes.Repeat([]byte("a"), 64*1024)
	for range 80 {
		if _, err := file.Write(chunk); err != nil {
			_ = file.Close()

			t.Fatal(err)
		}
	}

	_ = file.Close()

	_, err = ReadRecords(t.Context(), path, ReadOptions{})
	if err == nil || !errors.Is(err, ErrLogRecordTooLong) {
		t.Fatalf("ReadRecords() error = %v, want ErrLogRecordTooLong", err)
	}
}

func TestOpenReaderRejectsFIFO(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	fifoPath := filepath.Join(dir, "test.fifo")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("Mkfifo unavailable: %v", err)
	}

	file, err := OpenReader(fifoPath)
	if file != nil {
		_ = file.Close()

		t.Fatal("OpenReader(fifo) unexpectedly succeeded")
	}

	if !errors.Is(err, ErrLogPathNotRegular) {
		t.Fatalf("OpenReader(fifo) error = %v, want ErrLogPathNotRegular", err)
	}
}

func TestStreamLogsRejectsNegativeOrOversizedLineLimits(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	nonexistent := filepath.Join(t.TempDir(), "nonexistent.log")

	err := StreamLogs(t.Context(), nonexistent, ReadOptions{Lines: -1}, &buf)
	if !errors.Is(err, ErrLogReadLimit) {
		t.Fatalf("StreamLogs(-1) error = %v, want ErrLogReadLimit", err)
	}

	err = StreamLogs(t.Context(), nonexistent, ReadOptions{Lines: MaxRetainedLogRecords + 1}, &buf)
	if !errors.Is(err, ErrLogReadLimit) {
		t.Fatalf("StreamLogs(overMax) error = %v, want ErrLogReadLimit", err)
	}
}

func TestRingBufferTrackingAndReplacement(t *testing.T) {
	t.Parallel()

	ring := newRingBuffer(3, 100)

	line1 := []byte(strings.Repeat("a", 29) + "\n")
	line2 := []byte(strings.Repeat("b", 29) + "\n")
	line3 := []byte(strings.Repeat("c", 29) + "\n")
	line4 := []byte(strings.Repeat("d", 29) + "\n")

	testRingBufferFillAndDrain(t, ring, line1, line2, line3, line4)
	testRingBufferOverflowRejection(t, ring)
}

func testRingBufferFillAndDrain(t *testing.T, ring *ringBuffer, line1, line2, line3, line4 []byte) {
	t.Helper()

	assertRingAdd(t, ring, line1, 1, 30)
	assertRingAdd(t, ring, line2, 2, 60)
	assertRingAdd(t, ring, line3, 3, 90)
	assertRingAdd(t, ring, line4, 3, 90)

	drained := ring.drain()
	if len(drained) != 3 || string(drained[0]) != string(line2) || string(drained[1]) != string(line3) || string(drained[2]) != string(line4) {
		t.Fatalf("drain unexpected: %q", drained)
	}
}

func assertRingAdd(t *testing.T, ring *ringBuffer, line []byte, wantCount, wantBytes int) {
	t.Helper()

	if err := ring.add(line); err != nil {
		t.Fatalf("add(): %v", err)
	}

	if ring.count != wantCount || ring.curBytes != wantBytes {
		t.Fatalf("ring state: count=%d curBytes=%d, want count=%d curBytes=%d",
			ring.count, ring.curBytes, wantCount, wantBytes)
	}
}

func testRingBufferOverflowRejection(t *testing.T, ring *ringBuffer) {
	t.Helper()

	line5 := []byte(strings.Repeat("e", 49) + "\n")

	err := ring.add(line5)
	if !errors.Is(err, ErrLogReadLimit) {
		t.Fatalf("add(line5) error = %v, want ErrLogReadLimit", err)
	}

	if ring.count != 3 || ring.curBytes != 90 {
		t.Fatalf("ring after failed add: count=%d curBytes=%d", ring.count, ring.curBytes)
	}
}

func TestStreamLogsBoundedByteLimitExceeded(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "overflow.log")

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	recordSize := 3800 * 1024
	record := make([]byte, recordSize)
	copy(record, `{"level":"info","msg":"`)

	for i := 23; i < recordSize-3; i++ {
		record[i] = 'x'
	}

	copy(record[recordSize-3:], `"}`+"\n")

	for range 9 {
		if _, err := file.Write(record); err != nil {
			_ = file.Close()

			t.Fatalf("file.Write: %v", err)
		}
	}

	if err := file.Close(); err != nil {
		t.Fatalf("file.Close: %v", err)
	}

	var buf bytes.Buffer

	err = StreamLogs(t.Context(), path, ReadOptions{Lines: 10}, &buf)
	if !errors.Is(err, ErrLogReadLimit) {
		t.Fatalf("StreamLogs overflow error = %v, want ErrLogReadLimit", err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Size() != int64(recordSize*9) {
		t.Fatalf("file modified after overflow: info=%v err=%v", info, err)
	}
}

func TestReadRecordsAggregateByteOverflowRejection(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "records_overflow.log")

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	recordSize := 3800 * 1024
	record := make([]byte, recordSize)
	copy(record, `{"level":"info","msg":"`)

	for i := 23; i < recordSize-3; i++ {
		record[i] = 'x'
	}

	copy(record[recordSize-3:], `"}`+"\n")

	for range 9 {
		if _, err := file.Write(record); err != nil {
			_ = file.Close()

			t.Fatalf("file.Write: %v", err)
		}
	}

	if err := file.Close(); err != nil {
		t.Fatalf("file.Close: %v", err)
	}

	_, err = ReadRecords(t.Context(), path, ReadOptions{Lines: 0})
	if !errors.Is(err, ErrLogReadLimit) {
		t.Fatalf("ReadRecords overflow error = %v, want ErrLogReadLimit", err)
	}
}

type cancelOnWriteWriter struct {
	cancel context.CancelFunc
	count  int
}

func (w *cancelOnWriteWriter) Write(p []byte) (int, error) {
	w.count++
	if w.count == 1 && w.cancel != nil {
		w.cancel()
	}

	return len(p), nil
}

func TestStreamLogsCancellationViaWriterInjectingCancel(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cancel_writer.log")

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	for i := range 20 {
		if _, err := fmt.Fprintf(file, `{"level":"info","seq":%d}`+"\n", i); err != nil {
			_ = file.Close()

			t.Fatal(err)
		}
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	writer := &cancelOnWriteWriter{cancel: cancel}

	err = StreamLogs(ctx, path, ReadOptions{Lines: 0, Follow: true}, writer)
	if err != nil {
		t.Fatalf("StreamLogs clean cancellation error = %v, want nil", err)
	}

	if writer.count >= 20 {
		t.Fatalf("writer processed all %d records despite cancellation on first", writer.count)
	}
}

var errCustomWriterFailure = errors.New("custom writer failure")

type joinedErrorWriter struct {
	cancel context.CancelFunc
}

func (w *joinedErrorWriter) Write(p []byte) (int, error) {
	if w.cancel != nil {
		w.cancel()
	}

	return 0, errors.Join(context.Canceled, errCustomWriterFailure)
}

func TestStreamLogsJoinedWriterCancellationAndSentinelPreservesBoth(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "joined_error.log")
	if err := os.WriteFile(path, []byte(`{"level":"info","msg":"test"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	writer := &joinedErrorWriter{cancel: cancel}

	err := StreamLogs(ctx, path, ReadOptions{Follow: true}, writer)
	if err == nil {
		t.Fatalf("StreamLogs with joined error succeeded, want error")
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected error to contain context.Canceled, got %v", err)
	}

	if !errors.Is(err, errCustomWriterFailure) {
		t.Errorf("expected error to contain errCustomWriterFailure, got %v", err)
	}
}

func TestTruncateMissingParentAndFileNoEffects(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	nonexistentDir := filepath.Join(tempDir, "nonexistent", "nested", "active.log")
	if err := Truncate(t.Context(), nonexistentDir); err != nil {
		t.Fatalf("Truncate(missing parent) error = %v, want nil", err)
	}

	if _, err := os.Stat(filepath.Dir(nonexistentDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Truncate created parent directory: %v", err)
	}

	missingFile := filepath.Join(tempDir, "missing.log")
	if err := Truncate(t.Context(), missingFile); err != nil {
		t.Fatalf("Truncate(missing file) error = %v, want nil", err)
	}

	if _, err := os.Stat(missingFile); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Truncate created log file: %v", err)
	}

	if _, err := os.Stat(missingFile + ".lock"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Truncate created lock file: %v", err)
	}
}

var errTestCleanup = errors.New("cleanup failed")

func TestIsOnlyNotExistHelper(t *testing.T) {
	t.Parallel()

	if !isOnlyNotExist(fs.ErrNotExist) {
		t.Errorf("isOnlyNotExist(fs.ErrNotExist) = false, want true")
	}

	joined := errors.Join(fs.ErrNotExist, errTestCleanup)
	if isOnlyNotExist(joined) {
		t.Errorf("isOnlyNotExist(joined with cleanup) = true, want false")
	}

	if isOnlyNotExist(nil) {
		t.Errorf("isOnlyNotExist(nil) = true, want false")
	}

	if isOnlyNotExist(errTestCleanup) {
		t.Errorf("isOnlyNotExist(errTestCleanup) = true, want false")
	}
}

func TestStreamLogsBoundedDrainCancellation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bounded_drain_cancel.log")

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	for i := range 20 {
		if _, err := fmt.Fprintf(file, `{"level":"info","seq":%d}`+"\n", i); err != nil {
			_ = file.Close()

			t.Fatal(err)
		}
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	t.Run("bounded drain follow cancellation returns clean nil", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		writer := &cancelOnWriteWriter{cancel: cancel, count: 0}

		err := StreamLogs(ctx, path, ReadOptions{Lines: 10, Follow: true}, writer)
		if err != nil {
			t.Fatalf("StreamLogs clean cancellation error = %v, want nil", err)
		}

		if writer.count != 1 {
			t.Fatalf("writer processed %d lines, want exactly 1 line before cancellation", writer.count)
		}
	})

	t.Run("bounded drain non-follow cancellation preserves canceled error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		writer := &cancelOnWriteWriter{cancel: cancel, count: 0}

		err := StreamLogs(ctx, path, ReadOptions{Lines: 10, Follow: false}, writer)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StreamLogs non-follow cancellation error = %v, want context.Canceled", err)
		}

		if writer.count != 1 {
			t.Fatalf("writer processed %d lines, want exactly 1 line before cancellation", writer.count)
		}
	})
}

func TestTruncateLockCancelNoClear(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "active.log")

	initialContent := []byte("important log data\n")
	if err := os.WriteFile(logPath, initialContent, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("already canceled context does not clear active log", func(t *testing.T) {
		canceledCtx, cancel := context.WithCancel(t.Context())
		cancel()

		err := Truncate(canceledCtx, logPath)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Truncate canceled error = %v, want context.Canceled", err)
		}

		data, err := os.ReadFile(logPath)
		if err != nil || string(data) != string(initialContent) {
			t.Fatalf("active log was modified despite cancellation: data=%q err=%v", string(data), err)
		}
	})

	t.Run("lock wait timeout on held lock aborts promptly without clearing", func(t *testing.T) {
		lockPath := logPath + ".lock"

		holder, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = holder.Close() }()

		if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatalf("unix.Flock holder: %v", err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		err = Truncate(ctx, logPath)
		elapsed := time.Since(start)

		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("Truncate lock timeout error = %v, want context deadline", err)
		}

		if elapsed > 2*time.Second {
			t.Fatalf("Truncate blocked for %v on held lock instead of aborting promptly on context cancellation", elapsed)
		}

		data, err := os.ReadFile(logPath)
		if err != nil || string(data) != string(initialContent) {
			t.Fatalf("active log was cleared despite lock wait cancellation: data=%q", string(data))
		}
	})
}

func TestTruncateReturnsStatErrorsNotSwallowed(t *testing.T) {
	t.Parallel()

	if os.Getuid() == 0 {
		t.Skip("skipping permission test as root")
	}

	tempDir := t.TempDir()

	unreadableDir := filepath.Join(tempDir, "unreadable")
	if err := os.Mkdir(unreadableDir, 0o700); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(unreadableDir, "active.log")
	if err := os.WriteFile(logPath, []byte("data\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(unreadableDir, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(unreadableDir, 0o700) }()

	err := Truncate(t.Context(), logPath)
	if err == nil {
		t.Fatal("Truncate on unreadable directory unexpectedly succeeded, want permission error")
	}

	if !errors.Is(err, fs.ErrPermission) && !errors.Is(err, unix.EACCES) {
		t.Fatalf("Truncate error = %v, want permission/EACCES error", err)
	}
}
