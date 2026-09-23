//go:build integration && (linux || darwin)

package snapshot_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	tmuxintegration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestSaveUsesExplicitServerAndExactSession(t *testing.T) {
	source, other := tmuxtest.NewServer(t), tmuxtest.NewServer(t)

	dir := filepath.Join(t.TempDir(), "space ' tab\t newline\n directory")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	selected, pane := saveSessionFixture(t, source, "1", dir, "SELECTED_MARKER")
	_, neighbor := saveSessionFixture(t, source, "10", dir, "NEIGHBOR_MARKER")
	_, foreign := saveSessionFixture(t, other, "1", dir, "FOREIGN_MARKER")

	neighborInfo, err := neighbor.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	foreignInfo, err := foreign.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", other.Endpoint().SocketPath+",1,0")
	r := openSaveRepository(t)
	cfg := engine.SaveConfig{
		LeaseRoot: filepath.Join(t.TempDir(), "leases"),
	}

	result, err := engine.SaveObservation(t.Context(), r, source, []tmux.SessionID{selected.ID()}, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != "partial" {
		t.Fatal("observation claimed full parity")
	}

	saved, err := r.Read(t.Context(), result.Generation)
	if err != nil {
		t.Fatal(err)
	}

	assertSavedSelection(t, saved, pane, dir)
	assertPaneUnchanged(t, neighbor, neighborInfo)
	assertPaneUnchanged(t, foreign, foreignInfo)
}

func assertSavedSelection(t *testing.T, saved model.Manifest, pane tmux.Pane, dir string) {
	t.Helper()

	if len(saved.Sessions) != 1 || string(saved.Sessions[0].Name) != "1" || len(saved.Panes) != 1 {
		t.Fatal("wrong session selected")
	}

	live, err := pane.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if string(saved.Panes[0].Directory) != live.CurrentPath || live.CurrentPath != dir {
		t.Fatal("directory bytes changed")
	}
}

func saveSessionFixture(t *testing.T, server *tmux.Server, name, dir, marker string) (tmux.Session, tmux.Pane) {
	t.Helper()

	session, err := server.NewSession(t.Context(), tmux.NewSessionOptions{Name: name, Dir: dir, Program: tmux.Exec("/bin/sh", "-c", `printf '%s' "$1"; exec sleep 300`, "sh", marker), Start: tmux.ExistingOnly, Size: tmux.Size{Width: 100, Height: 35}})
	if err != nil {
		t.Fatal(err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := windows[0].Handle().Options().SetAutomaticRename(t.Context(), false); err != nil {
		t.Fatal(err)
	}

	pane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	waitForCapture(t, pane, []byte(marker))

	return session, pane
}

func openSaveRepository(t *testing.T) *store.Repository {
	t.Helper()

	r, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "repository"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})

	return r
}

func assertPaneUnchanged(t *testing.T, pane tmux.Pane, before tmux.PaneInfo) {
	t.Helper()

	after, err := pane.Info(t.Context())
	if err != nil {
		t.Fatal("unrelated pane disappeared", err)
	}

	if before.ID != after.ID || before.PID != after.PID || before.Title != after.Title || before.CurrentPath != after.CurrentPath {
		t.Fatal("save changed unrelated work")
	}
}

func TestSaveLeaseContentionLeavesRepositoryUnchanged(t *testing.T) {
	server := tmuxtest.NewServer(t)
	dir := t.TempDir()
	selected, _ := saveSessionFixture(t, server, "lease-test", dir, "LEASE_MARKER")

	leaseRoot := filepath.Join(t.TempDir(), "shared-leases")
	r1 := openSaveRepository(t)
	r2 := openSaveRepository(t)
	cfg := engine.SaveConfig{LeaseRoot: leaseRoot}

	res1, err := engine.SaveObservation(t.Context(), r1, server, []tmux.SessionID{selected.ID()}, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if res1.Status != "partial" {
		t.Fatalf("unexpected status: %s", res1.Status)
	}

	heldLease, err := tmuxintegration.AcquireLease(t.Context(), leaseRoot, server.Endpoint().SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	timeoutCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, saveErr := engine.SaveObservation(timeoutCtx, r2, server, []tmux.SessionID{selected.ID()}, cfg)
	if !errors.Is(saveErr, context.DeadlineExceeded) {
		t.Fatalf("expected save to fail with deadline exceeded, got: %v", saveErr)
	}
	catalog, err := r2.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Attempts) != 0 {
		t.Fatalf("expected 0 attempts in contended repository, got %d", len(catalog.Attempts))
	}

	if err := heldLease.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := engine.SaveObservation(t.Context(), r2, server, []tmux.SessionID{selected.ID()}, cfg)
	if err != nil {
		t.Fatalf("expected save to succeed after lease release: %v", err)
	}

	if result.Status != "partial" {
		t.Fatalf("unexpected status: %s", result.Status)
	}
}

func TestSaveDistinctEndpointsProceedConcurrently(t *testing.T) {
	server1 := tmuxtest.NewServer(t)
	server2 := tmuxtest.NewServer(t)
	dir := t.TempDir()
	sess1, _ := saveSessionFixture(t, server1, "s1", dir, "S1_MARKER")
	sess2, _ := saveSessionFixture(t, server2, "s2", dir, "S2_MARKER")

	leaseRoot := filepath.Join(t.TempDir(), "shared-leases")
	r1 := openSaveRepository(t)
	r2 := openSaveRepository(t)

	cfg := engine.SaveConfig{LeaseRoot: leaseRoot}

	var g sync.WaitGroup

	errs := make([]error, 2)

	g.Add(2)
	go func() {
		defer g.Done()

		_, errs[0] = engine.SaveObservation(t.Context(), r1, server1, []tmux.SessionID{sess1.ID()}, cfg)
	}()
	go func() {
		defer g.Done()

		_, errs[1] = engine.SaveObservation(t.Context(), r2, server2, []tmux.SessionID{sess2.ID()}, cfg)
	}()

	g.Wait()

	if errs[0] != nil {
		t.Fatalf("server1 save failed: %v", errs[0])
	}

	if errs[1] != nil {
		t.Fatalf("server2 save failed: %v", errs[1])
	}
}

func TestSaveCanceledAdmissionLeavesRepositoryUnchanged(t *testing.T) {
	server := tmuxtest.NewServer(t)
	dir := t.TempDir()
	selected, _ := saveSessionFixture(t, server, "cancel-test", dir, "CANCEL_MARKER")

	leaseRoot := filepath.Join(t.TempDir(), "shared-leases")
	r := openSaveRepository(t)

	heldLease, err := tmuxintegration.AcquireLease(t.Context(), leaseRoot, server.Endpoint().SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = heldLease.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	cfg := engine.SaveConfig{LeaseRoot: leaseRoot}

	_, err = engine.SaveObservation(ctx, r, server, []tmux.SessionID{selected.ID()}, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Attempts) != 0 {
		t.Fatalf("expected 0 attempts, got %d", len(catalog.Attempts))
	}
}

func TestSaveLeaseReleasedOnCaptureFailure(t *testing.T) {
	server := tmuxtest.NewServer(t)
	dir := t.TempDir()
	selected, _ := saveSessionFixture(t, server, "fail-test", dir, "FAIL_MARKER")

	leaseRoot := filepath.Join(t.TempDir(), "shared-leases")
	r := openSaveRepository(t)
	cfg := engine.SaveConfig{LeaseRoot: leaseRoot}

	invalidSession := tmux.SessionID("$999999")

	_, err := engine.SaveObservation(t.Context(), r, server, []tmux.SessionID{invalidSession}, cfg)
	if err == nil {
		t.Fatal("expected save to fail for non-existent session")
	}

	result, err := engine.SaveObservation(t.Context(), r, server, []tmux.SessionID{selected.ID()}, cfg)
	if err != nil {
		t.Fatalf("expected save to succeed after capture failure release: %v", err)
	}

	if result.Status != "partial" {
		t.Fatalf("unexpected status: %s", result.Status)
	}
}
