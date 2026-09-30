//go:build (linux || darwin) && integration

package tmux_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	itmux "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func startCliServer(t *testing.T) string {
	t.Helper()

	socketPath := filepath.Join(t.TempDir(), "cli.sock")
	t.Cleanup(func() { killCliServer(t, socketPath) })

	for _, args := range [][]string{
		{"new-session", "-d", "-s", "cli"},
		{"new-window", "-t", "cli", "-n", "editor"},
		{"split-window", "-t", "cli:1", "-h"},
	} {
		command := exec.CommandContext(t.Context(), "tmux", append([]string{"-S", socketPath}, args...)...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}

		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}

	return socketPath
}

// killCliServer stops the server started through the tmux CLI before its socket directory is
// removed, which would otherwise leave the daemon running without a reachable socket.
func killCliServer(t *testing.T, socketPath string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server, err := gotmux.New(gotmux.Config{SocketPath: socketPath})
	if err != nil {
		t.Errorf("cleanup open cli server: %v", err)
		return
	}

	info, err := server.Probe(ctx)
	if errors.Is(err, gotmux.ErrNoServer) {
		return
	}

	if err == nil {
		err = server.KillMatching(ctx, info.Identity)
	}

	if err != nil && !errors.Is(err, gotmux.ErrNoServer) {
		t.Errorf("cleanup kill cli server: %v", err)
	}
}

func paneReferences(snapshot gotmux.Snapshot) map[string]string {
	references := make(map[string]string, len(snapshot.Panes()))

	for index, info := range snapshot.Panes() {
		references[string(info.ID)] = "p-" + string(rune('a'+index))
	}

	return references
}

func TestCliServerLayoutsDecode(t *testing.T) {
	server, err := gotmux.New(gotmux.Config{SocketPath: startCliServer(t)})
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()

	before, err := server.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	references := paneReferences(before)

	if len(before.Windows()) != 2 {
		t.Fatalf("windows = %d, want 2", len(before.Windows()))
	}

	leafs, branches := 0, 0

	for _, info := range before.Windows() {
		node, err := itmux.DecodeLayout(info.Layout, references)
		if err != nil {
			t.Fatalf("window %s layout %q failed to decode: %v", info.ID, info.Layout, err)
		}

		assertRootGeometry(t, node, info)

		if node.Pane != "" {
			leafs++

			continue
		}

		branches++

		assertTilingSplit(t, node, info)
	}

	if leafs != 1 || branches != 1 {
		t.Fatalf("decoded %d single-pane and %d split windows, want 1 and 1", leafs, branches)
	}
}

func assertRootGeometry(t *testing.T, node model.Layout, info gotmux.WindowInfo) {
	t.Helper()

	if node.Width == info.Width && node.Height == info.Height && node.Left == 0 && node.Top == 0 {
		return
	}

	t.Fatalf("window %s root geometry = %dx%d at %d,%d, want %dx%d at 0,0",
		info.ID, node.Width, node.Height, node.Left, node.Top, info.Width, info.Height)
}

func assertTilingSplit(t *testing.T, node model.Layout, info gotmux.WindowInfo) {
	t.Helper()

	if node.Axis != "x" {
		t.Fatalf("split axis = %q, want x for a horizontal split", node.Axis)
	}

	if len(node.Children) != info.PaneCount {
		t.Fatalf("split children = %d, want observed pane count %d", len(node.Children), info.PaneCount)
	}

	next := 0

	for index, child := range node.Children {
		if child.Width <= 0 || child.Height <= 0 {
			t.Fatalf("child %d has non-positive geometry %dx%d", index, child.Width, child.Height)
		}

		if child.Pane == "" {
			t.Fatalf("child %d must be a pane leaf", index)
		}

		if child.Top != 0 || child.Height != node.Height {
			t.Fatalf("child %d = %dx%d at %d,%d, want full-height column", index, child.Width, child.Height, child.Left, child.Top)
		}

		if child.Left != next {
			t.Fatalf("child %d left = %d, want %d (contiguous tiling)", index, child.Left, next)
		}

		next = child.Left + child.Width + 1
	}

	if next-1 != node.Width {
		t.Fatalf("children plus dividers span %d cells, want window width %d", next-1, node.Width)
	}
}
