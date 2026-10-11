//go:build (linux || darwin) && integration

package remote_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/remote"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

type peerHarness struct {
	server     *gotmux.Server
	options    remote.PeerActionOptions
	repository string
}

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

// newPeerHarness runs invocations the way `sesh remote peer action` does: through this
// machine's own action runner against a disposable tmux server.
func newPeerHarness(t *testing.T) peerHarness {
	t.Helper()

	server := tmuxtest.NewServer(t)
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")

	historyDir := filepath.Join(root, "history")
	if err := os.Mkdir(historyDir, 0o700); err != nil {
		t.Fatal(err)
	}

	repository := app.NewRepository(repositoryPath)

	t.Cleanup(func() { _ = repository.Close() })

	local := app.NewLocal(app.LocalOptions{
		Config:             config.DefaultConfig(),
		Server:             server,
		Repository:         repository,
		LeaseRoot:          filepath.Join(root, "leases"),
		HistoryPath:        filepath.Join(historyDir, "history.json"),
		AgentSource:        agents.NewAHTStore(filepath.Join(root, "agents")),
		Streams:            gotmux.Streams{In: nil, Out: nil, Err: nil},
		Source:             "remote",
		DeferSwitchHistory: false,
		TaskLedger:         "",
		WorktreeDir:        "",
	})

	return peerHarness{
		server: server,
		options: remote.PeerActionOptions{
			PeerID:     "peer",
			Receipts:   openReceipts(t),
			Allowed:    func(action.ID) bool { return true },
			Dispatcher: local,
			Now:        nil,
		},
		repository: repositoryPath,
	}
}

func (harness peerHarness) request(t *testing.T, id action.ID, input any) remote.ActionRequest {
	t.Helper()

	entry, ok := action.Lookup(id)
	if !ok {
		t.Fatalf("action %s is not registered", id)
	}

	encoded, err := entry.EncodeInput(input)
	if err != nil {
		t.Fatal(err)
	}

	invocationID, err := remote.NewInvocationID()
	if err != nil {
		t.Fatal(err)
	}

	request := remote.ActionRequest{
		InvocationID: invocationID,
		IntentDigest: "",
		Action:       id,
		ConnectionID: "work",
		ConfigDigest: "digest",
		PeerID:       "peer",
		Socket:       harness.server.Endpoint().SocketPath,
		Input:        encoded,
	}

	request.IntentDigest, err = remote.IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	return request
}

func (harness peerHarness) run(t *testing.T, ctx context.Context, id action.ID, input any) remote.ActionResult {
	t.Helper()

	return remote.ExecutePeerAction(ctx, harness.options, harness.request(t, id, input))
}

