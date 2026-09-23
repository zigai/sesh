//go:build (linux || darwin) && integration

package remote_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/remote"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func openReceipts(t *testing.T) *privatefs.Dir {
	t.Helper()

	path := filepath.Join(t.TempDir(), "receipts")

	dir, err := privatefs.Ensure(path)
	if err != nil {
		t.Fatalf("opening receipt directory: %v", err)
	}

	t.Cleanup(func() { _ = dir.Close() })

	return dir
}

func liveActionRequest(t *testing.T, target session.Session, operation remote.Action, newName string) remote.ActionRequest {
	t.Helper()

	request := remote.ActionRequest{
		Operation:           operation,
		ConnectionID:        "work",
		ConfigDigest:        "digest",
		PeerID:              "peer",
		Kind:                string(session.KindLive),
		TmuxServerSocket:    target.TmuxServerSocket,
		TmuxServerPID:       target.RuntimeIdentity.TmuxServerPID,
		TmuxServerStartedAt: target.RuntimeIdentity.TmuxServerStartedAt,
		SessionID:           target.ID,
		SessionName:         target.Name,
		SessionCreatedAt:    target.RuntimeIdentity.TmuxSessionCreatedAt,
		NewName:             newName,
	}

	id, err := remote.NewInvocationID()
	if err != nil {
		t.Fatal(err)
	}

	request.InvocationID = id

	digest, err := remote.IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	return request
}

func findSession(t *testing.T, sessions []session.Session, name string) session.Session {
	t.Helper()

	for _, candidate := range sessions {
		if candidate.Name == name {
			return candidate
		}
	}

	t.Fatalf("session %q is absent from the observed inventory %#v", name, sessions)

	return session.Session{}
}

func TestGotmuxLiveServerSessionLifecycle(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	live := remote.NewGotmuxLiveServer(server)

	directory := t.TempDir()

	if err := live.Create(ctx, "peer-created", directory); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	sessions, err := live.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions() error = %v", err)
	}

	created := findSession(t, sessions, "peer-created")
	assertCreatedSession(t, ctx, server, created)

	if err := live.Rename(ctx, created.ID, "peer-renamed"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	assertRenamedSession(t, ctx, server, created.ID)

	if err := live.Kill(ctx, created.ID); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}

	if _, err := server.Session(ctx, gotmux.SessionID(created.ID)); err == nil {
		t.Fatal("killed session is still present")
	}
}

func assertCreatedSession(t *testing.T, ctx context.Context, server *gotmux.Server, created session.Session) {
	t.Helper()

	if created.RuntimeIdentity.TmuxServerPID <= 0 || created.RuntimeIdentity.TmuxSessionCreatedAt.IsZero() {
		t.Fatalf("observed session = %#v, want a tmux incarnation", created)
	}

	observed, err := server.Session(ctx, gotmux.SessionID(created.ID))
	if err != nil {
		t.Fatalf("gotmux lookup error = %v", err)
	}

	info, err := observed.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if info.Name != "peer-created" {
		t.Fatalf("tmux session name = %q, want peer-created", info.Name)
	}
}

func assertRenamedSession(t *testing.T, ctx context.Context, server *gotmux.Server, sessionID string) {
	t.Helper()

	renamed, err := server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		t.Fatal(err)
	}

	renamedInfo, err := renamed.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if renamedInfo.Name != "peer-renamed" {
		t.Fatalf("renamed session = %q, want peer-renamed", renamedInfo.Name)
	}
}

func TestPeerActionAgainstDisposableServer(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	live := remote.NewGotmuxLiveServer(server)

	options := remote.PeerActionOptions{
		PeerID:   "peer",
		Receipts: openReceipts(t),
		Policy:   remote.PeerPolicy{Create: true, Rename: true, Kill: true, Delete: false, Snapshot: false, Restore: false, Sockets: nil},
		Live:     live,
		Saved:    nil,
		Now:      nil,
	}

	renamed := assertCreateAndRenameTarget(t, ctx, options, live, t.TempDir())
	assertStaleTargetRefused(t, ctx, options, server, renamed)
	assertKillAndReplayOutcome(t, ctx, options, server, renamed)
}

