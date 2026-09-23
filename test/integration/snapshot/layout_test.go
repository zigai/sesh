//go:build integration

package snapshot_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/capture"
)

func TestLayoutRemappingPreservesExactGeometry(t *testing.T) {
	source := tmuxtest.NewServer(t)
	session, window, panes := layoutFixture(t, source, "source", 29)
	want := observePanes(t, window)

	info, err := window.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	layout, err := integration.DecodeLayout(info.Layout, map[string]string{string(panes[0].ID()): "left", string(panes[1].ID()): "right"})
	if err != nil {
		t.Fatal(err)
	}

	if err := session.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := window.Info(t.Context()); err == nil {
		t.Fatal("source window still exists")
	}

	target := tmuxtest.NewServer(t)
	if _, err := target.NewSession(t.Context(), tmux.NewSessionOptions{Name: "unrelated", Program: tmux.Exec("sleep", "300"), Start: tmux.ExistingOnly}); err != nil {
		t.Fatal(err)
	}

	_, restored, replacements := layoutFixture(t, target, "restored", 47)
	if panes[0].ID() == replacements[0].ID() {
		t.Fatal("fixture did not exercise ID remapping")
	}

	if cmp.Equal(want, observePanes(t, restored)) {
		t.Fatal("fixture lacks a geometric difference")
	}

	remapped, err := integration.EncodeLayout(layout, map[string]tmux.PaneID{"left": replacements[0].ID(), "right": replacements[1].ID()})
	if err != nil {
		t.Fatal(err)
	}

	if err := restored.SelectLayout(t.Context(), remapped); err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(want, observePanes(t, restored)); diff != "" {
		t.Fatal(diff)
	}
}

func TestCapturePaneBordersAndZoom(t *testing.T) {
	for _, border := range []tmux.PaneBorderStatus{"off", "top", "bottom"} {
		t.Run(string(border), func(t *testing.T) {
			server := tmuxtest.NewServer(t)

			session, window, panes := layoutFixture(t, server, "borders", 29)
			if _, err := panes[0].Split(t.Context(), tmux.SplitOptions{Direction: tmux.Vertical, Size: tmux.SplitSize{Cells: 11}, Program: tmux.Exec("sleep", "300")}); err != nil {
				t.Fatal(err)
			}

			if err := window.Options().SetPaneBaseIndex(t.Context(), 1); err != nil {
				t.Fatal(err)
			}

			if err := window.Options().SetPaneBorderStatus(t.Context(), border); err != nil {
				t.Fatal(err)
			}

			assertCaptureGeometry(t, server, session, window)

			if err := panes[1].ToggleZoom(t.Context()); err != nil {
				t.Fatal(err)
			}

			assertCaptureGeometry(t, server, session, window)
		})
	}
}

func assertCaptureGeometry(t *testing.T, server *tmux.Server, session tmux.Session, window tmux.Window) {
	t.Helper()
	want := observePanes(t, window)

	result, err := capture.Observe(t.Context(), server, []tmux.SessionID{session.ID()})
	if err != nil {
		t.Fatalf("capture geometry %v: %v", want, err)
	}

	got := make([]observedPane, 0, len(result.Manifest.Panes))
	for _, pane := range result.Manifest.Panes {
		got = append(got, observedPane{pane.Index, pane.Width, pane.Height, pane.Left, pane.Top, pane.Active})
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func layoutFixture(t *testing.T, server *tmux.Server, name string, rightWidth int) (tmux.Session, tmux.Window, []tmux.Pane) {
	t.Helper()

	session, err := server.NewSession(t.Context(), tmux.NewSessionOptions{Name: name, Program: tmux.Exec("sleep", "300"), Size: tmux.Size{Width: 121, Height: 41}, Start: tmux.ExistingOnly})
	if err != nil {
		t.Fatal(err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	window := windows[0].Handle()

	first, err := window.ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	second, err := first.Split(t.Context(), tmux.SplitOptions{Direction: tmux.Horizontal, Size: tmux.SplitSize{Cells: rightWidth}, Program: tmux.Exec("sleep", "300")})
	if err != nil {
		t.Fatal(err)
	}

	return session, window, []tmux.Pane{first, second}
}

func TestCaptureSharedWindowsAndGroupClosure(t *testing.T) {
	server := tmuxtest.NewServer(t)
	first, window, _ := layoutFixture(t, server, "group-first", 29)

	second, err := server.NewSession(t.Context(), tmux.NewSessionOptions{Name: "group-second", Group: "group-first", Start: tmux.ExistingOnly})
	if err != nil {
		t.Fatal(err)
	}

	third, _, _ := layoutFixture(t, server, "linked-third", 29)

	index := 9
	if _, err := window.Link(t.Context(), third, tmux.LinkOptions{Index: &index}); err != nil {
		t.Fatal(err)
	}

	assertCapturedClosure(t, server, first, second, third)
}
