//go:build integration

package logging

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type workerProc struct {
	cmd    *exec.Cmd
	stdin  *bufio.Writer
	stdout *bufio.Scanner
	stderr *bytes.Buffer
}

func TestConcurrentAppendAndRotationIntegration(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "concurrent_integration.log")

	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}

	exe = filepath.Clean(exe)

	worker0 := startIntegrationWorker(t, exe, logPath, "worker_0")
	worker1 := startIntegrationWorker(t, exe, logPath, "worker_1")

	releaseWorkerBarrier(t, worker0, worker1)

	if err := worker0.cmd.Wait(); err != nil {
		t.Fatalf("worker0 failed: %v, stderr: %s", err, worker0.stderr.String())
	}

	if err := worker1.cmd.Wait(); err != nil {
		t.Fatalf("worker1 failed: %v, stderr: %s", err, worker1.stderr.String())
	}

	verifyWorkerOutputs(t, worker0, worker1)

	verifyLogPermissions(t, logPath)

	verifyIntegrationRecords(t, logPath)
}

func startIntegrationWorker(t *testing.T, exe, logPath, id string) *workerProc {
	t.Helper()

	//nolint:gosec // integration test invokes current test executable with controlled arguments
	cmd := exec.CommandContext(
		t.Context(),
		exe,
		"-test.run=^TestConcurrentAppendRotationWorkerHelper$",
		"-test.count=1",
		"-test.v",
	)

	cmd.Env = append(
		os.Environ(),
		"SESH_LOGGING_WORKER="+id,
		"SESH_LOGGING_PATH="+logPath,
	)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("worker %s StdinPipe: %v", id, err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("worker %s StdoutPipe: %v", id, err)
	}

	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		t.Fatalf("worker %s Start: %v", id, err)
	}

	proc := &workerProc{
		cmd:    cmd,
		stdin:  bufio.NewWriter(stdinPipe),
		stdout: bufio.NewScanner(stdoutPipe),
		stderr: stderrBuf,
	}

	for proc.stdout.Scan() {
		if proc.stdout.Text() == "READY" {
			break
		}
	}

	if err := proc.stdout.Err(); err != nil {
		t.Fatalf("worker %s readiness error: %v", id, err)
	}

	return proc
}

func releaseWorkerBarrier(t *testing.T, w0, w1 *workerProc) {
	t.Helper()

	if _, err := w0.stdin.WriteString("START\n"); err != nil {
		t.Fatalf("release worker0: %v", err)
	}

	if err := w0.stdin.Flush(); err != nil {
		t.Fatalf("flush worker0 stdin: %v", err)
	}

	if _, err := w1.stdin.WriteString("START\n"); err != nil {
		t.Fatalf("release worker1: %v", err)
	}

	if err := w1.stdin.Flush(); err != nil {
		t.Fatalf("flush worker1 stdin: %v", err)
	}
}

func verifyWorkerOutputs(t *testing.T, w0, w1 *workerProc) {
	t.Helper()

	if w0.stderr.Len() > 0 {
		t.Fatalf("worker0 emitted stderr: %s", w0.stderr.String())
	}

	if w1.stderr.Len() > 0 {
		t.Fatalf("worker1 emitted stderr: %s", w1.stderr.String())
	}
}

func verifyLogPermissions(t *testing.T, logPath string) {
	t.Helper()

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat logPath: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Errorf("logPath mode = %04o, want 0600", info.Mode().Perm())
	}

	lockInfo, err := os.Stat(logPath + ".lock")
	if err != nil {
		t.Fatalf("stat lockPath: %v", err)
	}

	if lockInfo.Mode().Perm() != 0o600 {
		t.Errorf("lockPath mode = %04o, want 0600", lockInfo.Mode().Perm())
	}

	if _, err := os.Lstat(logPath + ".rotate.tmp"); !os.IsNotExist(err) {
		t.Errorf("temp rotation file exists: %v", err)
	}

	if _, err := os.Lstat(rotationPath(logPath, 4)); !os.IsNotExist(err) {
		t.Errorf("excess backup 4 exists: %v", err)
	}
}

func verifyIntegrationRecords(t *testing.T, logPath string) {
	t.Helper()

	records := readRotatedRecords(t, logPath, 3, 512)
	if len(records) == 0 {
		t.Fatal("no records found in rotated logs")
	}

	seenWorkerSeq := make(map[string]struct{})
	lastSeqPerWorker := make(map[string]int)

	for _, rec := range records {
		worker, ok := rec["worker"].(string)
		if !ok || worker == "" {
			t.Fatalf("missing or invalid worker field in record: %#v", rec)
		}

		seqVal, ok := rec["seq"].(float64)
		if !ok {
			t.Fatalf("missing or invalid seq field in record: %#v", rec)
		}

		seq := int(seqVal)
		key := worker + "/" + strconv.Itoa(seq)

		if _, exists := seenWorkerSeq[key]; exists {
			t.Fatalf("duplicate record found: %s", key)
		}

		seenWorkerSeq[key] = struct{}{}

		if prev, exists := lastSeqPerWorker[worker]; exists && seq <= prev {
			t.Fatalf("worker %s seq not strictly increasing: got %d after %d", worker, seq, prev)
		}

		lastSeqPerWorker[worker] = seq

		verifyRecordSchema(t, rec)
	}
}

func verifyRecordSchema(t *testing.T, rec map[string]any) {
	t.Helper()

	level, _ := rec["level"].(string)
	if level != "info" {
		t.Errorf("expected level info, got %q", level)
	}

	timeStr, _ := rec["time"].(string)
	if _, err := time.Parse(time.RFC3339Nano, timeStr); err != nil {
		t.Errorf("invalid time format %q: %v", timeStr, err)
	}

	msg, _ := rec["message"].(string)
	if msg != "concurrent-worker-event" {
		t.Errorf("unexpected message: %q", msg)
	}
}

func TestConcurrentAppendRotationWorkerHelper(t *testing.T) {
	workerID := os.Getenv("SESH_LOGGING_WORKER")
	if workerID == "" {
		return
	}

	rawPath := os.Getenv("SESH_LOGGING_PATH")
	if rawPath == "" {
		os.Exit(1)
	}

	logPath := filepath.Clean(rawPath)

	sink, err := Open(logPath)
	if err != nil {
		os.Exit(1)
	}

	sink.rotation.policy = rotationPolicy{
		maxBytes:    512,
		backupCount: 3,
	}

	_, _ = os.Stdout.WriteString("READY\n")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || scanner.Text() != "START" {
		_ = sink.Close()

		os.Exit(1)
	}

	logger := sink.Logger()
	for seq := range 80 {
		logger.Info().
			Str("worker", workerID).
			Int("seq", seq).
			Str("pad", strings.Repeat("z", 24)).
			Msg("concurrent-worker-event")
	}

	if err := sink.Close(); err != nil {
		os.Exit(1)
	}

	os.Exit(0)
}
