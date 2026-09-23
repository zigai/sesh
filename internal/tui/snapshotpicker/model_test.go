package snapshotpicker

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zigai/sesh/internal/config"
)

func TestSnapshotPickerRendersTimelineAndRejectsInvalidSnapshot(t *testing.T) {
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	model := newSnapshotPickerTestModel(t, []Entry{
		{
			ID:           "snap-latest",
			Path:         "/tmp/tmux_resurrect_20260713T110000.txt",
			SavedAt:      now.Add(-time.Hour),
			SizeBytes:    2048,
			SessionCount: 2,
			WindowCount:  4,
			PaneCount:    7,
			Validity:     SnapshotValid,
			Latest:       true,
		},
		{
			ID:           "snap-previous",
			Path:         "/tmp/tmux_resurrect_20260712T120000.txt",
			SavedAt:      now.Add(-24 * time.Hour),
			SizeBytes:    1024,
			SessionCount: 1,
			WindowCount:  1,
			PaneCount:    2,
			Validity:     SnapshotInvalid,
			Warnings:     []string{"one pane has no window record"},
		},
	})
	model.now = now
	setSnapshotPickerSize(model, 100, 10)

	view := snapshotPickerStripANSI(snapshotPickerView(model.View()))
	for _, value := range []string{
		"Snapshot History · 2", "Timestamp", "Age", "Status", "File",
		"2026-07-13 11:00:00", "1h", "valid", "2.0K", "tmux_resurrect_20260713T110000.txt",
	} {
		if !strings.Contains(view, value) {
			t.Fatalf("snapshot picker view = %q, want %q", view, value)
		}
	}

	model, _ = updateSnapshotPickerTestModel(t, model, snapshotPickerKeyCode(tea.KeyDown))

	updated, cmd := updateSnapshotPickerTestModel(t, model, snapshotPickerKeyCode(tea.KeyEnter))
	if cmd != nil {
		t.Fatal("enter on invalid snapshot returned a command, want selection rejected")
	}

	if _, ok := updated.Selected(); ok {
		t.Fatal("Selected() reported a selection, want invalid snapshot rejected")
	}

	if got := updated.SelectedID(); got != "" {
		t.Fatalf("SelectedID() = %q, want no invalid selection", got)
	}
}

func TestSnapshotPickerSelectsPartialSnapshotWithoutClaimingValidity(t *testing.T) {
	t.Parallel()

	model := newSnapshotPickerTestModel(t, []Entry{{
		ID:       "partial-generation",
		Path:     "partial-generation",
		SavedAt:  time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC),
		Validity: SnapshotPartial,
		Warnings: []string{"parity evidence is incomplete"},
	}})
	setSnapshotPickerSize(model, 80, 8)

	view := snapshotPickerStripANSI(snapshotPickerView(model.View()))
	if !strings.Contains(view, "partial") {
		t.Fatalf("partial evidence is not visible: %q", view)
	}

	updated, cmd := updateSnapshotPickerTestModel(t, model, snapshotPickerKeyCode(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("partial selection did not quit")
	}

	if got := updated.SelectedID(); got != "partial-generation" {
		t.Fatalf("selected generation = %q", got)
	}
}

func TestSnapshotPickerEmptyStateDoesNotSelect(t *testing.T) {
	model := newSnapshotPickerTestModel(t, nil)
	setSnapshotPickerSize(model, 48, 8)

	if view := snapshotPickerStripANSI(snapshotPickerView(model.View())); !strings.Contains(view, "No saved snapshots found") {
		t.Fatalf("empty snapshot picker view = %q", view)
	}

	updated, cmd := updateSnapshotPickerTestModel(t, model, snapshotPickerKeyCode(tea.KeyEnter))
	if cmd != nil {
		t.Fatalf("empty enter command = %v, want nil", cmd)
	}

	if _, ok := updated.Selected(); ok || updated.SelectedID() != "" {
		t.Fatalf("empty enter selection = %v/%q, want none", ok, updated.SelectedID())
	}
}

func TestSnapshotPickerInvalidEntryShowsReasonWithoutSelecting(t *testing.T) {
	model := newSnapshotPickerTestModel(t, []Entry{
		{
			ID:       "snap-broken",
			Path:     "/tmp/broken.txt",
			SavedAt:  time.Now(),
			Validity: SnapshotInvalid,
			Warnings: []string{"snapshot has no restorable pane records"},
		},
	})
	setSnapshotPickerSize(model, 72, 8)

	updated, cmd := updateSnapshotPickerTestModel(t, model, snapshotPickerKeyCode(tea.KeyEnter))
	if cmd != nil {
		t.Fatalf("invalid enter command = %v, want nil", cmd)
	}

	if _, ok := updated.Selected(); ok || updated.SelectedID() != "" {
		t.Fatalf("invalid enter selection = %v/%q, want none", ok, updated.SelectedID())
	}

	view := snapshotPickerStripANSI(snapshotPickerView(updated.View()))
	if !strings.Contains(view, "Cannot restore: snapshot has no restorable pane records") {
		t.Fatalf("invalid snapshot view = %q, want disabled reason", view)
	}
}