func assertCreateAndRenameTarget(t *testing.T, ctx context.Context, options remote.PeerActionOptions, live remote.LiveServer, directory string) session.Session {
	t.Helper()

	create := remote.ActionRequest{
		InvocationID:        "",
		IntentDigest:        "",
		Operation:           remote.ActionCreate,
		ConnectionID:        "work",
		ConfigDigest:        "digest",
		PeerID:              "peer",
		Kind:                "",
		TmuxServerSocket:    "",
		TmuxServerPID:       0,
		TmuxServerStartedAt: time.Time{},
		SessionID:           "",
		SessionName:         "peer-target",
		SessionCreatedAt:    time.Time{},
		NewName:             "",
		Directory:           directory,
	}

	id, err := remote.NewInvocationID()
	if err != nil {
		t.Fatal(err)
	}

	create.InvocationID = id

	digest, err := remote.IntentDigest(create)
	if err != nil {
		t.Fatal(err)
	}

	create.IntentDigest = digest

	if result := remote.ExecutePeerAction(ctx, options, create); !result.OK {
		t.Fatalf("create result = %#v, want success", result)
	}

	sessions, err := live.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	target := findSession(t, sessions, "peer-target")
	rename := liveActionRequest(t, target, remote.ActionRename, "peer-target-renamed")

	if result := remote.ExecutePeerAction(ctx, options, rename); !result.OK {
		t.Fatalf("rename result = %#v, want success", result)
	}

	sessions, err = live.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	return findSession(t, sessions, "peer-target-renamed")
}

func assertStaleTargetRefused(t *testing.T, ctx context.Context, options remote.PeerActionOptions, server *gotmux.Server, renamed session.Session) {
	t.Helper()

	stale := liveActionRequest(t, renamed, remote.ActionKill, "")
	stale.TmuxServerPID++

	digest, err := remote.IntentDigest(stale)
	if err != nil {
		t.Fatal(err)
	}

	stale.IntentDigest = digest

	if result := remote.ExecutePeerAction(ctx, options, stale); result.Code != remote.ActionCodeTargetChanged {
		t.Fatalf("stale kill result = %#v, want %s", result, remote.ActionCodeTargetChanged)
	}

	if _, err := server.Session(ctx, gotmux.SessionID(renamed.ID)); err != nil {
		t.Fatalf("stale target mutation reached tmux: %v", err)
	}
}

func assertKillAndReplayOutcome(t *testing.T, ctx context.Context, options remote.PeerActionOptions, server *gotmux.Server, renamed session.Session) {
	t.Helper()

	kill := liveActionRequest(t, renamed, remote.ActionKill, "")

	first := remote.ExecutePeerAction(ctx, options, kill)
	if !first.OK {
		t.Fatalf("kill result = %#v, want success", first)
	}

	if _, err := server.Session(ctx, gotmux.SessionID(renamed.ID)); err == nil {
		t.Fatal("killed session is still present")
	}

	replayed := remote.ExecutePeerAction(ctx, options, kill)
	if !replayed.OK || replayed.InvocationID != first.InvocationID {
		t.Fatalf("replayed result = %#v, want the recorded outcome", replayed)
	}
}

