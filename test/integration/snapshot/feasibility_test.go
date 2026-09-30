//go:build integration

package snapshot_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"
)

func TestPaneStateIsNotATerminalCheckpoint(t *testing.T) {
	server := tmuxtest.NewServer(t)

	info, err := server.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("published gotmux v0.4.0; tmux %v", info.Version)

	session, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    "terminal-probe",
		Program: tmux.Exec("/bin/sh", "-c", "printf '\033[31mCHECKPOINT_SENTINEL\033[0m'; exec sleep 300"),
		Size:    tmux.Size{Width: 80, Height: 24}, Start: tmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	pane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	content := waitForCapture(t, pane, []byte("CHECKPOINT_SENTINEL"))

	state, err := pane.Capture(t.Context(), tmux.CaptureOptions{PaneState: true})
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(state, []byte("\n")) {
		t.Fatalf("expected no pending terminal escape sequence, got %q", state)
	}

	if len(content) == 0 {
		t.Fatal("probe produced no terminal content")
	}
}

func waitForCapture(t *testing.T, pane tmux.Pane, marker []byte) []byte {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		data, err := pane.Capture(ctx, tmux.CaptureOptions{IncludeEscapes: true, PreserveSpaces: true})
		if err != nil {
			t.Fatal(err)
		}

		if bytes.Contains(data, marker) {
			return data
		}

		select {
		case <-ctx.Done():
			t.Fatal("terminal helper did not become ready")
		case <-ticker.C:
		}
	}
}

type observedPane struct {
	Index  int
	Width  int
	Height int
	Left   int
	Top    int
	Active bool
}

func observePanes(t *testing.T, window tmux.Window) []observedPane {
	t.Helper()

	panes, err := window.Panes(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	result := make([]observedPane, 0, len(panes))
	for _, pane := range panes {
		result = append(result, observedPane{pane.Index, pane.Width, pane.Height, pane.Left, pane.Top, pane.Active})
	}

	return result
}

func TestStructuralOracleDetectsGeometryAndOptions(t *testing.T) {
	server := tmuxtest.NewServer(t)

	windows, err := server.Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	window := windows[0].Handle()

	first, err := window.ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	second, err := first.Split(t.Context(), tmux.SplitOptions{
		Direction: tmux.DirectionHorizontal, Size: tmux.SplitSize{Cells: 31}, Program: tmux.Exec("/bin/sh"),
	})
	if err != nil {
		t.Fatal(err)
	}

	before := observePanes(t, window)
	if err = second.Resize(t.Context(), tmux.Size{Width: 25}); err != nil {
		t.Fatal(err)
	}

	if cmp.Equal(before, observePanes(t, window)) {
		t.Fatal("oracle ignored changed pane geometry")
	}

	verifyOptionOracle(t, window)
}

func verifyOptionOracle(t *testing.T, window tmux.Window) {
	t.Helper()

	if err := window.Options().SetUser(t.Context(), "@oracle", "before"); err != nil {
		t.Fatal(err)
	}

	optionsBefore, err := window.Options().List(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if err := window.Options().SetUser(t.Context(), "@oracle", "after"); err != nil {
		t.Fatal(err)
	}

	optionsAfter, err := window.Options().List(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(optionsBefore) != len(optionsAfter) || optionsBefore[0].Name != optionsAfter[0].Name {
		t.Fatal("unexpected option inventory change")
	}

	valueBefore, _ := optionsBefore[0].Value.Get()

	valueAfter, _ := optionsAfter[0].Value.Get()
	if valueBefore == valueAfter {
		t.Fatal("oracle ignored changed option value")
	}
}
