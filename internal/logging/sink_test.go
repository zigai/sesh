//go:build linux || darwin

package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"golang.org/x/sys/unix"
)

var (
	errTestSinkClose = errors.New("close failure")
	errTestSinkWrite = errors.New("write failure")
)

func TestSinkLoggerFiltersBelowInfoAndWritesJSONLines(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sesh.log")

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	logger := sink.Logger()
	logger.Trace().Msg("trace omitted")
	logger.Debug().Msg("debug omitted")
	logger.Info().Int("attempt", 1).Msg("info kept")
	logger.Warn().Msg("warn kept")
	logger.Error().Msg("error kept")

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	data, records := readJSONLines(t, path)
	if got, want := bytes.Count(data, []byte{'\n'}), 3; got != want {
		t.Fatalf("newline count = %d, want %d", got, want)
	}

	if !bytes.HasSuffix(data, []byte{'\n'}) {
		t.Fatal("log does not end with a newline")
	}

	wantLevels := []string{"info", "warn", "error"}
	wantMessages := []string{"info kept", "warn kept", "error kept"}

	if len(records) != len(wantLevels) {
		t.Fatalf("record count = %d, want %d", len(records), len(wantLevels))
	}

	for index, record := range records {
		if got := recordString(t, record, "level"); got != wantLevels[index] {
			t.Errorf("record %d level = %q, want %q", index, got, wantLevels[index])
		}

		if got := recordString(t, record, "message"); got != wantMessages[index] {
			t.Errorf("record %d message = %q, want %q", index, got, wantMessages[index])
		}

		timestamp := recordString(t, record, "time")
		if !strings.HasSuffix(timestamp, "Z") {
			t.Errorf("record %d time = %q, want UTC Z suffix", index, timestamp)
		}

		if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
			t.Errorf("record %d time %q is not RFC3339Nano: %v", index, timestamp, err)
		}
	}

	if got := records[0]["attempt"]; got != float64(1) {
		t.Errorf("structured attempt field = %#v, want 1", got)
	}
}

func TestSinkSetLevel(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sesh.log")

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	sink.SetLevel(zerolog.DebugLevel)
	logger := sink.Logger()
	logger.Debug().Msg("debug visible")
	logger.Info().Msg("info visible")

	sink.SetLevel(zerolog.WarnLevel)
	logger = sink.Logger()
	logger.Info().Msg("info suppressed")
	logger.Warn().Msg("warn visible")

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, records := readJSONLines(t, path)
	if len(records) != 3 {
		t.Fatalf("record count = %d, want 3", len(records))
	}

	wantLevels := []string{"debug", "info", "warn"}
	wantMessages := []string{"debug visible", "info visible", "warn visible"}

	for index, record := range records {
		if got := recordString(t, record, "level"); got != wantLevels[index] {
			t.Errorf("record %d level = %q, want %q", index, got, wantLevels[index])
		}

		if got := recordString(t, record, "message"); got != wantMessages[index] {
			t.Errorf("record %d message = %q, want %q", index, got, wantMessages[index])
		}
	}
}

func TestSinkMakesOneUnderlyingWritePerEvent(t *testing.T) {
	t.Parallel()

	writer := &stubWriteCloser{}
	sink := newSink(writer)
	logger := sink.Logger()

	logger.Info().Msg("first")
	logger.Info().Msg("second")

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if got, want := writer.writeCalls, 2; got != want {
		t.Fatalf("underlying Write calls = %d, want %d", got, want)
	}

	assertJSONLines(t, writer.data.Bytes(), 2)
}

func TestOpenAppendsAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sesh.log")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}

	firstLogger := first.Logger()
	firstLogger.Info().Msg("first")

	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}

	secondLogger := second.Logger()
	secondLogger.Info().Msg("second")

	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	_, records := readJSONLines(t, path)
	if got, want := len(records), 2; got != want {
		t.Fatalf("record count = %d, want %d", got, want)
	}

	if got := recordString(t, records[0], "message"); got != "first" {
		t.Errorf("first message = %q, want %q", got, "first")
	}

	if got := recordString(t, records[1], "message"); got != "second" {
		t.Errorf("second message = %q, want %q", got, "second")
	}
}

