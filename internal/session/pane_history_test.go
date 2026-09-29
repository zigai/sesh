//go:build linux || darwin

package session_test

import (
	"context"
	"testing"

	"github.com/zigai/sesh/internal/session"
)

const testSessionDev = "dev"

func TestSortPaneHistoryDeterministicOrdering(t *testing.T) {
	t.Parallel()

	input := []session.PaneHistory{
		{SessionName: "ops", WindowIndex: 1, PaneIndex: 0, Lines: 10},
		{SessionName: testSessionDev, WindowIndex: 10, PaneIndex: 0, Lines: 25},
		{SessionName: testSessionDev, WindowIndex: 2, PaneIndex: 1, Lines: 15},
		{SessionName: testSessionDev, WindowIndex: 2, PaneIndex: 0, Lines: 50},
		{SessionName: testSessionDev, WindowIndex: 1, PaneIndex: 0, Lines: 5},
	}

	session.SortPaneHistory(input)

	expected := []session.PaneHistory{
		{SessionName: testSessionDev, WindowIndex: 1, PaneIndex: 0, Lines: 5},
		{SessionName: testSessionDev, WindowIndex: 2, PaneIndex: 0, Lines: 50},
		{SessionName: testSessionDev, WindowIndex: 2, PaneIndex: 1, Lines: 15},
		{SessionName: testSessionDev, WindowIndex: 10, PaneIndex: 0, Lines: 25},
		{SessionName: "ops", WindowIndex: 1, PaneIndex: 0, Lines: 10},
	}

	for i, want := range expected {
		got := input[i]
		if got.SessionName != want.SessionName ||
			got.WindowIndex != want.WindowIndex ||
			got.PaneIndex != want.PaneIndex ||
			got.Lines != want.Lines {
			t.Fatalf("pane[%d] = %+v, want %+v", i, got, want)
		}
	}
}

func TestSortPaneHistorySparseIndexes(t *testing.T) {
	t.Parallel()

	input := []session.PaneHistory{
		{SessionName: "alpha", WindowIndex: 100, PaneIndex: 5, Lines: 1},
		{SessionName: "alpha", WindowIndex: 3, PaneIndex: 0, Lines: 10},
		{SessionName: "alpha", WindowIndex: 25, PaneIndex: 1, Lines: 20},
		{SessionName: "alpha", WindowIndex: 3, PaneIndex: 2, Lines: 15},
	}

	session.SortPaneHistory(input)

	wantIndexes := [][2]int{
		{3, 0},
		{3, 2},
		{25, 1},
		{100, 5},
	}

	for i, want := range wantIndexes {
		if input[i].WindowIndex != want[0] || input[i].PaneIndex != want[1] {
			t.Fatalf("at %d: got window %d pane %d, want window %d pane %d",
				i, input[i].WindowIndex, input[i].PaneIndex, want[0], want[1])
		}
	}
}

func TestSortPaneHistoryRawNameTiebreak(t *testing.T) {
	t.Parallel()

	input := []session.PaneHistory{
		{SessionName: "sess", NameRaw: session.RawBytes("sess-\xff"), WindowIndex: 1, PaneIndex: 0},
		{SessionName: "sess", NameRaw: session.RawBytes("sess-\xfe"), WindowIndex: 1, PaneIndex: 0},
	}

	session.SortPaneHistory(input)

	if string(input[0].NameRaw) != "sess-\xfe" || string(input[1].NameRaw) != "sess-\xff" {
		t.Fatalf("expected raw bytes tiebreak order, got %+v", input)
	}
}

func TestSummarizePaneHistory(t *testing.T) {
	t.Parallel()

	panes := []session.PaneHistory{
		{SessionName: testSessionDev, WindowIndex: 1, PaneIndex: 0, Lines: 50},
		{SessionName: "ops", WindowIndex: 1, PaneIndex: 0, Lines: 10},
		{SessionName: testSessionDev, WindowIndex: 1, PaneIndex: 1, Lines: 25},
		{SessionName: "stage", WindowIndex: 1, PaneIndex: 0, Lines: 75},
	}

	summaries := session.SummarizePaneHistory(panes)

	if len(summaries) != 3 {
		t.Fatalf("got %d summaries, want 3", len(summaries))
	}

	if summaries[0].SessionName != testSessionDev || summaries[0].Lines != 75 || summaries[0].PaneCount != 2 {
		t.Fatalf("summary[0] = %+v, want dev with 75 lines, 2 panes", summaries[0])
	}

	if summaries[1].SessionName != "stage" || summaries[1].Lines != 75 || summaries[1].PaneCount != 1 {
		t.Fatalf("summary[1] = %+v, want stage with 75 lines, 1 pane", summaries[1])
	}

	if summaries[2].SessionName != "ops" || summaries[2].Lines != 10 || summaries[2].PaneCount != 1 {
		t.Fatalf("summary[2] = %+v, want ops with 10 lines, 1 pane", summaries[2])
	}
}

func TestSummarizePaneHistoryEmpty(t *testing.T) {
	t.Parallel()

	summaries := session.SummarizePaneHistory(nil)
	if len(summaries) != 0 {
		t.Fatalf("expected empty summaries, got %d", len(summaries))
	}
}

func TestListPaneHistoryNilServer(t *testing.T) {
	t.Parallel()

	panes, err := session.ListPaneHistory(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListPaneHistory(nil) returned error: %v", err)
	}

	if panes != nil {
		t.Fatalf("expected nil panes, got %+v", panes)
	}
}
