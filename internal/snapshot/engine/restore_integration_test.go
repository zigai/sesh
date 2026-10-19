//go:build (linux || darwin) && integration

package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
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
		Start:   gotmux.StartPolicyExistingOnly,
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
		LeaseRoot:         leaseRoot,
		Generation:        saveRes.Generation,
		Sessions:          nil,
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirLaunch,
	})
	if err != nil {
		oopsErr, _ := oops.AsOops(err)
		t.Fatalf("Restore failed: %+v, context: %v", err, oopsErr.Context())
	}

	if len(restoreRes.SessionsRestored) == 0 {
		t.Fatal("expected at least 1 session restored")
	}

	if slices.Contains(restoreRes.MissingRequirements, unsupportedOptionsNote) {
		t.Fatalf("a snapshot from the same tmux reported unsupported options: %v", restoreRes.MissingRequirements)
	}

	assertSessionRestoredAs(t, ctx, server, "roundtrip-alpha")
}

func assertSessionRestoredAs(t *testing.T, ctx context.Context, server *gotmux.Server, name string) {
	t.Helper()

	restoredSess, err := server.FindSession(ctx, name)
	if err != nil {
		t.Fatalf("FindSession for restored session failed: %v", err)
	}

	info, err := restoredSess.Info(ctx)
	if err != nil {
		t.Fatalf("restoredSess.Info failed: %v", err)
	}

	if info.Name != name {
		t.Fatalf("expected session name %s, got %q", name, info.Name)
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
		Start:   gotmux.StartPolicyExistingOnly,
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
		LeaseRoot:         leaseRoot,
		Generation:        saveRes.Generation,
		Sessions:          nil,
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirLaunch,
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

func TestEngineRestore_LeavesOutOptionsTheTargetDoesNotSupport(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	live := sessionWithLocalOptions(t, ctx, server, "retired-options")

	observation, err := capture.Observe(ctx, server, []gotmux.SessionID{live.ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	addRetiredOptions(t, &observation.Manifest)

	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitCapture(t, ctx, repoDir, observation)

	if err := live.Kill(ctx); err != nil {
		t.Fatalf("Kill session failed: %v", err)
	}

	repo := openFixture(t, ctx, repoDir)

	result, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:         filepath.Join(t.TempDir(), "leases"),
		Generation:        generation,
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirLaunch,
		Profile:           engine.RecoveryPractical,
	})
	if err != nil {
		oopsErr, _ := oops.AsOops(err)
		t.Fatalf("Restore failed: %+v, context: %v", err, oopsErr.Context())
	}

	if result.Status != "best_effort" || !result.Verified {
		t.Fatalf("restore status = %q verified = %v, want best_effort and verified", result.Status, result.Verified)
	}

	if !slices.Contains(result.MissingRequirements, unsupportedOptionsNote) {
		t.Fatalf("missing requirements %v lack the unsupported option note", result.MissingRequirements)
	}

	assertOperationSettled(t, ctx, repo, result.OperationID)
	assertLocalOptionsRestored(t, ctx, server, "retired-options")
}

func sessionWithLocalOptions(t *testing.T, ctx context.Context, server *gotmux.Server, name string) gotmux.Session {
	t.Helper()

	live, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    name,
		Program: gotmux.Exec("sleep", "300"),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableWindowRename(t, ctx, live)

	windows, err := live.Windows(ctx)
	if err != nil {
		t.Fatal(err)
	}

	window := windows[0].Window().Options()

	if err := live.Options().Set(ctx, "status-left", "kept-left"); err != nil {
		t.Fatal(err)
	}

	if err := live.Options().SetUser(ctx, "@project", "kept-project"); err != nil {
		t.Fatal(err)
	}

	if err := window.Set(ctx, "main-pane-width", "77"); err != nil {
		t.Fatal(err)
	}

	if err := window.SetUser(ctx, "@task", "kept-task"); err != nil {
		t.Fatal(err)
	}

	return live
}

func addRetiredOptions(t *testing.T, manifest *model.Manifest) {
	t.Helper()

	manifest.Sessions[0].Options = append(manifest.Sessions[0].Options, model.Option{
		Scope: "session", Name: model.Bytes("legacy-session-style"), Value: model.Bytes("default"), Present: true, Inherited: false,
	})
	manifest.Windows[0].Options = append(manifest.Windows[0].Options, model.Option{
		Scope: "window", Name: model.Bytes("legacy-window-style"), Value: model.Bytes("default"), Present: true, Inherited: false,
	})

	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest validation failed: %v", err)
	}
}

func assertOperationSettled(t *testing.T, ctx context.Context, repo *store.Repository, operationID string) {
	t.Helper()

	catalog, err := repo.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, op := range catalog.Operations {
		if op.ID == operationID && op.State == "active" {
			t.Fatal("restore operation is still active")
		}
	}
}

func assertLocalOptionsRestored(t *testing.T, ctx context.Context, server *gotmux.Server, name string) {
	t.Helper()

	restored, err := server.FindSession(ctx, name)
	if err != nil {
		t.Fatalf("FindSession failed: %v", err)
	}

	assertLocalOption(t, ctx, restored.Options(), "status-left", "kept-left")
	assertLocalOption(t, ctx, restored.Options(), "@project", "kept-project")

	windows, err := restored.Windows(ctx)
	if err != nil {
		t.Fatal(err)
	}

	window := windows[0].Window().Options()
	assertLocalOption(t, ctx, window, "main-pane-width", "77")
	assertLocalOption(t, ctx, window, "@task", "kept-task")
}

var unsupportedOptionsNote = restore.MissingRequirement{
	Domain: "options", Status: "partial", Reason: model.ReasonTargetOptionUnsupported,
}

type optionReader interface {
	Get(ctx context.Context, name string) (gotmux.OptionValue[string], error)
}

func assertLocalOption(t *testing.T, ctx context.Context, scope optionReader, name, want string) {
	t.Helper()

	value, err := scope.Get(ctx, name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}

	if got, ok := value.Local.Get(); !ok || got != want {
		t.Fatalf("%s = %q (set %v), want %q", name, got, ok, want)
	}
}

type recordingOverrides struct {
	script    string
	marker    string
	committed []string
	launched  bool
	unchanged bool
}

func TestEngineRestoreCommitsPlannedPanesWithoutLaunchOverrides(t *testing.T) {
	server := tmuxtest.NewServer(t)
	dir := t.TempDir()
	repo := openFixture(t, t.Context(), filepath.Join(dir, "repo"))
	leaseRoot := filepath.Join(dir, "leases")
	restored := startSleepingSession(t, t.Context(), server, "state-restored")
	kept := startSleepingSession(t, t.Context(), server, "state-kept")

	saved, err := engine.SaveObservation(t.Context(), repo, server, []gotmux.SessionID{restored.ID(), kept.ID()}, engine.SaveConfig{
		LeaseRoot: leaseRoot, ProcessEnrichment: true, Profile: engine.RecoveryPractical,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := restored.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	overrides := &recordingOverrides{unchanged: true}

	result, err := engine.Restore(t.Context(), repo, server, engine.RestoreConfig{
		LeaseRoot: leaseRoot, Generation: saved.Generation, LaunchProcesses: true,
		Profile: engine.RecoveryPractical, Launches: overrides,
	})
	if err != nil || !result.Verified {
		t.Fatalf("Restore() = %+v, %v; want a verified restore", result, err)
	}

	if len(result.SessionsSkipped) != 1 || len(result.Panes) != 1 {
		t.Fatalf("restore result = %+v, want one session skipped and one pane restored", result)
	}

	if !slices.Equal(overrides.committed, []string{result.Panes[0].PaneID}) {
		t.Fatalf("committed panes = %v, want only restored pane %s", overrides.committed, result.Panes[0].PaneID)
	}
}

func TestEngineRestoreLaunchesOverriddenPanesAndCommitsOnlyPlannedOnesFirst(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	dir := t.TempDir()
	repo := openFixture(t, ctx, filepath.Join(dir, "repo"))
	leaseRoot := filepath.Join(dir, "leases")

	restored := startSleepingSession(t, ctx, server, "override-restored")
	kept := startSleepingSession(t, ctx, server, "override-kept")

	saved, err := engine.SaveObservation(ctx, repo, server, []gotmux.SessionID{restored.ID(), kept.ID()}, engine.SaveConfig{
		LeaseRoot: leaseRoot, ProcessEnrichment: true, Profile: engine.RecoveryPractical,
	})
	if err != nil {
		t.Fatalf("SaveObservation failed: %v", err)
	}

	if err := restored.Kill(ctx); err != nil {
		t.Fatal(err)
	}

	overrides := &recordingOverrides{script: writeMarkerScript(t, dir), marker: filepath.Join(dir, "marker")}

	result, err := engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot: leaseRoot, Generation: saved.Generation, LaunchProcesses: true,
		Profile: engine.RecoveryPractical, Launches: overrides,
	})
	if err != nil || !result.Verified {
		t.Fatalf("Restore() = %+v, %v; want a verified restore", result, err)
	}

	if len(result.SessionsSkipped) != 1 || len(result.Panes) != 1 {
		t.Fatalf("restore result = %+v, want the live session skipped and one pane restored", result)
	}

	if !slices.Equal(overrides.committed, []string{result.Panes[0].PaneID}) {
		t.Fatalf("committed panes = %v, want only the restored pane %s", overrides.committed, result.Panes[0].PaneID)
	}

	if overrides.launched {
		t.Fatal("overrides committed after a pane was launched")
	}

	waitForMarker(t, overrides.marker)
}

func (o *recordingOverrides) Override(_ context.Context, manifest model.Manifest) (map[string]model.Launch, error) {
	launches := make(map[string]model.Launch, len(manifest.Panes))
	if o.unchanged {
		return launches, nil
	}

	for _, pane := range manifest.Panes {
		launches[pane.ID] = model.Launch{
			Executable:  model.Bytes(o.script),
			Arguments:   []model.Bytes{model.Bytes("marker"), model.Bytes(o.marker)},
			Directory:   pane.Directory,
			Environment: nil,
			Birth:       nil,
			Recovery:    model.RecoveryRestart,
			Reason:      "",
		}
	}

	return launches, nil
}

func (o *recordingOverrides) Commit(_ context.Context, panes []string) error {
	o.committed = append(o.committed, panes...)
	_, err := os.Stat(o.marker)
	o.launched = err == nil

	return nil
}

func startSleepingSession(t *testing.T, ctx context.Context, server *gotmux.Server, name string) gotmux.Session {
	t.Helper()

	session, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name: name, Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableWindowRename(t, ctx, session)

	return session
}

func writeMarkerScript(t *testing.T, dir string) string {
	t.Helper()

	script := filepath.Join(dir, "marker.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho launched > \"$1\"\nexec sleep 300\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}

	return script
}

func waitForMarker(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("overridden launch did not run")
}