func TestOpenRecoversIncompleteLogTail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tail string
	}{
		{
			name: "malformed record",
			tail: `{"level":"error","error_co{"level":"info","message":"next"}` + "\n",
		},
		{
			name: "missing newline",
			tail: `{"level":"info","message":"partial"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sesh.log")
			data := `{"level":"info","message":"before"}` + "\n" + tt.tail

			if err := os.WriteFile(path, []byte(data), logFileMode); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			sink, err := Open(path)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}

			logger := sink.Logger()
			logger.Info().Msg("after recovery")

			if err := sink.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}

			_, records := readJSONLines(t, path)
			if got, want := len(records), 2; got != want {
				t.Fatalf("record count = %d, want %d", got, want)
			}

			if got := recordString(t, records[0], "message"); got != "before" {
				t.Errorf("first message = %q, want %q", got, "before")
			}

			if got := recordString(t, records[1], "message"); got != "after recovery" {
				t.Errorf("second message = %q, want %q", got, "after recovery")
			}
		})
	}
}

func TestOpenRejectsForeignFileAndPreservesContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "multi-line plain text",
			content: "line 1\nline 2\nline 3\n",
		},
		{
			name:    "single-line non-json",
			content: "this is not json\n",
		},
		{
			name:    "non-json without newline",
			content: "hello world without newline",
		},
		{
			name:    "json array instead of object",
			content: "[1, 2, 3]\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "foreign.log")
			if err := os.WriteFile(path, []byte(tt.content), logFileMode); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			sink, err := Open(path)
			if sink != nil {
				_ = sink.Close()

				t.Fatal("Open() unexpectedly succeeded for foreign file")
			}

			if !errors.Is(err, ErrLogForeign) {
				t.Fatalf("Open() error = %v, want %v", err, ErrLogForeign)
			}

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("ReadFile() error = %v", readErr)
			}

			if string(data) != tt.content {
				t.Fatalf("file content = %q, want preserved %q", string(data), tt.content)
			}
		})
	}
}

func TestSinkSerializesConcurrentEvents(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sesh.log")

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	logger := sink.Logger()

	const (
		workerCount  = 24
		eventsPerRun = 25
	)

	var waitGroup sync.WaitGroup
	for worker := range workerCount {
		waitGroup.Add(1)
		go func(w int) {
			defer waitGroup.Done()

			for event := range eventsPerRun {
				logger.Info().
					Int("worker", w).
					Int("event", event).
					Msg("concurrent event")
			}
		}(worker)
	}

	waitGroup.Wait()

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, records := readJSONLines(t, path)
	if got, want := len(records), workerCount*eventsPerRun; got != want {
		t.Fatalf("record count = %d, want %d", got, want)
	}
}

func TestOpenCreatesPrivateParentAndFile(t *testing.T) {
	root := t.TempDir()
	firstParent := filepath.Join(root, "private")
	parent := filepath.Join(firstParent, "nested")
	path := filepath.Join(parent, "sesh.log")

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	assertMode(t, firstParent, 0o700)
	assertMode(t, parent, 0o700)
	assertMode(t, path, 0o600)
	assertMode(t, path+".lock", 0o600)
}

func TestOpenEnforcesPrivateModeOnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sesh.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	assertMode(t, path, 0o600)
}

func TestOpenRejectsEmptyDirectoryAndNonRegularPaths(t *testing.T) {
	if sink, err := Open(""); !errors.Is(err, ErrLogPathEmpty) || sink != nil {
		t.Fatalf("Open(empty) = (%v, %v), want nil sink and empty-path error", sink, err)
	}

	directory := t.TempDir()
	if sink, err := Open(directory); !errors.Is(err, ErrLogPathNotRegular) || sink != nil {
		t.Fatalf("Open(directory) = (%v, %v), want nil sink and non-regular-path error", sink, err)
	}

	info, err := os.Lstat(os.DevNull)
	if err != nil {
		t.Skipf("Lstat(%q) error = %v", os.DevNull, err)
	}

	if info.Mode().IsRegular() {
		t.Skipf("%q is unexpectedly a regular file", os.DevNull)
	}

	if sink, err := Open(os.DevNull); !errors.Is(err, ErrLogPathNotRegular) || sink != nil {
		t.Fatalf("Open(non-regular) = (%v, %v), want nil sink and non-regular-path error", sink, err)
	}
}

func TestOpenRejectsSymlinkWithoutChangingTargetMode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.log")
	link := filepath.Join(root, "link.log")

	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if sink, err := Open(link); !errors.Is(err, ErrLogPathNotRegular) || sink != nil {
		t.Fatalf("Open(symlink) = (%v, %v), want nil sink and non-regular-path error", sink, err)
	}

	assertMode(t, target, 0o640)
}

func TestOpenRejectsSymlinkLockFileWithoutModifyingTarget(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "test.log")
	targetLock := filepath.Join(root, "target.lock")
	symlinkLock := logPath + ".lock"

	if err := os.WriteFile(targetLock, []byte("protected"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := os.Chmod(targetLock, 0o640); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	if err := os.Symlink(targetLock, symlinkLock); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	sink, err := Open(logPath)
	if sink != nil {
		_ = sink.Close()

		t.Fatal("Open() unexpectedly succeeded when lock file is a symlink")
	}

	if !errors.Is(err, ErrLogPathNotRegular) {
		t.Fatalf("Open() error = %v, want %v", err, ErrLogPathNotRegular)
	}

	assertMode(t, targetLock, 0o640)
}

func TestOpenExistingRejectsChangedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "sesh.log")
	movedPath := filepath.Join(root, "original.log")

	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("write original file: %v", err)
	}

	parentFile, baseName, err := openPinnedParentDir(path, false)
	if err != nil {
		t.Fatalf("openPinnedParentDir() error = %v", err)
	}
	defer func() {
		_ = parentFile.Close()
	}()

	var origStat unix.Stat_t
	if err := unix.Fstatat(int(parentFile.Fd()), baseName, &origStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatalf("Fstatat() error = %v", err)
	}

	if err := os.Rename(path, movedPath); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement file: %v", err)
	}

	file, err := openExisting(parentFile, path, baseName, &origStat)
	if !errors.Is(err, ErrLogPathChanged) || file != nil {
		t.Fatalf("openExisting(changed path) = (%v, %v), want nil file and changed-path error", file, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove replacement after rejected open: %v", err)
	}
}

func TestSinkCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	writer := &stubWriteCloser{closeErr: errTestSinkClose}
	sink := newSink(writer)

	firstErr := sink.Close()
	if !errors.Is(firstErr, errTestSinkClose) {
		t.Fatalf("first Close() error = %v, want %v", firstErr, errTestSinkClose)
	}

	secondErr := sink.Close()
	if !errors.Is(secondErr, firstErr) {
		t.Fatalf("second Close() error = %v, want identical retained error %v", secondErr, firstErr)
	}

	if got, want := writer.closeCalls, 1; got != want {
		t.Fatalf("underlying Close calls = %d, want %d", got, want)
	}
}

func TestSinkRollsBackPartialFileWrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		writeErr     error
		wantCloseErr error
	}{
		{
			name:         "write error",
			writeErr:     errTestSinkWrite,
			wantCloseErr: errTestSinkWrite,
		},
		{
			name:         "short write",
			wantCloseErr: io.ErrShortWrite,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sesh.log")

			file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_APPEND|os.O_RDWR, logFileMode)
			if err != nil {
				t.Fatalf("OpenFile() error = %v", err)
			}

			writer := &partialWriteFile{
				File:     file,
				writeErr: tt.writeErr,
			}
			sink := newSink(writer)

			logger := sink.Logger()
			logger.Info().Msg("discarded partial event")
			logger.Info().Msg("kept event")

			if err := sink.Close(); !errors.Is(err, tt.wantCloseErr) {
				t.Fatalf("Close() error = %v, want retained %v", err, tt.wantCloseErr)
			}

			_, records := readJSONLines(t, path)
			if got, want := len(records), 1; got != want {
				t.Fatalf("record count = %d, want %d", got, want)
			}

			if got := recordString(t, records[0], "message"); got != "kept event" {
				t.Errorf("message = %q, want %q", got, "kept event")
			}
		})
	}
}

func TestSinkRetainsWriteFailuresWithoutZerologStderrFallback(t *testing.T) {
	tests := []struct {
		name         string
		write        func([]byte) (int, error)
		wantWriteErr error
		wantCloseErr error
	}{
		{
			name: "write error",
			write: func(_ []byte) (int, error) {
				return 0, errTestSinkWrite
			},
			wantWriteErr: errTestSinkWrite,
			wantCloseErr: errTestSinkClose,
		},
		{
			name: "short write",
			write: func(p []byte) (int, error) {
				return len(p) - 1, nil
			},
			wantWriteErr: io.ErrShortWrite,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &stubWriteCloser{
				writeFunc: tt.write,
				closeErr:  tt.wantCloseErr,
			}
			sink := newSink(writer)

			stderr := captureStderr(t, func() {
				logger := sink.Logger()
				logger.Info().Msg("retained failure")
			})
			if len(stderr) != 0 {
				t.Fatalf("zerolog wrote fallback stderr: %q", stderr)
			}

			firstErr := sink.Close()
			if !errors.Is(firstErr, tt.wantWriteErr) {
				t.Errorf("Close() error = %v, want retained %v", firstErr, tt.wantWriteErr)
			}

			if tt.wantCloseErr != nil && !errors.Is(firstErr, tt.wantCloseErr) {
				t.Errorf("Close() error = %v, want joined %v", firstErr, tt.wantCloseErr)
			}

			if secondErr := sink.Close(); !errors.Is(secondErr, firstErr) {
				t.Errorf("second Close() error = %v, want identical retained error %v", secondErr, firstErr)
			}

			if got, want := writer.writeCalls, 1; got != want {
				t.Errorf("underlying Write calls = %d, want %d", got, want)
			}
		})
	}
}

func TestSinkBoundaryErrorsUseOops(t *testing.T) {
	t.Parallel()

	_, err := Open("")
	if err == nil {
		t.Fatal("Open(empty) succeeded, want error")
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("Open error %v is not an oops error", err)
	}

	if oopsErr.Domain() != "logging" {
		t.Errorf("domain = %q, want 'logging'", oopsErr.Domain())
	}

	if oopsErr.Code() != "log_path_empty" {
		t.Errorf("code = %q, want 'log_path_empty'", oopsErr.Code())
	}
}

type stubWriteCloser struct {
	data       bytes.Buffer
	writeFunc  func([]byte) (int, error)
	closeErr   error
	writeCalls int
	closeCalls int
}

func (w *stubWriteCloser) Write(p []byte) (int, error) {
	w.writeCalls++
	if w.writeFunc != nil {
		return w.writeFunc(p)
	}

	n, err := w.data.Write(p)
	if err != nil {
		return n, fmt.Errorf("write buffered test log: %w", err)
	}

	return n, nil
}

func (w *stubWriteCloser) Close() error {
	w.closeCalls++
	return w.closeErr
}

type partialWriteFile struct {
	*os.File

	writeErr error
	written  bool
}

func (w *partialWriteFile) Write(p []byte) (int, error) {
	if w.written {
		n, err := w.File.Write(p)
		if err != nil {
			return n, fmt.Errorf("write complete test event: %w", err)
		}

		return n, nil
	}

	w.written = true

	n, err := w.File.Write(p[:len(p)/2])
	if err != nil {
		return n, fmt.Errorf("write partial test event: %w", err)
	}

	return n, w.writeErr
}

func readJSONLines(t *testing.T, path string) ([]byte, []map[string]any) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	return data, decodeJSONLines(t, data)
}

func assertJSONLines(t *testing.T, data []byte, want int) {
	t.Helper()

	records := decodeJSONLines(t, data)
	if got := len(records); got != want {
		t.Fatalf("record count = %d, want %d", got, want)
	}
}

func decodeJSONLines(t *testing.T, data []byte) []map[string]any {
	t.Helper()

	if len(data) == 0 {
		return nil
	}

	if !bytes.HasSuffix(data, []byte{'\n'}) {
		t.Fatalf("JSONL data does not end with newline: %q", data)
	}

	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})

	records := make([]map[string]any, 0, len(lines))
	for index, line := range lines {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("line %d is invalid JSON: %v; line = %q", index, err, line)
		}

		records = append(records, record)
	}

	return records
}

func recordString(t *testing.T, record map[string]any, key string) string {
	t.Helper()

	value, ok := record[key].(string)
	if !ok {
		t.Fatalf("record field %q = %#v, want string", key, record[key])
	}

	return value
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}

	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %q = %#o, want %#o", path, got, want)
	}
}

func captureStderr(t *testing.T, run func()) []byte {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}

	original := os.Stderr
	os.Stderr = writer

	run()

	os.Stderr = original

	if err := writer.Close(); err != nil {
		t.Fatalf("close stderr capture writer: %v", err)
	}

	captured, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}

	if err := reader.Close(); err != nil {
		t.Fatalf("close stderr capture reader: %v", err)
	}

	return captured
}

func TestSinkWriteRejectsInvalidJSONAndRetainsFailure(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "invalid_write.log")

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	invalidPayloads := [][]byte{
		[]byte("not a json object\n"),
		[]byte("{\"incomplete\":\n"),
		[]byte("[1, 2, 3]\n"),
		[]byte(""),
		[]byte("\n"),
	}

	for _, payload := range invalidPayloads {
		n, writeErr := sink.Write(payload)
		if writeErr == nil || !errors.Is(writeErr, ErrLogInvalidRecord) {
			t.Errorf("Write(%q) = (%d, %v), want ErrLogInvalidRecord", payload, n, writeErr)
		}
	}

	closeErr := sink.Close()
	if !errors.Is(closeErr, ErrLogInvalidRecord) {
		t.Fatalf("Close() error = %v, want retained ErrLogInvalidRecord", closeErr)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if len(data) != 0 {
		t.Fatalf("active log has %d bytes, want 0 bytes after rejected writes", len(data))
	}
}

func TestSinkWriteRejectsClosedSinkAndRetainsFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "closed_write.log")

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	logger := sink.Logger()

	invalidEvent := []byte("not valid json\n")

	n, writeErr := sink.Write(invalidEvent)
	if writeErr == nil || !errors.Is(writeErr, ErrLogInvalidRecord) {
		t.Fatalf("Write() invalid after close = (%d, %v), want ErrLogInvalidRecord", n, writeErr)
	}

	stderr := captureStderr(t, func() {
		logger.Info().Msg("stale-logger-event")
	})
	if len(stderr) != 0 {
		t.Fatalf("closed logger leaked to stderr: %q, want 0 bytes", stderr)
	}

	secondCloseErr := sink.Close()
	if !errors.Is(secondCloseErr, os.ErrClosed) {
		t.Fatalf("second Close() error = %v, want os.ErrClosed", secondCloseErr)
	}

	if !errors.Is(secondCloseErr, ErrLogInvalidRecord) {
		t.Fatalf("second Close() error = %v, want ErrLogInvalidRecord", secondCloseErr)
	}
}

func TestZerologContextEventsRetainFailuresWithoutStderrLeak(t *testing.T) {
	writer := &stubWriteCloser{
		writeFunc: func(_ []byte) (int, error) {
			return 0, errTestSinkWrite
		},
		closeErr: errTestSinkClose,
	}
	sink := newSink(writer)

	logger := sink.Logger()
	ctx := logger.WithContext(t.Context())

	stderr := captureStderr(t, func() {
		zerolog.Ctx(ctx).Info().Str("field", "value").Msg("context-event-failed")
	})

	if len(stderr) != 0 {
		t.Fatalf("zerolog emitted stderr: %q, want 0 bytes", stderr)
	}

	closeErr := sink.Close()
	if !errors.Is(closeErr, errTestSinkWrite) {
		t.Errorf("Close() error = %v, want %v", closeErr, errTestSinkWrite)
	}

	if !errors.Is(closeErr, errTestSinkClose) {
		t.Errorf("Close() error = %v, want %v", closeErr, errTestSinkClose)
	}
}

func TestBoundedLockTimeoutOnStalledOwner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "timeout.lock")

	ownerFile, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = ownerFile.Close()
	}()

	if err := setLogFileLock(ownerFile, unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	waiterFile, err := os.OpenFile(lockPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = waiterFile.Close()
	}()

	start := time.Now()
	lockErr := setLogFileLock(waiterFile, unix.LOCK_EX)
	elapsed := time.Since(start)

	if lockErr == nil || !errors.Is(lockErr, ErrLockTimeout) {
		t.Fatalf("setLogFileLock() error = %v, want ErrLockTimeout", lockErr)
	}

	if elapsed < 4*time.Second || elapsed > 10*time.Second {
		t.Errorf("lock timeout elapsed = %v, expected ~5s", elapsed)
	}
}

func TestOpenRejectsAncestorSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	realSub := filepath.Join(root, "ancestor_real")
	if err := os.MkdirAll(realSub, 0o700); err != nil {
		t.Fatal(err)
	}

	symAncestor := filepath.Join(root, "ancestor_sym")
	if err := os.Symlink(realSub, symAncestor); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	logPath := filepath.Join(symAncestor, "nested", "sesh.log")

	sink, err := Open(logPath)
	if sink != nil {
		_ = sink.Close()

		t.Fatal("Open() succeeded unexpectedly for ancestor symlink path")
	}

	if !errors.Is(err, ErrLogPathNotRegular) {
		t.Fatalf("Open(ancestor symlink) error = %v, want %v", err, ErrLogPathNotRegular)
	}
}
