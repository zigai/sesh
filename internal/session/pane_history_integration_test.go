//go:build (linux || darwin) && integration

package session_test

import (
	"context"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/session"
)

func TestPaneHistoryLiveObservationWithDisposableGotmux(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	setupAlphaBetaPanes(t, ctx, server)

	panes, err := session.ListPaneHistory(ctx, server)
	if err != nil {
		t.Fatalf("ListPaneHistory() returned error: %v", err)
	}

	session.SortPaneHistory(panes)

	if len(panes) < 3 {
		t.Fatalf("expected at least 3 panes, got %d: %+v", len(panes), panes)
	}

	summaries := session.SummarizePaneHistory(panes)
	if len(summaries) < 2 {
		t.Fatalf("expected at least 2 session summaries, got %d", len(summaries))
	}

	verifyAlphaBetaSummaries(t, summaries)
}

func setupAlphaBetaPanes(t *testing.T, ctx context.Context, server *gotmux.Server) {
	t.Helper()

	sessA, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "hist-alpha"})
	if err != nil {
		t.Fatalf("creating session alpha: %v", err)
	}

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "hist-beta"}); err != nil {
		t.Fatalf("creating session beta: %v", err)
	}

	panesA, err := sessA.Panes(ctx)
	if err != nil || len(panesA) == 0 {
		t.Fatalf("querying panes for alpha: %v", err)
	}

	paneHandle := panesA[0].Handle()
	if _, err := paneHandle.Split(ctx, gotmux.SplitOptions{Direction: gotmux.DirectionVertical}); err != nil {
		t.Fatalf("splitting pane in alpha: %v", err)
	}
}

func verifyAlphaBetaSummaries(t *testing.T, summaries []session.HistorySummary) {
	t.Helper()

	var foundAlpha, foundBeta bool

	for _, s := range summaries {
		if s.SessionName == "hist-alpha" {
			foundAlpha = true

			if s.PaneCount != 2 {
				t.Fatalf("alpha pane count = %d, want 2", s.PaneCount)
			}
		}

		if s.SessionName == "hist-beta" {
			foundBeta = true

			if s.PaneCount != 1 {
				t.Fatalf("beta pane count = %d, want 1", s.PaneCount)
			}
		}
	}

	if !foundAlpha || !foundBeta {
		t.Fatalf("missing alpha (%v) or beta (%v) in summaries: %+v", foundAlpha, foundBeta, summaries)
	}
}

func TestPaneHistorySharedWindowsAcrossSessions(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	setupSharedWindowSessions(t, ctx, server)

	panes, err := session.ListPaneHistory(ctx, server)
	if err != nil {
		t.Fatalf("ListPaneHistory() error = %v", err)
	}

	session.SortPaneHistory(panes)
	verifySharedPanesPresent(t, panes)
}

func setupSharedWindowSessions(t *testing.T, ctx context.Context, server *gotmux.Server) {
	t.Helper()

	sessA, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "sess-a"})
	if err != nil {
		t.Fatalf("creating session a: %v", err)
	}

	sessB, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "sess-b"})
	if err != nil {
		t.Fatalf("creating session b: %v", err)
	}

	winLinksA, err := sessA.Windows(ctx)
	if err != nil || len(winLinksA) == 0 {
		t.Fatalf("getting windows in sessA: %v", err)
	}

	sharedWin := winLinksA[0].Window()
	idx10 := 10

	if _, err := sharedWin.Link(ctx, sessB, gotmux.LinkOptions{Index: &idx10}); err != nil {
		t.Fatalf("linking window to sessB: %v", err)
	}
}

func verifySharedPanesPresent(t *testing.T, panes []session.PaneHistory) {
	t.Helper()

	var foundInA, foundInB bool

	for _, p := range panes {
		if p.SessionName == "sess-a" {
			foundInA = true
		}

		if p.SessionName == "sess-b" && p.WindowIndex == 10 {
			foundInB = true
		}
	}

	if !foundInA {
		t.Fatalf("expected pane reported for sess-a, got %+v", panes)
	}

	if !foundInB {
		t.Fatalf("expected shared window reported for sess-b at window index 10, got %+v", panes)
	}
}

func TestPaneHistoryNumericSortingIndex2vs10(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	setupSparseWindows(t, ctx, server)

	panes, err := session.ListPaneHistory(ctx, server)
	if err != nil {
		t.Fatalf("ListPaneHistory() error = %v", err)
	}

	session.SortPaneHistory(panes)
	verifySparsePanesOrder(t, panes)
}

func setupSparseWindows(t *testing.T, ctx context.Context, server *gotmux.Server) {
	t.Helper()

	sess, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "sess-sparse"})
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}

	idx10 := 10
	if _, err := sess.NewWindow(ctx, gotmux.NewWindowOptions{Index: &idx10}); err != nil {
		t.Fatalf("creating window at index 10: %v", err)
	}

	idx2 := 2
	if _, err := sess.NewWindow(ctx, gotmux.NewWindowOptions{Index: &idx2}); err != nil {
		t.Fatalf("creating window at index 2: %v", err)
	}
}

func verifySparsePanesOrder(t *testing.T, panes []session.PaneHistory) {
	t.Helper()

	var sparsePanes []session.PaneHistory

	for _, p := range panes {
		if p.SessionName == "sess-sparse" {
			sparsePanes = append(sparsePanes, p)
		}
	}

	seenWin2 := false
	seenWin10 := false

	for _, p := range sparsePanes {
		if p.WindowIndex == 2 {
			seenWin2 = true

			if seenWin10 {
				t.Fatalf("Window 10 appeared before Window 2 in sorted output: %+v", sparsePanes)
			}
		}

		if p.WindowIndex == 10 {
			seenWin10 = true

			if !seenWin2 {
				t.Fatalf("Window 10 appeared before Window 2 was seen: %+v", sparsePanes)
			}
		}
	}

	if !seenWin2 || !seenWin10 {
		t.Fatalf("expected windows 2 and 10, got %+v", sparsePanes)
	}
}

func TestPaneHistoryKilledServerReturnsEmpty(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "temp"}); err != nil {
		t.Fatal(err)
	}

	if err := server.Kill(ctx); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	panes, err := session.ListPaneHistory(ctx, server)
	if err != nil {
		t.Fatalf("unexpected error on killed server: %v", err)
	}

	if len(panes) != 0 {
		t.Fatalf("expected 0 panes after kill, got %d", len(panes))
	}
}

func TestPaneHistoryCanceledContextReturnsError(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := session.ListPaneHistory(ctx, server)
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}
}
