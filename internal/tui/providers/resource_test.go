package providers

import (
	"testing"
	"time"

	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
)

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