func TestPeerAttachValidationAgainstDisposableServer(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	live := remote.NewGotmuxLiveServer(server)

	if err := live.Create(ctx, "attach-target", t.TempDir()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	sessions, err := live.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	target := findSession(t, sessions, "attach-target")

	attachTarget := remote.AttachTarget{
		PeerID:              "peer",
		TmuxServerSocket:    target.TmuxServerSocket,
		TmuxServerPID:       target.RuntimeIdentity.TmuxServerPID,
		TmuxServerStartedAt: target.RuntimeIdentity.TmuxServerStartedAt,
		SessionID:           target.ID,
		SessionName:         target.Name,
		SessionCreatedAt:    target.RuntimeIdentity.TmuxSessionCreatedAt,
		PaneID:              "",
		CatalogSequence:     1,
	}

	if _, err := remote.ValidateAttachTarget(attachTarget, sessions); err != nil {
		t.Fatalf("ValidateAttachTarget() error = %v", err)
	}

	if target.ActivePaneID != "" {
		owner, err := live.PaneSession(ctx, target.ActivePaneID)
		if err != nil {
			t.Fatalf("PaneSession() error = %v", err)
		}

		if owner != target.ID {
			t.Fatalf("pane owner = %q, want %q", owner, target.ID)
		}
	}

	if err := live.Kill(ctx, target.ID); err != nil {
		t.Fatal(err)
	}

	fresh, err := live.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := remote.ValidateAttachTarget(attachTarget, fresh); !errors.Is(err, remote.ErrTargetChanged) {
		t.Fatalf("ValidateAttachTarget(fresh inventory) error = %v, want %v", err, remote.ErrTargetChanged)
	}
}

func TestPeerPolicyAgainstDisposableServerRefusesUnpermittedMutations(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	live := remote.NewGotmuxLiveServer(server)

	if err := live.Create(ctx, "policy-target", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	sessions, err := live.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	target := findSession(t, sessions, "policy-target")

	options := remote.PeerActionOptions{
		PeerID:   "peer",
		Receipts: openReceipts(t),
		Policy:   remote.PeerPolicy{Sockets: []string{"/tmp/other.sock"}},
		Live:     live,
	}

	kill := liveActionRequest(t, target, remote.ActionKill, "")

	if result := remote.ExecutePeerAction(ctx, options, kill); result.Code != remote.ActionCodeForbidden {
		t.Fatalf("kill result = %#v, want %s", result, remote.ActionCodeForbidden)
	}

	if _, err := server.Session(ctx, gotmux.SessionID(target.ID)); err != nil {
		t.Fatalf("refused mutation reached tmux: %v", err)
	}
}

func TestNativeCompoundDeleteRefusesSameNameReplacement(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)

	defer cancel()

	live := remote.NewGotmuxLiveServer(server)
	if err := live.Create(ctx, "replacement-target", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	original := findSession(t, mustLiveSessions(t, ctx, live), "replacement-target")
	if err := live.Kill(ctx, original.ID); err != nil {
		t.Fatal(err)
	}

	if err := live.Create(ctx, "replacement-target", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	replacement := findSession(t, mustLiveSessions(t, ctx, live), "replacement-target")

	leaseRoot := filepath.Join(t.TempDir(), "leases")

	leaseDir, err := privatefs.Ensure(leaseRoot)
	if err != nil {
		t.Fatal(err)
	}

	if err := leaseDir.Close(); err != nil {
		t.Fatal(err)
	}

	service, err := remote.NewNativeSavedSessions(remote.NativeSavedConfig{
		Repository: filepath.Join(t.TempDir(), "missing-repository"),
		LeaseRoot:  leaseRoot,
		Server:     server,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.DeleteLiveAndSaved(ctx, original, live); !errors.Is(err, session.ErrSessionReplaced) {
		t.Fatalf("DeleteLiveAndSaved() error = %v, want ErrSessionReplaced", err)
	}

	if _, err := server.Session(ctx, gotmux.SessionID(replacement.ID)); err != nil {
		t.Fatalf("replacement session was deleted: %v", err)
	}
}

func TestNativeRestoreRefusesGenerationFromAnotherEndpoint(t *testing.T) {
	sourceServer := tmuxtest.NewServer(t)
	targetServer := tmuxtest.NewServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)

	defer cancel()

	sourceLive := remote.NewGotmuxLiveServer(sourceServer)
	if err := sourceLive.Create(ctx, "scoped-saved-target", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	savedTarget := findSession(t, mustLiveSessions(t, ctx, sourceLive), "scoped-saved-target")

	leaseRoot := filepath.Join(t.TempDir(), "leases")

	leaseDir, err := privatefs.Ensure(leaseRoot)
	if err != nil {
		t.Fatal(err)
	}

	if err := leaseDir.Close(); err != nil {
		t.Fatal(err)
	}

	repository := filepath.Join(t.TempDir(), "repository")

	sourceService, err := remote.NewNativeSavedSessions(remote.NativeSavedConfig{
		Repository: repository,
		LeaseRoot:  leaseRoot,
		Server:     sourceServer,
	})
	if err != nil {
		t.Fatal(err)
	}

	generation, _ := sourceService.Snapshot(ctx, sourceServer.Endpoint().SocketPath)
	if generation == "" {
		t.Fatal("snapshot did not preserve its durable generation ID")
	}

	targetService, err := remote.NewNativeSavedSessions(remote.NativeSavedConfig{
		Repository: repository,
		LeaseRoot:  leaseRoot,
		Server:     targetServer,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := targetService.Restore(ctx, targetServer.Endpoint().SocketPath, generation, savedTarget.ID); !errors.Is(err, remote.ErrTargetChanged) {
		t.Fatalf("Restore() error = %v, want ErrTargetChanged", err)
	}
}

func TestNativeSavedDeleteReturnsRewriteReceiptWithoutKillingLive(t *testing.T) {
	server := tmuxtest.NewServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)

	defer cancel()

	live := remote.NewGotmuxLiveServer(server)
	if err := live.Create(ctx, "saved-delete-target", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	target := findSession(t, mustLiveSessions(t, ctx, live), "saved-delete-target")

	leaseRoot := filepath.Join(t.TempDir(), "leases")

	leaseDir, err := privatefs.Ensure(leaseRoot)
	if err != nil {
		t.Fatal(err)
	}

	if err := leaseDir.Close(); err != nil {
		t.Fatal(err)
	}

	repository := filepath.Join(t.TempDir(), "repository")

	service, err := remote.NewNativeSavedSessions(remote.NativeSavedConfig{
		Repository: repository, LeaseRoot: leaseRoot, Server: server,
	})
	if err != nil {
		t.Fatal(err)
	}

	generation, savedID := commitSavedDeleteFixture(t, ctx, repository, server.Endpoint().SocketPath, target.Name)

	rewriteID, err := service.Delete(ctx, generation, savedID)
	if err != nil || rewriteID == "" {
		t.Fatalf("Delete() = %q, %v, want durable rewrite receipt", rewriteID, err)
	}

	if _, err := server.Session(ctx, gotmux.SessionID(target.ID)); err != nil {
		t.Fatalf("saved-only deletion killed the live session: %v", err)
	}
}

func commitSavedDeleteFixture(
	t *testing.T,
	ctx context.Context,
	repository, socket, name string,
) (string, string) {
	t.Helper()

	repo, err := store.Open(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()

	attempt, err := repo.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}

	endpointScope, err := model.EndpointScope(socket)
	if err != nil {
		t.Fatal(err)
	}

	const sessionID = "saved-session"

	manifest := model.Manifest{
		Format: "sesh", Version: model.Schema, Started: 1, Finished: 2,
		Source: model.Source{
			TmuxVersion: "3.7c", LibraryVersion: "v0.3.0", Consistency: "observed", EndpointScope: endpointScope,
		},
		Sessions: []model.Session{{ID: sessionID, Name: model.Bytes(name), Directory: model.Bytes("/tmp")}},
		Windows:  []model.Window{{ID: "saved-window", Name: model.Bytes("window"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "saved-pane"}}},
		Panes: []model.Pane{{
			ID: "saved-pane", Window: "saved-window", Directory: model.Bytes("/tmp"), Width: 80, Height: 24,
			Active: true, Launch: model.Launch{Recovery: "unverified"},
		}},
		Links:     []model.Link{{Session: sessionID, Window: "saved-window", Index: 0, Active: true}},
		Artifacts: nil, Evidence: savedDeleteEvidence(),
	}

	generation, err := repo.Commit(ctx, attempt.ID, manifest, nil)
	if err != nil {
		t.Fatal(err)
	}

	return generation.ID, sessionID
}

func savedDeleteEvidence() []model.Evidence {
	evidence := make([]model.Evidence, 0, 10)
	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		evidence = append(evidence, model.Evidence{Domain: domain, Status: "unverified", Reason: "not_captured"})
	}

	return evidence
}

func mustLiveSessions(t *testing.T, ctx context.Context, live *remote.GotmuxLiveServer) []session.Session {
	t.Helper()

	rows, err := live.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	return rows
}
