//go:build (linux || darwin) && integration

package engine_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestEngineRestore_FullRoundtrip(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
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

	liveSess, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "roundtrip-alpha",
		Program: gotmux.Exec("sleep", "300"),
		Start:   gotmux.ExistingOnly,
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableWindowRename(t, ctx, liveSess)

	saveRes, err := engine.SaveObservation(ctx, repo, server, []gotmux.SessionID{liveSess.ID()}, engine.SaveConfig{
		LeaseRoot:              leaseRoot,
		ProcessEnrichment:      false,
		ProcessEnvironmentKeys: nil,
	})
	if err != nil {
		t.Fatalf("SaveObservation failed: %v", err)
	}

	if saveRes.Generation == "" {
		t.Fatal("expected non-empty generation from save")
	}

	if err := liveSess.Kill(ctx); err != nil {
		t.Fatalf("Kill session failed: %v", err)
	}

	if _, err := server.FindSession(ctx, "roundtrip-alpha"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for killed session, got %v", err)
	}

	restoreRes, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:              leaseRoot,
		Generation:             saveRes.Generation,
		Sessions:               nil,
		LaunchProcesses:        false,
		AllowMissingWorkingDir: true,
	})
	if err != nil {
		oopsErr, _ := oops.AsOops(err)
		t.Fatalf("Restore failed: %+v, context: %v", err, oopsErr.Context())
	}

	if len(restoreRes.SessionsRestored) == 0 {
		t.Fatal("expected at least 1 session restored")
	}

	restoredSess, err := server.FindSession(ctx, "roundtrip-alpha")
	if err != nil {
		t.Fatalf("FindSession for restored session failed: %v", err)
	}

	info, err := restoredSess.Info(ctx)
	if err != nil {
		t.Fatalf("restoredSess.Info failed: %v", err)
	}

	if info.Name != "roundtrip-alpha" {
		t.Fatalf("expected session name roundtrip-alpha, got %q", info.Name)
	}
}

func TestEngineRestore_SessionCollision(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
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

	liveSess, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "collision-session",
		Program: gotmux.Exec("sleep", "300"),
		Start:   gotmux.ExistingOnly,
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableWindowRename(t, ctx, liveSess)

	saveRes, err := engine.SaveObservation(ctx, repo, server, []gotmux.SessionID{liveSess.ID()}, engine.SaveConfig{
		LeaseRoot:              leaseRoot,
		ProcessEnrichment:      false,
		ProcessEnvironmentKeys: nil,
	})
	if err != nil {
		t.Fatalf("SaveObservation failed: %v", err)
	}

	_, err = engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:              leaseRoot,
		Generation:             saveRes.Generation,
		Sessions:               nil,
		LaunchProcesses:        false,
		AllowMissingWorkingDir: true,
	})
	if err == nil {
		t.Fatal("expected error for collision, got nil")
	}

	if !errors.Is(err, restore.ErrSessionCollision) {
		t.Fatalf("expected ErrSessionCollision, got %v", err)
	}

	found, err := server.FindSession(ctx, "collision-session")
	if err != nil {
		t.Fatalf("original session must still exist: %v", err)
	}

	if found.ID() != liveSess.ID() {
		t.Fatalf("session ID altered: %v vs %v", found.ID(), liveSess.ID())
	}
}

func disableWindowRename(t *testing.T, ctx context.Context, sess gotmux.Session) {
	t.Helper()

	windows, err := sess.Windows(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, w := range windows {
		opts := w.Window().Options()
		if err := opts.SetAutomaticRename(ctx, false); err != nil {
			t.Fatal(err)
		}

		if err := opts.SetAllowRename(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
}
