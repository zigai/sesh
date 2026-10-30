//go:build linux

package providers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReadResourceSampleParsesProcessIdentityAndRSS(t *testing.T) {
	root := t.TempDir()
	for _, process := range []struct {
		pid, parent         int
		start, user, system uint64
		resident            string
	}{
		{pid: 100, parent: 1, start: 400, user: 120, system: 30, resident: "12"},
		{pid: 101, parent: 100, start: 401, user: 40, system: 10, resident: "8"},
	} {
		dir := filepath.Join(root, strconv.Itoa(process.pid))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(resourceStat(process.pid, process.parent, process.start, process.user, process.system)), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "statm"), []byte("20 "+process.resident+" 0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	measured, err := ReadResourceSample(t.Context(), root, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}

	pageSize := os.Getpagesize()
	if pageSize <= 0 {
		t.Fatal("invalid page size")
	}

	if len(measured.Processes) != 2 || measured.Processes[100].StartTime != 400 || measured.Processes[100].CPUTicks != 150 || strconv.FormatUint(measured.Processes[101].RSSBytes, 10) != strconv.Itoa(8*pageSize) {
		t.Fatalf("sample = %+v", measured)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := ReadResourceSample(ctx, root, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %v", err)
	}
}

func resourceStat(pid, parent int, start, user, system uint64) string {
	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}

	fields[0] = "S"
	fields[1] = strconv.Itoa(parent)
	fields[11] = strconv.FormatUint(user, 10)
	fields[12] = strconv.FormatUint(system, 10)
	fields[19] = strconv.FormatUint(start, 10)

	return strconv.Itoa(pid) + " (name with ) paren) " + strings.Join(fields, " ")
}
