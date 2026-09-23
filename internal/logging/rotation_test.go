//go:build linux || darwin

package logging

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

const (
	rotationTestMaxBytes    int64 = 512
	rotationTestBackupCount int   = 3
)

func TestDefaultRotationPolicyUsesOneHundredMiB(t *testing.T) {
	t.Parallel()

	policy := defaultRotationPolicy()
	if policy.maxBytes != 100<<20 {
		t.Fatalf("maxBytes = %d, want %d", policy.maxBytes, int64(100<<20))
	}

	if policy.backupCount != 3 {
		t.Fatalf("backupCount = %d, want 3", policy.backupCount)
	}
}

func TestSinkRotatesCompleteJSONLinesIntoBoundedBackups(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sesh.log")
	sink := openTestRotatingSink(t, path, rotationPolicy{maxBytes: rotationTestMaxBytes, backupCount: 2})
	logger := sink.Logger()

	for sequence := range 40 {
		logger.Info().Int("sequence", sequence).Str("payload", strings.Repeat("x", 24)).Msg("rotation test")
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	records := readRotatedRecords(t, path, 2, rotationTestMaxBytes)
	previousSequence := -1

	for _, record := range records {
		sequenceValue, ok := record["sequence"].(float64)
		if !ok {
			t.Fatalf("sequence type = %T, want float64", record["sequence"])
		}

		sequence := int(sequenceValue)
		if sequence <= previousSequence {
			t.Fatalf("rotated sequence order = %d after %d", sequence, previousSequence)
		}

		previousSequence = sequence
	}

	if previousSequence != 39 {
		t.Fatalf("newest sequence = %d, want 39", previousSequence)
	}

	assertRotationArtifactsBounded(t, path, 2)
}

func TestSinkRotationFailureIsRetainedWithoutCorruptingActiveLog(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "sesh.log")
	sink := openTestRotatingSink(t, path, rotationPolicy{maxBytes: 1, backupCount: 2})
	logger := sink.Logger()
	logger.Info().Int("sequence", 1).Msg("before failed rotation")

	target := filepath.Join(root, "private-target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := os.Symlink(target, rotationPath(path, 2)); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	logger.Info().Int("sequence", 2).Msg("after failed rotation")

	if err := sink.Close(); !errors.Is(err, ErrRotationPathNotRegular) {
		t.Fatalf("Close() error = %v, want %v", err, ErrRotationPathNotRegular)
	}

	_, records := readJSONLines(t, path)
	if len(records) != 2 {
		t.Fatalf("active record count = %d, want 2", len(records))
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	}

	if string(data) != "unchanged" {
		t.Fatalf("rotation target = %q, want unchanged", data)
	}

	if _, err := os.Lstat(path + ".rotate.tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotation temporary file error = %v, want not exist", err)
	}
}

func TestSinkRotationRejectsSymlinkTempFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "sesh.log")
	sink := openTestRotatingSink(t, path, rotationPolicy{maxBytes: 1, backupCount: 2})
	logger := sink.Logger()
	logger.Info().Int("sequence", 1).Msg("initial")

	target := filepath.Join(root, "private-temp-target")
	if err := os.WriteFile(target, []byte("preserve-temp"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tempPath := path + ".rotate.tmp"
	if err := os.Symlink(target, tempPath); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}

	logger.Info().Int("sequence", 2).Msg("triggers rotation with symlink temp")

	if err := sink.Close(); !errors.Is(err, ErrRotationPathNotRegular) {
		t.Fatalf("Close() error = %v, want %v", err, ErrRotationPathNotRegular)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	}

	if string(data) != "preserve-temp" {
		t.Fatalf("temp target = %q, want preserve-temp", string(data))
	}
}

func TestSinkConcurrentGoroutineAppendAndRotation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "concurrent_rotate.log")
	sink := openTestRotatingSink(t, path, rotationPolicy{maxBytes: 1024, backupCount: 3})
	logger := sink.Logger()

	const (
		goroutines      = 10
		eventsPerWorker = 30
	)

	var wg sync.WaitGroup
	for w := range goroutines {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()

			for s := range eventsPerWorker {
				logger.Info().
					Int("worker", worker).
					Int("seq", s).
					Str("pad", strings.Repeat("a", 20)).
					Msg("event")
			}
		}(w)
	}

	wg.Wait()

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	records := readRotatedRecords(t, path, 3, 1024)
	if len(records) == 0 {
		t.Fatal("no records found after concurrent rotation")
	}

	assertRotationArtifactsBounded(t, path, 3)
}

func openTestRotatingSink(t *testing.T, path string, policy rotationPolicy) *Sink {
	t.Helper()

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	sink.rotation.policy = policy

	return sink
}

func readRotatedRecords(t *testing.T, path string, backupCount int, maxBytes int64) []map[string]any {
	t.Helper()

	records := []map[string]any{}

	for index := backupCount; index >= 0; index-- {
		currentPath := path
		if index > 0 {
			currentPath = rotationPath(path, index)
		}

		if _, err := os.Stat(currentPath); errors.Is(err, os.ErrNotExist) {
			continue
		}

		data, fileRecords := readJSONLines(t, currentPath)
		if index > 0 && int64(len(data)) > maxBytes {
			t.Fatalf("%s size = %d, want at most %d", filepath.Base(currentPath), len(data), maxBytes)
		}

		records = append(records, fileRecords...)
	}

	if len(records) == 0 {
		t.Fatal("rotated logs contain no records")
	}

	return records
}

