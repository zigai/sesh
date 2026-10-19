//go:build (linux || darwin) && integration

package engine_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/restore"
)

type collisionFixture struct {
	server     *gotmux.Server
	repoDir    string
	leaseRoot  string
	generation string
	ids        map[string]string
	live       map[string]gotmux.Session
}

func newCollisionFixture(t *testing.T, ctx context.Context) collisionFixture {
	t.Helper()

	server := tmuxtest.NewServer(t)
	live := map[string]gotmux.Session{}

	for _, name := range []string{"collision-kept", "collision-taken"} {
		session, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: name, Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly})
		if err != nil {
			t.Fatalf("NewSession %s failed: %v", name, err)
		}

		disableSourceWindowRename(t, ctx, session)

		live[name] = session
	}

	observation, err := capture.Observe(ctx, server, []gotmux.SessionID{live["collision-kept"].ID(), live["collision-taken"].ID()})
	if err != nil {
		t.Fatalf("capture.Observe failed: %v", err)
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	generation := commitCapture(t, ctx, repoDir, observation)

	ids := map[string]string{}
	for _, session := range observation.Manifest.Sessions {
		ids[string(session.Name)] = session.ID
	}

	if err := live["collision-kept"].Kill(ctx); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	return collisionFixture{
		server:     server,
		repoDir:    repoDir,
		leaseRoot:  filepath.Join(t.TempDir(), "leases"),
		generation: generation,
		ids:        ids,
		live:       live,
	}
}

func (f collisionFixture) config(sessions []string, policy engine.CollisionPolicy) engine.RestoreConfig {
	return engine.RestoreConfig{
		LeaseRoot:         f.leaseRoot,
		Generation:        f.generation,
		Sessions:          sessions,
		OperationID:       "",
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirLaunch,
		OnCollision:       policy,
		Profile:           engine.RecoveryPractical,
	}
}

func requireLiveSessionUntouched(t *testing.T, ctx context.Context, f collisionFixture, name string) {
	t.Helper()

	found, err := f.server.FindSession(ctx, name)
	if err != nil {
		t.Fatalf("live session %s must survive the restore: %v", name, err)
	}

	if found.ID() != f.live[name].ID() {
		t.Fatalf("live session %s was replaced: %v vs %v", name, found.ID(), f.live[name].ID())
	}
}

func TestEngineRestoreSkipsCollidingSessionAndRestoresTheRest(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	fixture := newCollisionFixture(t, ctx)
	repo := openFixture(t, ctx, fixture.repoDir)

	result, err := engine.Restore(ctx, repo, fixture.server, fixture.config(nil, engine.CollisionPolicyDefault))
	if err != nil {
		t.Fatalf("restoring around a collision must succeed: %v", err)
	}

	if result.Status != "partial" || result.Reason != "sessions_skipped" {
		t.Fatalf("expected partial/sessions_skipped, got %s/%s", result.Status, result.Reason)
	}

	if !result.Verified {
		t.Fatalf("the sessions that were restored must still verify: %+v", result)
	}

	if len(result.SessionsSkipped) != 1 || result.SessionsSkipped[0].Name != "collision-taken" ||
		result.SessionsSkipped[0].ID != fixture.ids["collision-taken"] || result.SessionsSkipped[0].Reason != restore.CollisionNameInUse {
		t.Fatalf("expected the colliding session to be reported by name and id, got %+v", result.SessionsSkipped)
	}

	if _, err := fixture.server.FindSession(ctx, "collision-kept"); err != nil {
		t.Fatalf("the non-colliding session must be restored: %v", err)
	}

	requireLiveSessionUntouched(t, ctx, fixture, "collision-taken")
}

func TestEngineRestoreNamesCollidingSessionWhenSessionsAreExplicit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	fixture := newCollisionFixture(t, ctx)
	repo := openFixture(t, ctx, fixture.repoDir)

	result, err := engine.Restore(ctx, repo, fixture.server, fixture.config(
		[]string{fixture.ids["collision-kept"], fixture.ids["collision-taken"]}, engine.CollisionPolicyDefault,
	))
	if !errors.Is(err, restore.ErrSessionCollision) {
		t.Fatalf("explicit selection with a collision must fail, got %v", err)
	}

	if len(result.SessionsSkipped) != 1 || result.SessionsSkipped[0].Name != "collision-taken" {
		t.Fatalf("the colliding session must be named, got %+v", result.SessionsSkipped)
	}

	if result.OperationID != "" {
		t.Fatalf("a refused plan must not leave a pending operation, got %q", result.OperationID)
	}

	if _, err := fixture.server.FindSession(ctx, "collision-kept"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("a refused restore must not create any session, got %v", err)
	}

	requireLiveSessionUntouched(t, ctx, fixture, "collision-taken")
}

