//go:build integration && (linux || darwin)

package snapshot_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/zigai/gotmux/tmux"
)

const (
	launchHelperPackage = "./testdata/creation/launchhelper"

	maxReportFileSize       = 1 << 20
	maxReportSectionEntries = 4096
)

var errReportTooLarge = errors.New("report file exceeds maximum allowed size")

func buildLaunchHelper(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "launchhelper")

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", path, launchHelperPackage)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build launch helper: %v: %s", err, out)
	}

	return path
}

type processReport struct {
	PID  int
	Argv []string
	Env  []string
}

func readBoundedReportFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open report %s: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()

	lr := io.LimitReader(f, maxBytes+1)

	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("read report %s: %w", path, err)
	}

	if int64(len(data)) > maxBytes {
		return nil, errReportTooLarge
	}

	return data, nil
}

func awaitProcessReport(t *testing.T, path string) processReport {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error

	for {
		data, err := readBoundedReportFile(path, maxReportFileSize)
		if err == nil {
			return parseProcessReport(t, path, data)
		}

		if errors.Is(err, errReportTooLarge) {
			t.Fatalf("launch report %s exceeds maximum size %d bytes", path, maxReportFileSize)
		}

		lastErr = err

		select {
		case <-ctx.Done():
			t.Fatalf("no launch report at %s: %v", path, lastErr)
		case <-ticker.C:
		}
	}
}

func popNULTerminated(data []byte, idx *int) (string, bool) {
	if *idx >= len(data) {
		return "", false
	}

	pos := bytes.IndexByte(data[*idx:], 0)
	if pos == -1 {
		return "", false
	}

	val := string(data[*idx : *idx+pos])
	*idx += pos + 1

	return val, true
}

func applyReportSection(t *testing.T, path, name string, entries []string, report *processReport) {
	t.Helper()

	switch name {
	case "pid":
		if len(entries) != 1 {
			t.Fatalf("malformed launch report pid section in %s: expected 1 entry, got %d", path, len(entries))
		}

		pid, err := strconv.Atoi(entries[0])
		if err != nil || pid <= 0 {
			t.Fatalf("malformed pid in %s: %q", path, entries[0])
		}

		report.PID = pid
	case "argv":
		report.Argv = entries
	case "env":
		report.Env = entries
	default:
		t.Fatalf("unknown section %q in %s", name, path)
	}
}

func readSectionEntries(t *testing.T, path, name string, data []byte, idx *int) []string {
	t.Helper()

	countStr, ok := popNULTerminated(data, idx)
	if !ok {
		t.Fatalf("malformed launch report at %s: unexpected EOF reading section %s count", path, name)
	}

	count, err := strconv.Atoi(countStr)
	if err != nil || count < 0 || count > maxReportSectionEntries {
		t.Fatalf("malformed launch report section %s count in %s: %q (max %d)", name, path, countStr, maxReportSectionEntries)
	}

	entries := make([]string, count)

	for i := range count {
		entry, ok := popNULTerminated(data, idx)
		if !ok {
			t.Fatalf("malformed launch report section %s entry %d in %s: unexpected EOF", name, i, path)
		}

		entries[i] = entry
	}

	return entries
}

func parseProcessReport(t *testing.T, path string, data []byte) processReport {
	t.Helper()

	var report processReport

	idx := 0
	sectionsSeen := make(map[string]bool)

	for idx < len(data) {
		name, ok := popNULTerminated(data, &idx)
		if !ok {
			t.Fatalf("malformed launch report at %s: unexpected EOF reading section name", path)
		}

		if sectionsSeen[name] {
			t.Fatalf("duplicate section %q in launch report %s", name, path)
		}

		sectionsSeen[name] = true
		entries := readSectionEntries(t, path, name, data, &idx)

		applyReportSection(t, path, name, entries, &report)
	}

	if !sectionsSeen["pid"] || !sectionsSeen["argv"] || !sectionsSeen["env"] || report.PID <= 0 || len(report.Argv) == 0 {
		t.Fatalf("incomplete launch report at %s (pid=%d, argv=%d, env=%d)", path, report.PID, len(report.Argv), len(report.Env))
	}

	return report
}

func awaitPaneDead(t *testing.T, window tmux.Window, id tmux.PaneID) tmux.PaneInfo {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		panes, err := window.Panes(ctx)
		if err != nil {
			t.Fatalf("list window panes: %v", err)
		}

		for _, pane := range panes {
			if pane.ID == id && pane.Dead {
				return pane
			}
		}

		select {
		case <-ctx.Done():
			t.Fatalf("pane %s never reported a terminated process", id)
		case <-ticker.C:
		}
	}
}