func assertRotationArtifactsBounded(t *testing.T, path string, backupCount int) {
	t.Helper()

	for index := 0; index <= backupCount; index++ {
		currentPath := path
		if index > 0 {
			currentPath = rotationPath(path, index)
		}

		info, err := os.Stat(currentPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && index > 0 {
				continue
			}

			t.Fatalf("Stat(%s) error = %v", currentPath, err)
		}

		if info.Mode().Perm() != logFileMode {
			t.Fatalf("%s mode = %04o, want %04o", filepath.Base(currentPath), info.Mode().Perm(), logFileMode)
		}
	}

	if _, err := os.Lstat(rotationPath(path, backupCount+1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("excess backup error = %v, want not exist", err)
	}

	if _, err := os.Lstat(path + ".rotate.tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rotation temporary file error = %v, want not exist", err)
	}
}

func TestEqualActiveAndBackupBothSurvive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "equal.log")
	backupPath := rotationPath(path, 1)

	var content bytes.Buffer
	for i := range 5 {
		fmt.Fprintf(&content, "{\"level\":\"info\",\"heartbeat\":%d,\"message\":\"ping\"}\n", i)
	}

	if err := os.WriteFile(path, content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(backupPath, content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if info.Size() != int64(content.Len()) {
		t.Fatalf("active log was truncated or corrupted: size = %d, want %d", info.Size(), content.Len())
	}

	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}

	if backupInfo.Size() != int64(content.Len()) {
		t.Fatalf("backup log was truncated or corrupted: size = %d, want %d", backupInfo.Size(), content.Len())
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	records, err := ReadRecords(t.Context(), path, ReadOptions{})
	if err != nil {
		t.Fatalf("ReadRecords() error = %v", err)
	}

	if len(records) != 10 {
		t.Fatalf("record count = %d, want 10 (5 from backup + 5 from active)", len(records))
	}
}

func TestRenameRotationInterruptedAfterRename(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "interrupted_after_rename.log")
	backupPath := rotationPath(path, 1)

	var rotatedContent bytes.Buffer
	for i := range 8 {
		fmt.Fprintf(&rotatedContent, "{\"level\":\"info\",\"seq\":%d,\"message\":\"rotated\"}\n", i)
	}

	if err := os.WriteFile(backupPath, rotatedContent.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	logger := sink.Logger()
	for i := 8; i < 12; i++ {
		logger.Info().Int("seq", i).Msg("fresh")
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	records, err := ReadRecords(t.Context(), path, ReadOptions{})
	if err != nil {
		t.Fatalf("ReadRecords() error = %v", err)
	}

	if len(records) != 12 {
		t.Fatalf("got %d records, want 12", len(records))
	}
}

func TestRenameRotationInterruptedDuringBackupShift(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "interrupted_shift.log")
	backup2Path := rotationPath(path, 2)

	var content bytes.Buffer
	for i := range 5 {
		fmt.Fprintf(&content, "{\"level\":\"info\",\"seq\":%d,\"message\":\"active\"}\n", i)
	}

	if err := os.WriteFile(path, content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var b2Content bytes.Buffer
	for i := 10; i < 15; i++ {
		fmt.Fprintf(&b2Content, "{\"level\":\"info\",\"seq\":%d,\"message\":\"old\"}\n", i)
	}

	if err := os.WriteFile(backup2Path, b2Content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	sink, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	records, err := ReadRecords(t.Context(), path, ReadOptions{})
	if err != nil {
		t.Fatalf("ReadRecords() error = %v", err)
	}

	if len(records) != 10 {
		t.Fatalf("got %d records, want 10", len(records))
	}
}

func TestRotateLogFailsWhenNewActiveCannotBeCreated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "active.log")

	parentFile, baseName, err := openPinnedParentDir(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = parentFile.Close()
	}()

	file, err := openOrCreateActiveLog(parentFile, path, baseName)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = file.Close()
	}()

	initialData := []byte("{\"level\":\"info\",\"msg\":\"initial\"}\n")
	if _, err := file.Write(initialData); err != nil {
		t.Fatal(err)
	}

	state := &rotationState{
		path:     path,
		baseName: baseName,
		policy: rotationPolicy{
			maxBytes:    50,
			backupCount: 2,
		},
		preOpenHook: func(parentDir *os.File, name string) {
			_ = unix.Symlinkat("/dev/null", int(parentDir.Fd()), name)
		},
	}

	newFile, rotErr := rotateLog(file, state, parentFile)
	if rotErr == nil {
		if newFile != nil {
			_ = newFile.Close()
		}

		t.Fatal("rotateLog() expected error when new active cannot be created")
	}

	if newFile != nil {
		t.Fatalf("newFile = %v, want nil", newFile)
	}

	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		t.Fatalf("old file was prematurely closed: %v", err)
	}

	backupPath := rotationPath(path, 1)

	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup error = %v", err)
	}

	if !bytes.Equal(backupData, initialData) {
		t.Fatalf("backup data = %q, want %q", string(backupData), string(initialData))
	}
}
