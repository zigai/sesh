//go:build linux

package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samber/oops"
)

type foregroundFixtureProcess struct {
	pid, parent, group, foregroundGroup int
	startTime                           uint64
	children                            []int
	exited                              bool
}

func TestSelectLinuxForegroundUsesUniqueProcessGroupRoot(t *testing.T) {
	tests := []struct {
		name      string
		processes []foregroundFixtureProcess
		wantPID   int
		wantErr   error
	}{
		{
			name: "pane owns foreground group",
			processes: []foregroundFixtureProcess{
				{pid: 10, parent: 1, group: 10, foregroundGroup: 10, startTime: 100, children: []int{20}},
				{pid: 20, parent: 10, group: 10, foregroundGroup: 10, startTime: 200},
			},
			wantPID: 10,
		},
		{
			name: "shell foreground child",
			processes: []foregroundFixtureProcess{
				{pid: 10, parent: 1, group: 10, foregroundGroup: 20, startTime: 100, children: []int{20}},
				{pid: 20, parent: 10, group: 20, foregroundGroup: 20, startTime: 200, children: []int{21}},
				{pid: 21, parent: 20, group: 20, foregroundGroup: 20, startTime: 210},
			},
			wantPID: 20,
		},
		{
			name: "nested shell foreground child",
			processes: []foregroundFixtureProcess{
				{pid: 10, parent: 1, group: 10, foregroundGroup: 30, startTime: 100, children: []int{20}},
				{pid: 20, parent: 10, group: 20, foregroundGroup: 30, startTime: 200, children: []int{30}},
				{pid: 30, parent: 20, group: 30, foregroundGroup: 30, startTime: 300},
			},
			wantPID: 30,
		},
		{
			name: "exited descendant is skipped",
			processes: []foregroundFixtureProcess{
				{pid: 10, parent: 1, group: 10, foregroundGroup: 20, startTime: 100, children: []int{20}},
				{pid: 20, parent: 10, group: 20, foregroundGroup: 20, startTime: 200, children: []int{30}},
				{pid: 30, parent: 20, group: 30, foregroundGroup: 0, startTime: 300, exited: true},
			},
			wantPID: 20,
		},
		{
			name: "exited foreground child is unavailable",
			processes: []foregroundFixtureProcess{
				{pid: 10, parent: 1, group: 10, foregroundGroup: 20, startTime: 100, children: []int{20}},
				{pid: 20, parent: 10, group: 20, foregroundGroup: 20, startTime: 200, exited: true},
			},
			wantErr: ErrForegroundUnavailable,
		},
		{
			name: "pipeline is ambiguous",
			processes: []foregroundFixtureProcess{
				{pid: 10, parent: 1, group: 10, foregroundGroup: 20, startTime: 100, children: []int{20, 21}},
				{pid: 20, parent: 10, group: 20, foregroundGroup: 20, startTime: 200},
				{pid: 21, parent: 10, group: 20, foregroundGroup: 20, startTime: 210},
			},
			wantErr: ErrForegroundAmbiguous,
		},
		{
			name: "missing group member is unavailable",
			processes: []foregroundFixtureProcess{
				{pid: 10, parent: 1, group: 10, foregroundGroup: 20, startTime: 100},
			},
			wantErr: ErrForegroundUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procRoot := writeForegroundProcFixture(t, tt.processes)

			selection, err := selectForeground(t.Context(), procForegroundSource{procRoot: procRoot, bootIDPath: ""}, ForegroundRequest{
				PanePID: 10, ExpectedPaneBirth: Identity{BootID: testBootID, StartTime: 100}, EnvironmentKeys: nil,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("selectForeground error = %v, want %v", err, tt.wantErr)
			}

			if err == nil && selection.process.pid != tt.wantPID {
				t.Fatalf("selected PID = %d, want %d", selection.process.pid, tt.wantPID)
			}
		})
	}
}

func TestForegroundErrorsHaveStableCodes(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{err: ErrForegroundUnavailable, code: CodeForegroundUnavailable},
		{err: ErrForegroundAmbiguous, code: CodeForegroundAmbiguous},
		{err: ErrForegroundChanged, code: CodeForegroundChanged},
	}

	for _, tt := range tests {
		wrapped := wrapError(tt.err, "observe_foreground")

		diagnostic, ok := oops.AsOops(wrapped)
		if !ok || diagnostic.Code() != tt.code {
			t.Fatalf("error %v code = %q, want %q", tt.err, diagnostic.Code(), tt.code)
		}
	}
}

func TestSelectLinuxForegroundRejectsPanePIDReuse(t *testing.T) {
	procRoot := writeForegroundProcFixture(t, []foregroundFixtureProcess{
		{pid: 10, parent: 1, group: 10, foregroundGroup: 10, startTime: 101},
	})

	_, err := selectForeground(t.Context(), procForegroundSource{procRoot: procRoot, bootIDPath: ""}, ForegroundRequest{
		PanePID: 10, ExpectedPaneBirth: Identity{BootID: testBootID, StartTime: 100}, EnvironmentKeys: nil,
	})
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("reused pane PID error = %v, want identity mismatch", err)
	}
}

func TestLinuxForegroundSelectionDetectsGroupAndBirthRaces(t *testing.T) {
	initial := foregroundSelection{
		pane: foregroundProcess{
			pid: 10, parent: 1, group: 10, session: 10, tty: 1, foregroundGroup: 20, startTime: 100,
		},
		process: foregroundProcess{
			pid: 20, parent: 10, group: 20, session: 10, tty: 1, foregroundGroup: 20, startTime: 200,
		},
	}

	changedGroup := initial

	changedGroup.pane.foregroundGroup = 30
	if sameForegroundSelection(initial, changedGroup) {
		t.Fatal("foreground process-group change was not detected")
	}

	reusedPID := initial

	reusedPID.process.startTime++
	if sameForegroundSelection(initial, reusedPID) {
		t.Fatal("foreground PID birth change was not detected")
	}
}

func writeForegroundProcFixture(t *testing.T, processes []foregroundFixtureProcess) string {
	t.Helper()

	procRoot := t.TempDir()

	for _, process := range processes {
		pid := strconv.Itoa(process.pid)
		pidDir := filepath.Join(procRoot, pid)
		taskDir := filepath.Join(pidDir, "task", pid)

		if err := os.MkdirAll(taskDir, 0o700); err != nil {
			t.Fatalf("create process fixture: %v", err)
		}

		stat := foregroundStatContent(process)
		if err := os.WriteFile(filepath.Join(pidDir, "stat"), stat, 0o600); err != nil {
			t.Fatalf("write stat fixture: %v", err)
		}

		children := ""
		for _, child := range process.children {
			children += strconv.Itoa(child) + " "
		}

		if err := os.WriteFile(filepath.Join(taskDir, "children"), []byte(children), 0o600); err != nil {
			t.Fatalf("write children fixture: %v", err)
		}
	}

	return procRoot
}

func foregroundStatContent(process foregroundFixtureProcess) []byte {
	state := "S"
	if process.exited {
		state = "Z"
	}

	return []byte(fmt.Sprintf(
		"%d (fixture) %s %d %d 10 34816 %d 0 0 0 0 0 0 0 0 0 20 0 1 0 %d 0\n",
		process.pid, state, process.parent, process.group, process.foregroundGroup, process.startTime,
	))
}
