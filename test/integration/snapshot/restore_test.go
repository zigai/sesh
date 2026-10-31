//go:build (linux || darwin) && integration

package snapshot_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func setupTestMultiWindowSession(t *testing.T, ctx context.Context, server *gotmux.Server) gotmux.Session {
	t.Helper()

	sess, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "project-alpha",
		Dir:     "",
		Window:  "editor",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 120, Height: 40},
		Start:   gotmux.StartPolicyExistingOnly,
		Group:   "",
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableServerWindowRename(t, ctx, server)

	windows, _, err := sess.WindowInfos(ctx)
	if err != nil {
		t.Fatal(err)
	}

	win1 := windows[0].Handle()

	activePane, err := win1.ActivePane(ctx)
	if err != nil {
		t.Fatalf("ActivePane failed: %v", err)
	}

	if err := activePane.SetTitle(ctx, "left-pane"); err != nil {
		t.Fatalf("SetTitle failed: %v", err)
	}

	splitPane, err := activePane.Split(ctx, gotmux.SplitOptions{
		Direction:           gotmux.DirectionHorizontal,
		Size:                gotmux.SplitSize{Cells: 40, Percent: 0},
		Dir:                 "",
		Program:             gotmux.Exec("sleep", "300"),
		Env:                 nil,
		TmuxEnv:             nil,
		Select:              false,
		Before:              false,
		FullSize:            false,
		KillTarget:          false,
		Zoom:                false,
		Title:               "",
		BorderLines:         "",
		Style:               "",
		ActiveBorderStyle:   "",
		InactiveBorderStyle: "",
		Message:             "",
	})
	if err != nil {
		t.Fatalf("Split failed: %v", err)
	}

	if err := splitPane.SetTitle(ctx, "right-pane"); err != nil {
		t.Fatalf("SetTitle on split failed: %v", err)
	}

	_, err = sess.NewWindow(ctx, gotmux.NewWindowOptions{
		Name:    "term",
		Dir:     "",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		TmuxEnv: nil,
		Index:   nil,
		Before:  false,
		After:   false,
		Select:  false,
	})
	if err != nil {
		t.Fatalf("NewWindow failed: %v", err)
	}

	disableServerWindowRename(t, ctx, server)

	return sess
}

func verifyRestoredState(t *testing.T, ctx context.Context, server *gotmux.Server) {
	t.Helper()

	restoredSess, err := server.FindSession(ctx, "project-alpha")
	if err != nil {
		t.Fatalf("FindSession project-alpha failed: %v", err)
	}

	restoredLinks, err := restoredSess.Windows(ctx)
	if err != nil {
		t.Fatalf("restoredSess.Windows failed: %v", err)
	}

	if len(restoredLinks) != 2 {
		t.Fatalf("expected 2 windows in restored session, got %d", len(restoredLinks))
	}

	windowNames := make(map[string]bool)

	for _, w := range restoredLinks {
		info, err := w.Info(ctx)
		if err != nil {
			t.Fatalf("w.Info failed: %v", err)
		}

		windowNames[info.WindowName] = true
	}

	if !windowNames["editor"] || !windowNames["term"] {
		t.Fatalf("missing expected window names (editor, term), got %v", windowNames)
	}
}

func verifyOperationInCatalog(t *testing.T, ctx context.Context, repo *store.Repository, opID string) {
	t.Helper()

	catalog, err := repo.Inspect(ctx)
	if err != nil {
		t.Fatalf("repo.Inspect failed: %v", err)
	}

	foundOp := false

	for _, op := range catalog.Operations {
		if op.ID == opID {
			foundOp = true

			if op.State != "active" {
				t.Fatalf("expected partial restore operation to stay active, got %q", op.State)
			}

			break
		}
	}

	if !foundOp {
		t.Fatalf("operation %s not found in catalog", opID)
	}
}

