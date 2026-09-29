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

	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
)

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

func TestResourceRowsAggregateSubtreesAndRejectPIDReuse(t *testing.T) {
	at := time.Unix(100, 0)
	previous := ResourceSample{At: at, Processes: map[int]ProcessSample{
		100: {PID: 100, StartTime: 1, CPUTicks: 100},
		101: {PID: 101, ParentPID: 100, StartTime: 2, CPUTicks: 50},
		200: {PID: 200, StartTime: 3, CPUTicks: 20},
	}}
	current := ResourceSample{At: at.Add(time.Second), Processes: map[int]ProcessSample{
		100: {PID: 100, StartTime: 1, CPUTicks: 200, RSSBytes: 1024 * 1024},
		101: {PID: 101, ParentPID: 100, StartTime: 2, CPUTicks: 150, RSSBytes: 2 * 1024 * 1024},
		200: {PID: 200, StartTime: 3, CPUTicks: 70, RSSBytes: 4 * 1024 * 1024},
	}}
	targets := []ResourceTarget{
		{PaneID: "%1", PanePID: 100, WindowIndex: 0, SessionKey: "s"},
		{PaneID: "%2", PanePID: 200, WindowIndex: 1, SessionKey: "s"},
		{PaneID: "%3", PanePID: 999, WindowIndex: 2, SessionKey: "s", Remote: true},
	}

	rows := ResourceRows(current, previous, targets)
	if got := rows["%1"][display.FieldPaneCPU]; got.Text != "200%" || got.State != layout.Present {
		t.Fatalf("pane cpu = %+v", got)
	}

	if got := rows["%1"][display.FieldPaneMem].Text; got != "3MiB" {
		t.Fatalf("pane memory = %q", got)
	}

	if got := rows["%1"][display.FieldSessionCPU].Text; got != "250%" {
		t.Fatalf("session cpu = %q", got)
	}

	if got := rows["%3"][display.FieldPaneCPU].State; got != layout.Unsupported {
		t.Fatalf("remote cpu state = %v", got)
	}

	current.Processes[101] = ProcessSample{PID: 101, ParentPID: 100, StartTime: 999, CPUTicks: 5}
	if got := ResourceRows(current, previous, targets)["%1"][display.FieldPaneCPU].State; got != layout.Pending {
		t.Fatalf("reused pid cpu state = %v", got)
	}
}
