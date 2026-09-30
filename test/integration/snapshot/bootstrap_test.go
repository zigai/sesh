//go:build integration

package snapshot_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"
)

func TestBootstrapRelocationPreservesSparseWindowSlots(t *testing.T) {
	for _, base := range []int{0, 1, 5} {
		t.Run(strconv.Itoa(base), func(t *testing.T) {
			server := tmuxtest.NewServer(t)

			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()

			if err := server.GlobalSessionOptions().SetBaseIndex(ctx, base); err != nil {
				t.Fatal(err)
			}

			session, err := server.NewSession(ctx, tmux.NewSessionOptions{
				Name: "bootstrap", Program: tmux.Exec("sleep", "300"), Start: tmux.StartPolicyExistingOnly,
			})
			if err != nil {
				t.Fatal(err)
			}

			_, links, err := session.WindowInfos(ctx)
			if err != nil || len(links) != 1 || links[0].Index != base {
				t.Fatalf("initial links: %v, %v", links, err)
			}

			bootstrapID := links[0].WindowID
			reserved := 7

			moved, err := links[0].Handle().Move(ctx, session, tmux.LinkOptions{Index: &reserved})
			if err != nil {
				t.Fatal(err)
			}

			want := createSparseWindows(t, ctx, session)

			if err = moved.UnlinkWith(ctx, tmux.UnlinkOptions{Force: true}); err != nil {
				t.Fatal(err)
			}

			assertSparseWindows(t, ctx, session, bootstrapID, want)
		})
	}
}

func createSparseWindows(t *testing.T, ctx context.Context, session tmux.Session) map[int]tmux.WindowID {
	t.Helper()

	want := make(map[int]tmux.WindowID)

	for _, index := range []int{0, 1, 5} {
		link, err := session.NewWindow(ctx, tmux.NewWindowOptions{
			Index: &index, Program: tmux.Exec("sleep", "300"),
		})
		if err != nil {
			t.Fatal(err)
		}

		want[index] = link.Window().ID()
	}

	return want
}

func assertSparseWindows(t *testing.T, ctx context.Context, session tmux.Session, bootstrap tmux.WindowID, want map[int]tmux.WindowID) {
	t.Helper()

	_, links, err := session.WindowInfos(ctx)
	if err != nil || len(links) != len(want) {
		t.Fatalf("final links: %v, %v", links, err)
	}

	for _, link := range links {
		if link.WindowID == bootstrap || link.WindowID != want[link.Index] {
			t.Fatalf("unexpected slot after bootstrap removal: %v", link)
		}
	}
}