func TestSnapshotPickerNarrowAndConstrainedViewsStayWithinBounds(t *testing.T) {
	entries := make([]Entry, 8)
	for index := range entries {
		entries[index] = Entry{
			ID:       "snap-" + string(rune('a'+index)),
			Path:     "/tmp/snapshot-with-a-very-long-file-name-" + string(rune('a'+index)) + ".txt",
			SavedAt:  time.Now().Add(-time.Duration(index) * time.Hour),
			Validity: SnapshotValid,
		}
	}

	tests := []struct {
		name   string
		width  int
		height int
	}{
		{name: "narrow", width: 30, height: 9},
		{name: "constrained height", width: 64, height: 5},
		{name: "tiny", width: 4, height: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := newSnapshotPickerTestModel(t, entries)
			setSnapshotPickerSize(model, tt.width, tt.height)
			view := snapshotPickerView(model.View())

			lines := strings.Split(view, "\n")
			if len(lines) > tt.height {
				t.Fatalf("view line count = %d, want <= %d: %q", len(lines), tt.height, view)
			}

			for _, line := range lines {
				if width := ansi.StringWidth(line); width != tt.width {
					t.Fatalf("view line width = %d, want %d: %q", width, tt.width, line)
				}
			}
		})
	}
}

func TestSnapshotPickerPagingAndMouseSelectionUseVisibleRows(t *testing.T) {
	entries := make([]Entry, 6)
	for index := range entries {
		entries[index] = Entry{
			ID:       "snap-" + string(rune('0'+index)),
			Path:     "/tmp/snapshot-" + string(rune('0'+index)) + ".txt",
			SavedAt:  time.Now(),
			Validity: SnapshotValid,
		}
	}

	model := newSnapshotPickerTestModel(t, entries)
	setSnapshotPickerSize(model, 72, 7)

	model, _ = updateSnapshotPickerTestModel(t, model, snapshotPickerKeyCode(tea.KeyPgDown))
	if model.cursor == 0 || model.listTop == 0 {
		t.Fatalf("page down cursor/listTop = %d/%d, want scrolled", model.cursor, model.listTop)
	}

	scrolled := model.cursor

	model, _ = updateSnapshotPickerTestModel(t, model, snapshotPickerMouseWheel(1, 3, tea.MouseWheelDown))
	if want := min(scrolled+3, len(entries)-1); model.cursor != want {
		t.Fatalf("wheel down cursor = %d, want %d", model.cursor, want)
	}

	model, cmd := updateSnapshotPickerTestModel(t, model, snapshotPickerMouseClick(1, 3, tea.MouseRight))
	if cmd != nil {
		t.Fatal("right click returned a command, want selection without activation")
	}

	updated, cmd := updateSnapshotPickerTestModel(t, model, snapshotPickerMouseClick(1, 3, tea.MouseLeft))
	if cmd == nil {
		t.Fatal("click on visible snapshot returned nil command, want tea.Quit")
	}

	selected, ok := updated.Selected()
	if !ok {
		t.Fatal("click on visible snapshot selected nothing, want visible row")
	}

	if selected.Path != entries[model.listTop].Path || selected.ID != entries[model.listTop].ID {
		t.Fatalf("mouse selected %q/%q, want visible row %q/%q",
			selected.ID, selected.Path, entries[model.listTop].ID, entries[model.listTop].Path)
	}
}

func TestNewModelRejectsNilConfigAndClonesWarnings(t *testing.T) {
	if _, err := NewModel(nil, nil); err == nil {
		t.Fatal("NewModel(nil) error = nil, want error")
	}

	entries := []Entry{{Warnings: []string{"original"}}}
	model := newSnapshotPickerTestModel(t, entries)

	entries[0].Warnings[0] = "changed"
	if got := model.entries[0].Warnings[0]; got != "original" {
		t.Fatalf("model warning = %q, want defensive copy", got)
	}
}

func newSnapshotPickerTestModel(t *testing.T, entries []Entry) *Model {
	t.Helper()

	model, err := NewModel(config.DefaultConfig(), entries)
	if err != nil {
		t.Fatalf("NewModel() returned error: %v", err)
	}

	return model
}

func setSnapshotPickerSize(model *Model, width int, height int) {
	model.width = width
	model.height = height
	model.ensureSnapshotCursorVisible()
}

func updateSnapshotPickerTestModel(
	t *testing.T,
	model *Model,
	msg tea.Msg,
) (*Model, tea.Cmd) {
	t.Helper()

	updated, cmd := model.Update(msg)

	next, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update() returned %T, want *Model", updated)
	}

	return next, cmd
}

func snapshotPickerStripANSI(s string) string {
	return ansi.Strip(s)
}

func snapshotPickerView(view tea.View) string {
	return view.Content
}

func snapshotPickerKeyCode(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

func snapshotPickerMouseClick(x int, y int, button tea.MouseButton) tea.MouseClickMsg {
	return tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: button})
}

func snapshotPickerMouseWheel(x int, y int, button tea.MouseButton) tea.MouseWheelMsg {
	return tea.MouseWheelMsg(tea.Mouse{X: x, Y: y, Button: button})
}