func TestSnapshotRestore_MultiWindowFidelity(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	repoDir := filepath.Join(t.TempDir(), "repo")

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = repo.Close()
	}()

	leaseRoot := filepath.Join(t.TempDir(), "leases")
	sess := setupTestMultiWindowSession(t, ctx, server)

	saveRes, err := engine.SaveObservation(ctx, repo, server, []gotmux.SessionID{sess.ID()}, engine.SaveConfig{
		LeaseRoot:              leaseRoot,
		ProcessEnrichment:      false,
		ProcessEnvironmentKeys: nil,
	})
	if err != nil {
		t.Fatalf("SaveObservation failed: %v", err)
	}

	if saveRes.Generation == "" {
		t.Fatal("expected non-empty generation")
	}

	if err := sess.Kill(ctx); err != nil {
		t.Fatalf("Kill session failed: %v", err)
	}

	restoreRes, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        saveRes.Generation,
		Sessions:          nil,
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirLaunch,
	})
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if len(restoreRes.SessionsRestored) != 1 {
		t.Fatalf("expected 1 restored session, got %d", len(restoreRes.SessionsRestored))
	}

	verifyRestoredState(t, ctx, server)
	verifyOperationInCatalog(t, ctx, repo, restoreRes.OperationID)

	_, err = engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        saveRes.Generation,
		Sessions:          nil,
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirLaunch,
	})
	if err == nil {
		t.Fatal("expected collision error on repeat restore, got nil")
	}

	if !errors.Is(err, restore.ErrSessionCollision) {
		t.Fatalf("expected ErrSessionCollision, got %v", err)
	}
}