func TestEngineRestoreRefusesEverythingWhenCollisionPolicyIsFail(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	fixture := newCollisionFixture(t, ctx)
	repo := openFixture(t, ctx, fixture.repoDir)

	result, err := engine.Restore(ctx, repo, fixture.server, fixture.config(nil, engine.CollisionPolicyFail))
	if !errors.Is(err, restore.ErrSessionCollision) {
		t.Fatalf("fail policy must refuse the restore, got %v", err)
	}

	if len(result.SessionsSkipped) != 1 || result.SessionsSkipped[0].Name != "collision-taken" {
		t.Fatalf("the colliding session must be named, got %+v", result.SessionsSkipped)
	}

	if _, err := fixture.server.FindSession(ctx, "collision-kept"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("fail policy must not restore the other sessions, got %v", err)
	}
}

func TestEngineRestoreExplainsMissingServerBeforeTakingALease(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	fixture := newCollisionFixture(t, ctx)
	repo := openFixture(t, ctx, fixture.repoDir)

	dead, _ := newAbsentServer(t)

	result, err := engine.Restore(ctx, repo, dead, fixture.config(nil, engine.CollisionPolicyDefault))
	if !errors.Is(err, engine.ErrNoServerRunning) {
		t.Fatalf("expected the no-server explanation, got %v", err)
	}

	if result.Reason != "no_server" {
		t.Fatalf("expected the fixed no_server reason, got %q", result.Reason)
	}

	if _, statErr := os.Stat(fixture.leaseRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a missing server must be refused before any lease is taken, stat: %v", statErr)
	}
}

func TestEngineRestoreLogsSkippedSessionsOnceAsPartialWithoutNames(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	fixture := newCollisionFixture(t, ctx)
	repo := openFixture(t, ctx, fixture.repoDir)

	var buffer bytes.Buffer

	logCtx := zerolog.New(&buffer).WithContext(ctx)

	if _, err := engine.Restore(logCtx, repo, fixture.server, fixture.config(nil, engine.CollisionPolicyDefault)); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	records := decodeLogRecords(t, buffer.Bytes())
	if len(records) != 2 {
		t.Fatalf("expected one start and one terminal record, got %d: %s", len(records), buffer.String())
	}

	terminal := records[1]
	if terminal["level"] != "warn" || terminal["status"] != "partial" || terminal["reason"] != "sessions_skipped" || terminal["skipped"] != float64(1) {
		t.Fatalf("a restore that skipped sessions must be one warn record with a fixed reason and a count: %+v", terminal)
	}

	for _, private := range []string{"collision-kept", "collision-taken", fixture.repoDir, fixture.leaseRoot, fixture.ids["collision-taken"]} {
		if bytes.Contains(buffer.Bytes(), []byte(private)) {
			t.Fatalf("restore logs must not contain names, ids or paths (%q): %s", private, buffer.String())
		}
	}
}

func TestEngineRestoreLogsMissingServerAsOneFixedReasonFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	fixture := newCollisionFixture(t, ctx)
	repo := openFixture(t, ctx, fixture.repoDir)

	dead, socketDir := newAbsentServer(t)

	var buffer bytes.Buffer

	logCtx := zerolog.New(&buffer).WithContext(ctx)

	if _, err := engine.Restore(logCtx, repo, dead, fixture.config(nil, engine.CollisionPolicyDefault)); err == nil {
		t.Fatal("expected a missing server to fail")
	}

	records := decodeLogRecords(t, buffer.Bytes())
	if len(records) != 2 {
		t.Fatalf("expected one start and one failure record, got %d: %s", len(records), buffer.String())
	}

	failure := records[1]
	if failure["level"] != "error" || failure["status"] != "failed" || failure["phase"] != "admission" || failure["reason"] != "no_server" {
		t.Fatalf("expected one error record with phase admission and reason no_server: %+v", failure)
	}

	if bytes.Contains(buffer.Bytes(), []byte(socketDir)) {
		t.Fatalf("the failure log must not contain the socket path: %s", buffer.String())
	}
}

func newAbsentServer(t *testing.T) (*gotmux.Server, string) {
	t.Helper()

	socketDir := t.TempDir()

	server, err := gotmux.New(gotmux.Config{
		SocketPath: filepath.Join(socketDir, "absent.sock"),
		ConfigFile: "/dev/null",
		Env:        []string{"PATH=" + os.Getenv("PATH"), "HOME=" + socketDir},
		Dir:        socketDir,
	})
	if err != nil {
		t.Fatalf("gotmux.New failed: %v", err)
	}

	return server, socketDir
}
