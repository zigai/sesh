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
		Start:   gotmux.ExistingOnly,
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
		Direction:           gotmux.Horizontal,
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
		LeaseRoot:              leaseRoot,
		Generation:             saveRes.Generation,
		Sessions:               nil,
		LaunchProcesses:        false,
		AllowMissingWorkingDir: true,
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
		LeaseRoot:              leaseRoot,
		Generation:             saveRes.Generation,
		Sessions:               nil,
		LaunchProcesses:        false,
		AllowMissingWorkingDir: true,
	})
	if err == nil {
		t.Fatal("expected collision error on repeat restore, got nil")
	}

	if !errors.Is(err, restore.ErrSessionCollision) {
		t.Fatalf("expected ErrSessionCollision, got %v", err)
	}
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