func TestSnapshotRestoreReleasesWindowSizePinnedByResize(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	repo, err := store.Open(ctx, filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = repo.Close()
	}()

	leaseRoot := filepath.Join(t.TempDir(), "leases")
	sess := setupTestMultiWindowSession(t, ctx, server)

	windows, _, err := sess.WindowInfos(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// resize-window pins the window to window-size manual, as an earlier restore leaves it.
	if err := windows[0].Handle().Resize(ctx, gotmux.Size{Width: 150, Height: 50}); err != nil {
		t.Fatal(err)
	}

	pinned, err := windows[0].Handle().Options().WindowSize(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if local, _ := pinned.Local.Get(); local != gotmux.WindowSizeManual {
		t.Fatalf("source window-size = %q, want the fixture pinned to manual", local)
	}

	saveRes, err := engine.SaveObservation(ctx, repo, server, []gotmux.SessionID{sess.ID()}, engine.SaveConfig{
		LeaseRoot:              leaseRoot,
		ProcessEnrichment:      false,
		ProcessEnvironmentKeys: nil,
	})
	if err != nil {
		t.Fatalf("SaveObservation failed: %v", err)
	}

	if err := sess.Kill(ctx); err != nil {
		t.Fatalf("Kill session failed: %v", err)
	}

	if _, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        saveRes.Generation,
		Sessions:          nil,
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirLaunch,
	}); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	global, err := server.GlobalWindowOptions().WindowSize(ctx)
	if err != nil {
		t.Fatal(err)
	}

	want, _ := global.Effective.Get()

	restored, err := server.FindSession(ctx, "project-alpha")
	if err != nil {
		t.Fatal(err)
	}

	restoredWindows, _, err := restored.WindowInfos(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, window := range restoredWindows {
		policy, err := window.Handle().Options().WindowSize(ctx)
		if err != nil {
			t.Fatal(err)
		}

		if got, _ := policy.Effective.Get(); got != want {
			t.Fatalf("restored window %s window-size = %q, want global %q", window.Handle().ID(), got, want)
		}
	}
}

func TestSnapshotRestoreKeepsLastActivePane(t *testing.T) {
	for _, last := range []string{"left-pane", "bottom-pane"} {
		t.Run(last, func(t *testing.T) {
			server := tmuxtest.NewServer(t)

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			repo, err := store.Open(ctx, filepath.Join(t.TempDir(), "repo"))
			if err != nil {
				t.Fatal(err)
			}

			defer func() {
				_ = repo.Close()
			}()

			leaseRoot := filepath.Join(t.TempDir(), "leases")
			sess := setupTestMultiWindowSession(t, ctx, server)
			editor := findPaneWindow(t, ctx, sess, "left-pane")
			left := findTitledPane(t, ctx, editor, "left-pane")

			bottom, err := left.Split(ctx, gotmux.SplitOptions{Direction: gotmux.DirectionVertical, Program: gotmux.Exec("sleep", "300")})
			if err != nil {
				t.Fatalf("Split failed: %v", err)
			}

			if err := bottom.SetTitle(ctx, "bottom-pane"); err != nil {
				t.Fatal(err)
			}

			for _, title := range []string{"right-pane", last, "right-pane"} {
				if err := findTitledPane(t, ctx, editor, title).Select(ctx); err != nil {
					t.Fatal(err)
				}
			}

			saveRes, err := engine.SaveObservation(ctx, repo, server, []gotmux.SessionID{sess.ID()}, engine.SaveConfig{
				LeaseRoot:              leaseRoot,
				ProcessEnrichment:      false,
				ProcessEnvironmentKeys: nil,
			})
			if err != nil {
				t.Fatalf("SaveObservation failed: %v", err)
			}

			if err := sess.Kill(ctx); err != nil {
				t.Fatalf("Kill session failed: %v", err)
			}

			if _, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
				LeaseRoot:         leaseRoot,
				Generation:        saveRes.Generation,
				Sessions:          nil,
				LaunchProcesses:   false,
				MissingWorkingDir: restore.MissingWorkingDirLaunch,
			}); err != nil {
				t.Fatalf("Restore failed: %v", err)
			}

			restored, err := server.FindSession(ctx, "project-alpha")
			if err != nil {
				t.Fatal(err)
			}

			window := findPaneWindow(t, ctx, restored, "left-pane")
			if got := activePaneTitle(t, ctx, window); got != "right-pane" {
				t.Fatalf("restored active pane = %q, want right-pane", got)
			}

			command, err := gotmux.NewCommand("select-pane", "-l", "-t", string(window.ID()))
			if err != nil {
				t.Fatal(err)
			}

			if _, err := server.Run(ctx, command); err != nil {
				t.Fatalf("select-pane -l failed: %v", err)
			}

			if got := activePaneTitle(t, ctx, window); got != last {
				t.Fatalf("select-pane -l went to %q, want %q", got, last)
			}
		})
	}
}

func findPaneWindow(t *testing.T, ctx context.Context, sess gotmux.Session, title string) gotmux.Window {
	t.Helper()

	windows, _, err := sess.WindowInfos(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, window := range windows {
		panes, err := window.Handle().Panes(ctx)
		if err != nil {
			t.Fatal(err)
		}

		for _, pane := range panes {
			if pane.Title == title {
				return window.Handle()
			}
		}
	}

	t.Fatalf("no window holds pane %q", title)

	return gotmux.Window{}
}

func findTitledPane(t *testing.T, ctx context.Context, window gotmux.Window, title string) gotmux.Pane {
	t.Helper()

	panes, err := window.Panes(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, pane := range panes {
		if pane.Title == title {
			return pane.Handle()
		}
	}

	t.Fatalf("no pane titled %q", title)

	return gotmux.Pane{}
}

func activePaneTitle(t *testing.T, ctx context.Context, window gotmux.Window) string {
	t.Helper()

	pane, err := window.ActivePane(ctx)
	if err != nil {
		t.Fatal(err)
	}

	info, err := pane.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	return info.Title
}

func disableServerWindowRename(t *testing.T, ctx context.Context, server *gotmux.Server) {
	t.Helper()

	windows, err := server.Windows(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, w := range windows {
		opts := w.Handle().Options()
		_ = opts.SetAutomaticRename(ctx, false)
		_ = opts.SetAllowRename(ctx, false)
	}
}