func (harness peerHarness) liveRef(t *testing.T, ctx context.Context, name string) action.SessionRef {
	t.Helper()

	rows, err := session.ListLive(ctx, harness.server, "")
	if err != nil {
		t.Fatal(err)
	}

	return action.RefOf(findSession(t, rows, name))
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

func killSession(t *testing.T, ctx context.Context, server *gotmux.Server, id string) {
	t.Helper()

	handle, err := server.Session(ctx, gotmux.SessionID(id))
	if err != nil {
		t.Fatal(err)
	}

	if err := handle.Kill(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPeerActionAgainstDisposableServer(t *testing.T) {
	harness := newPeerHarness(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	renamed := createAndRenameThroughPeer(t, ctx, harness)
	refuseStaleKill(t, ctx, harness, renamed)
	killAndReplay(t, ctx, harness, renamed)
}

func createAndRenameThroughPeer(t *testing.T, ctx context.Context, harness peerHarness) action.SessionRef {
	t.Helper()

	created := harness.run(t, ctx, action.SessionCreate.ID, action.CreateSession{Name: "peer-target", Directory: t.TempDir()})
	if !created.OK {
		t.Fatalf("create result = %#v, want success", created)
	}

	output, err := action.SessionCreate.DecodeOutput(created.Output)
	if createdSession, ok := output.(action.CreatedSession); err != nil || !ok || createdSession.Session.Name != "peer-target" {
		t.Fatalf("create output = %s, %v; want the created session", created.Output, err)
	}

	target := harness.liveRef(t, ctx, "peer-target")
	if result := harness.run(t, ctx, action.SessionRename.ID, action.RenameSession{Session: target, Name: "peer-renamed"}); !result.OK {
		t.Fatalf("rename result = %#v, want success", result)
	}

	return harness.liveRef(t, ctx, "peer-renamed")
}

func refuseStaleKill(t *testing.T, ctx context.Context, harness peerHarness, target action.SessionRef) {
	t.Helper()

	stale := target
	stale.ServerPID++

	if result := harness.run(t, ctx, action.SessionKill.ID, action.KillSession{Session: stale, Force: false}); result.Code != action.CodeTargetChanged {
		t.Fatalf("stale kill result = %#v, want %s", result, action.CodeTargetChanged)
	}

	if _, err := harness.server.Session(ctx, gotmux.SessionID(target.ID)); err != nil {
		t.Fatalf("stale target mutation reached tmux: %v", err)
	}
}

func killAndReplay(t *testing.T, ctx context.Context, harness peerHarness, target action.SessionRef) {
	t.Helper()

	kill := harness.request(t, action.SessionKill.ID, action.KillSession{Session: target, Force: false})

	first := remote.ExecutePeerAction(ctx, harness.options, kill)
	if !first.OK {
		t.Fatalf("kill result = %#v, want success", first)
	}

	if _, err := harness.server.Session(ctx, gotmux.SessionID(target.ID)); err == nil {
		t.Fatal("killed session is still present")
	}

	replayed := remote.ExecutePeerAction(ctx, harness.options, kill)
	if !replayed.OK || replayed.InvocationID != first.InvocationID || string(replayed.Output) != string(first.Output) {
		t.Fatalf("replayed result = %#v, want the recorded outcome", replayed)
	}
}

func TestPeerDeleteRefusesSameNameReplacement(t *testing.T) {
	harness := newPeerHarness(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	if _, err := harness.server.NewSession(ctx, gotmux.NewSessionOptions{Name: "replacement-target", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	original := harness.liveRef(t, ctx, "replacement-target")
	killSession(t, ctx, harness.server, original.ID)

	if _, err := harness.server.NewSession(ctx, gotmux.NewSessionOptions{Name: "replacement-target", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	replacement := harness.liveRef(t, ctx, "replacement-target")

	result := harness.run(t, ctx, action.SessionDelete.ID, action.DeleteSession{Session: original, Force: false})
	if result.Code != action.CodeTargetChanged {
		t.Fatalf("delete result = %#v, want %s", result, action.CodeTargetChanged)
	}

	if _, err := harness.server.Session(ctx, gotmux.SessionID(replacement.ID)); err != nil {
		t.Fatalf("replacement session was deleted: %v", err)
	}
}

func TestPeerSavedDeleteRefusesToKillALiveNamesake(t *testing.T) {
	harness := newPeerHarness(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	if _, err := harness.server.NewSession(ctx, gotmux.NewSessionOptions{Name: "saved-delete-target", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	live := harness.liveRef(t, ctx, "saved-delete-target")
	generation, savedID := commitSavedDeleteFixture(t, ctx, harness.repository, harness.server.Endpoint().SocketPath, live.Name)

	saved := action.SessionRef{
		Kind: session.KindSaved, ID: savedID, Name: live.Name, Socket: "", ServerPID: 0,
		ServerStartedAt: time.Time{}, CreatedAt: time.Time{}, Generation: generation,
	}

	result := harness.run(t, ctx, action.SessionDelete.ID, action.DeleteSession{Session: saved, Force: false})
	if result.Code != action.CodeTargetChanged {
		t.Fatalf("saved delete result = %#v, want %s", result, action.CodeTargetChanged)
	}

	if _, err := harness.server.Session(ctx, gotmux.SessionID(live.ID)); err != nil {
		t.Fatalf("saved-only deletion killed the live session: %v", err)
	}
}

func TestPeerAttachValidationAgainstDisposableServer(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	live := remote.NewGotmuxLiveServer(server)

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "attach-target", Dir: t.TempDir()}); err != nil {
		t.Fatalf("NewSession() error = %v", err)
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

	killSession(t, ctx, server, target.ID)

	fresh, err := live.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := remote.ValidateAttachTarget(attachTarget, fresh); !errors.Is(err, remote.ErrTargetChanged) {
		t.Fatalf("ValidateAttachTarget(fresh inventory) error = %v, want %v", err, remote.ErrTargetChanged)
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
