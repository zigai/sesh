//go:build integration

package snapshot_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
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

	layout, err := tmuxserver.DecodeLayout(info.Layout, map[string]string{string(panes[0].ID()): "left", string(panes[1].ID()): "right"})
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
	if _, err := target.NewSession(t.Context(), gotmux.NewSessionOptions{Name: "unrelated", Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly}); err != nil {
		t.Fatal(err)
	}

	_, restored, replacements := layoutFixture(t, target, "restored", 47)
	if panes[0].ID() == replacements[0].ID() {
		t.Fatal("fixture did not exercise ID remapping")
	}

	if cmp.Equal(want, observePanes(t, restored)) {
		t.Fatal("fixture lacks a geometric difference")
	}

	remapped, err := tmuxserver.EncodeLayout(layout.Tiled, map[string]gotmux.PaneID{"left": replacements[0].ID(), "right": replacements[1].ID()})
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
	for _, border := range []gotmux.PaneBorderStatus{"off", "top", "bottom"} {
		t.Run(string(border), func(t *testing.T) {
			server := tmuxtest.NewServer(t)

			session, window, panes := layoutFixture(t, server, "borders", 29)
			if _, err := panes[0].Split(t.Context(), gotmux.SplitOptions{Direction: gotmux.DirectionVertical, Size: gotmux.SplitSize{Cells: 11}, Program: gotmux.Exec("sleep", "300")}); err != nil {
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

func assertCaptureGeometry(t *testing.T, server *gotmux.Server, session gotmux.Session, window gotmux.Window) {
	t.Helper()
	want := observePanes(t, window)

	result, err := capture.Observe(t.Context(), server, []gotmux.SessionID{session.ID()})
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

func layoutFixture(t *testing.T, server *gotmux.Server, name string, rightWidth int) (gotmux.Session, gotmux.Window, []gotmux.Pane) {
	t.Helper()

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{Name: name, Program: gotmux.Exec("sleep", "300"), Size: gotmux.Size{Width: 121, Height: 41}, Start: gotmux.StartPolicyExistingOnly})
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

	second, err := first.Split(t.Context(), gotmux.SplitOptions{Direction: gotmux.DirectionHorizontal, Size: gotmux.SplitSize{Cells: rightWidth}, Program: gotmux.Exec("sleep", "300")})
	if err != nil {
		t.Fatal(err)
	}

	return session, window, []gotmux.Pane{first, second}
}

func TestCaptureSharedWindowsAndGroupClosure(t *testing.T) {
	server := tmuxtest.NewServer(t)
	first, window, _ := layoutFixture(t, server, "group-first", 29)

	second, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{Name: "group-second", Group: "group-first", Start: gotmux.StartPolicyExistingOnly})
	if err != nil {
		t.Fatal(err)
	}

	third, _, _ := layoutFixture(t, server, "linked-third", 29)

	index := 9
	if _, err := window.Link(t.Context(), third, gotmux.LinkOptions{Index: &index}); err != nil {
		t.Fatal(err)
	}

	assertCapturedClosure(t, server, first, second, third)
}

type observedPane struct {
	Index  int
	Width  int
	Height int
	Left   int
	Top    int
	Active bool
}

func observePanes(t *testing.T, window gotmux.Window) []observedPane {
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
