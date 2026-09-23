//go:build linux || darwin

package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/session"
)

var (
	errTestCompoundFailure = errors.New("compound failure")
	errTestTmuxCreate      = errors.New("tmux returned error after create")
	errTestTmuxRename      = errors.New("tmux returned error after rename")
	errTestTmuxKill        = errors.New("tmux returned error after kill")
)

type fakeLiveServer struct {
	mu          sync.Mutex
	sessions    []session.Session
	sessionsErr error
	createErr   error
	renameErr   error
	killErr     error
	attachErr   error
	paneOwners  map[string]string
	created     []string
	renamed     []string
	killed      []string
	attached    []string
	createHook  func()
	renameHook  func()
	killHook    func()
}

func (server *fakeLiveServer) Sessions(context.Context) ([]session.Session, error) {
	server.mu.Lock()
	defer server.mu.Unlock()

	return append([]session.Session(nil), server.sessions...), server.sessionsErr
}

func (server *fakeLiveServer) Create(_ context.Context, name, directory string) error {
	server.mu.Lock()
	server.created = append(server.created, name+"|"+directory)
	hook := server.createHook
	server.mu.Unlock()

	if hook != nil {
		hook()
	}

	return server.createErr
}

func (server *fakeLiveServer) Rename(_ context.Context, sessionID, name string) error {
	server.mu.Lock()
	server.renamed = append(server.renamed, sessionID+"|"+name)
	hook := server.renameHook
	server.mu.Unlock()

	if hook != nil {
		hook()
	}

	return server.renameErr
}

func (server *fakeLiveServer) Kill(_ context.Context, sessionID string) error {
	server.mu.Lock()

	server.killed = append(server.killed, sessionID)
	hook := server.killHook

	server.mu.Unlock()

	if hook != nil {
		hook()
	}

	return server.killErr
}

func (server *fakeLiveServer) Attach(_ context.Context, sessionID string, _ AttachStreams) error {
	server.mu.Lock()
	defer server.mu.Unlock()

	server.attached = append(server.attached, sessionID)

	return server.attachErr
}

func (server *fakeLiveServer) PaneSession(_ context.Context, paneID string) (string, error) {
	server.mu.Lock()
	defer server.mu.Unlock()

	owner, ok := server.paneOwners[paneID]
	if !ok {
		return "", ErrTargetChanged
	}

	return owner, nil
}

func (server *fakeLiveServer) counts() (int, int, int) {
	server.mu.Lock()
	defer server.mu.Unlock()

	return len(server.created), len(server.killed), len(server.attached)
}

type fakeCompoundDeleter struct {
	fakeSavedSessions

	deleteLiveAndSavedErr error
	deletedLive           []string
}

func (f *fakeCompoundDeleter) DeleteLiveAndSaved(_ context.Context, target session.Session, _ LiveServer) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deletedLive = append(f.deletedLive, target.ID)

	return f.operationID, f.deleteLiveAndSavedErr
}

type fakeSavedSessions struct {
	mu          sync.Mutex
	deleteErr   error
	snapshotErr error
	restoreErr  error
	operationID string
	deleted     []string
	snapshotted []string
	restored    []string
}

func (saved *fakeSavedSessions) Delete(_ context.Context, generation, sessionID string) (string, error) {
	saved.mu.Lock()
	defer saved.mu.Unlock()

	saved.deleted = append(saved.deleted, generation+"|"+sessionID)

	return saved.operationID, saved.deleteErr
}

func (saved *fakeSavedSessions) Snapshot(_ context.Context, socket string) (string, error) {
	saved.mu.Lock()
	defer saved.mu.Unlock()

	saved.snapshotted = append(saved.snapshotted, socket)

	return saved.operationID, saved.snapshotErr
}

func (saved *fakeSavedSessions) Restore(_ context.Context, socket, generation, sessionID string) (string, error) {
	saved.mu.Lock()
	defer saved.mu.Unlock()

	saved.restored = append(saved.restored, socket+"|"+generation+"|"+sessionID)

	return saved.operationID, saved.restoreErr
}

type peerFixture struct {
	options      PeerActionOptions
	live         *fakeLiveServer
	saved        *fakeSavedSessions
	request      ActionRequest
	receiptsPath string
}

func newPeerFixture(t *testing.T) peerFixture {
	t.Helper()

	receipts, receiptsPath := openTestReceiptDir(t)

	live := &fakeLiveServer{sessions: []session.Session{liveTargetSession()}}
	saved := &fakeSavedSessions{operationID: strings.Repeat("a", 32)}

	options := PeerActionOptions{
		PeerID:   testPeerID,
		Receipts: receipts,
		Policy:   PeerPolicy{Create: true, Rename: true, Kill: true, Delete: true, Snapshot: true, Restore: true},
		Live:     live,
		Saved:    saved,
		Now:      func() time.Time { return time.Unix(2000, 0).UTC() },
	}

	request := liveActionRequest(t)
	request.Operation = ActionKill
	request.NewName = ""

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	return peerFixture{options: options, live: live, saved: saved, request: request, receiptsPath: receiptsPath}
}

func liveTargetSession() session.Session {
	return session.Session{
		ID: "$1", Name: testSessionName, Kind: session.KindLive, TmuxServerSocket: "/tmp/tmux.sock",
		RuntimeIdentity: session.RuntimeIdentity{
			TmuxServerPID:        42,
			TmuxServerStartedAt:  time.Unix(100, 0).UTC(),
			TmuxSessionCreatedAt: time.Unix(200, 0).UTC(),
		},
	}
}

func TestExecutePeerActionEnforcesPeerPolicy(t *testing.T) {
	fixture := newPeerFixture(t)
	options, live, request := fixture.options, fixture.live, fixture.request
	options.Policy.Kill = false

	result := ExecutePeerAction(t.Context(), options, request)

	if result.OK || result.Code != ActionCodeForbidden {
		t.Fatalf("ExecutePeerAction() = %#v, want %s", result, ActionCodeForbidden)
	}

	if created, killed, _ := live.counts(); created != 0 || killed != 0 {
		t.Fatal("refused operation still reached the tmux handler")
	}
}

func TestExecutePeerActionRefusesUnsupportedHandlers(t *testing.T) {
	fixture := newPeerFixture(t)
	options := fixture.options
	options.Saved = nil

	request := ActionRequest{
		Operation:        ActionRestore,
		ConnectionID:     testConnectionID,
		ConfigDigest:     testConfigDigest,
		PeerID:           testPeerID,
		Kind:             string(session.KindSaved),
		TmuxServerSocket: "/tmp/tmux.sock",
		SessionID:        "saved-session",
		SessionName:      testSessionName,
		Generation:       "saved-generation",
	}

	id, err := NewInvocationID()
	if err != nil {
		t.Fatal(err)
	}

	request.InvocationID = id

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	result := ExecutePeerAction(t.Context(), options, request)

	if result.OK || result.Code != ActionCodeUnsupported {
		t.Fatalf("ExecutePeerAction() = %#v, want %s", result, ActionCodeUnsupported)
	}
}

func TestExecutePeerActionRejectsTamperedIntent(t *testing.T) {
	fixture := newPeerFixture(t)
	options, request := fixture.options, fixture.request

	request.NewName = "tampered"

	result := ExecutePeerAction(t.Context(), options, request)

	if result.OK || result.Code != ActionCodeInvalidRequest {
		t.Fatalf("ExecutePeerAction() = %#v, want %s", result, ActionCodeInvalidRequest)
	}
}

func TestExecutePeerActionRejectsPeerMismatch(t *testing.T) {
	fixture := newPeerFixture(t)
	options, request := fixture.options, fixture.request

	request.PeerID = "other"

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	result := ExecutePeerAction(t.Context(), options, request)

	if result.OK || result.Code != ActionCodeTargetChanged {
		t.Fatalf("ExecutePeerAction() = %#v, want %s", result, ActionCodeTargetChanged)
	}
}

func TestExecutePeerActionValidatesTargetIncarnation(t *testing.T) {
	fixture := newPeerFixture(t)
	options, live, request := fixture.options, fixture.live, fixture.request

	request.TmuxServerPID = 43

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	result := ExecutePeerAction(t.Context(), options, request)

	if result.OK || result.Code != ActionCodeTargetChanged {
		t.Fatalf("ExecutePeerAction() = %#v, want %s", result, ActionCodeTargetChanged)
	}

	if created, killed, _ := live.counts(); created != 0 || killed != 0 {
		t.Fatal("stale target reached the tmux handler")
	}

	looked, err := LookupPeerInvocation(options.Receipts, request.InvocationID)
	if err != nil {
		t.Fatalf("LookupPeerInvocation() error = %v", err)
	}

	if looked.OK || looked.Code != ActionCodeTargetChanged {
		t.Fatalf("recorded outcome = %#v, want the resolved refusal", looked)
	}
}

func TestExecutePeerActionKillsValidatedTarget(t *testing.T) {
	fixture := newPeerFixture(t)
	options, live, request := fixture.options, fixture.live, fixture.request

	result := ExecutePeerAction(t.Context(), options, request)

	if !result.OK || result.Code != "" {
		t.Fatalf("ExecutePeerAction() = %#v, want success", result)
	}

	_, killed, _ := live.counts()
	if killed != 1 {
		t.Fatalf("kill calls = %d, want 1", killed)
	}

	looked, err := LookupPeerInvocation(options.Receipts, request.InvocationID)
	if err != nil {
		t.Fatal(err)
	}

	if !looked.OK {
		t.Fatalf("durable outcome = %#v, want success", looked)
	}
}

func TestExecutePeerActionNeverReplaysAnAdmittedInvocation(t *testing.T) {
	fixture := newPeerFixture(t)
	options, live, request := fixture.options, fixture.live, fixture.request

	first := ExecutePeerAction(t.Context(), options, request)
	second := ExecutePeerAction(t.Context(), options, request)

	if !first.OK || !second.OK {
		t.Fatalf("results = (%#v, %#v), want the recorded success twice", first, second)
	}

	if _, killed, _ := live.counts(); killed != 1 {
		t.Fatalf("kill calls = %d, want exactly one execution for one invocation", killed)
	}
}

func TestExecutePeerActionReportsUnknownOutcomeWhenResolutionFails(t *testing.T) {
	fixture := newPeerFixture(t)
	options, live, request := fixture.options, fixture.live, fixture.request

	receiptsPath := fixture.receiptsPath

	live.killHook = func() {
		if err := os.Chmod(receiptsPath, 0o500); err != nil {
			t.Errorf("chmod receipts: %v", err)
		}
	}

	t.Cleanup(func() { _ = os.Chmod(receiptsPath, 0o700) })

	result := ExecutePeerAction(t.Context(), options, request)

	if result.OK || result.Code != ActionCodeUnknownOutcome {
		t.Fatalf("ExecutePeerAction() = %#v, want %s", result, ActionCodeUnknownOutcome)
	}

	if _, killed, _ := live.counts(); killed != 1 {
		t.Fatalf("kill calls = %d, want one executed mutation", killed)
	}

	looked, err := LookupPeerInvocation(options.Receipts, request.InvocationID)
	if err != nil {
		t.Fatalf("LookupPeerInvocation() error = %v", err)
	}

	if looked.OK || looked.Code != ActionCodeUnknownOutcome {
		t.Fatalf("looked up outcome = %#v, want an explicit unknown outcome", looked)
	}
}

func TestExecutePeerActionCreateConflictsWithExistingSession(t *testing.T) {
	fixture := newPeerFixture(t)
	options, live := fixture.options, fixture.live

	request := ActionRequest{
		Operation:    ActionCreate,
		ConnectionID: testConnectionID,
		ConfigDigest: testConfigDigest,
		PeerID:       testPeerID,
		SessionName:  testSessionName,
		Directory:    "/work",
	}

	id, err := NewInvocationID()
	if err != nil {
		t.Fatal(err)
	}

	request.InvocationID = id

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	result := ExecutePeerAction(t.Context(), options, request)

	if result.OK || result.Code != ActionCodeConflict {
		t.Fatalf("ExecutePeerAction() = %#v, want %s", result, ActionCodeConflict)
	}

	if created, _, _ := live.counts(); created != 0 {
		t.Fatal("conflicting create reached the tmux handler")
	}
}

func TestExecutePeerActionCreatesNewSession(t *testing.T) {
	fixture := newPeerFixture(t)
	options, live := fixture.options, fixture.live

	request := ActionRequest{
		Operation:    ActionCreate,
		ConnectionID: testConnectionID,
		ConfigDigest: testConfigDigest,
		PeerID:       testPeerID,
		SessionName:  "fresh",
		Directory:    "/work",
	}

	id, err := NewInvocationID()
	if err != nil {
		t.Fatal(err)
	}

	request.InvocationID = id

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	if result := ExecutePeerAction(t.Context(), options, request); !result.OK {
		t.Fatalf("ExecutePeerAction() = %#v, want success", result)
	}

	live.mu.Lock()
	defer live.mu.Unlock()

	if len(live.created) != 1 || live.created[0] != "fresh|/work" {
		t.Fatalf("create calls = %#v, want one create in the requested directory", live.created)
	}
}

func TestExecutePeerActionLiveDeleteFailsClosedWithoutCompoundDeleter(t *testing.T) {
	fixture := newPeerFixture(t)
	options, live, request := fixture.options, fixture.live, fixture.request

	request.Operation = ActionDelete
	request.NewName = ""

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	result := ExecutePeerAction(t.Context(), options, request)

	if result.OK || result.Code != ActionCodeUnsupported {
		t.Fatalf("ExecutePeerAction() = %#v, want %s", result, ActionCodeUnsupported)
	}

	if _, killed, _ := live.counts(); killed != 0 {
		t.Fatal("live delete without compound deleter killed the live session! must fail closed")
	}
}

func TestExecutePeerActionLiveDeleteWithCompoundDeleter(t *testing.T) {
	fixture := newPeerFixture(t)
	options, request := fixture.options, fixture.request

	request.Operation = ActionDelete
	request.NewName = ""

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	var compound fakeCompoundDeleter

	compound.operationID = strings.Repeat("f", 32)
	options.Saved = &compound

	result := ExecutePeerAction(t.Context(), options, request)
	if !result.OK || result.DurableOperationID != compound.operationID {
		t.Fatalf("ExecutePeerAction() = %#v, want success with durable operation ID", result)
	}

	if len(compound.deletedLive) != 1 || compound.deletedLive[0] != "$1" {
		t.Fatalf("compound delete calls = %#v, want session $1", compound.deletedLive)
	}

	compound.deleteLiveAndSavedErr = errTestCompoundFailure
	req2 := request
	req2.InvocationID = strings.Repeat("e", 32)
	req2.IntentDigest = digest

	resultErr := ExecutePeerAction(t.Context(), options, req2)
	if resultErr.OK || resultErr.Code != ActionCodeUnknownOutcome || resultErr.DurableOperationID != compound.operationID {
		t.Fatalf("compound failure = %#v, want unknown outcome with preserved durable ID", resultErr)
	}
}

func TestExecutePeerActionEffectAppliedThenErrorRemainsUnknown(t *testing.T) {
	t.Run("create effect applied", func(t *testing.T) {
		fixture := newPeerFixture(t)
		options, live := fixture.options, fixture.live

		request := ActionRequest{
			Operation:    ActionCreate,
			ConnectionID: testConnectionID,
			ConfigDigest: testConfigDigest,
			PeerID:       testPeerID,
			SessionName:  "post-created",
			Directory:    t.TempDir(),
		}
		id, _ := NewInvocationID()
		request.InvocationID = id
		request.IntentDigest, _ = IntentDigest(request)

		live.createErr = errTestTmuxCreate
		live.createHook = func() {
			live.mu.Lock()
			live.sessions = append(live.sessions, session.Session{
				ID: "$2", Name: "post-created", Kind: session.KindLive, TmuxServerSocket: "/tmp/tmux.sock",
			})
			live.mu.Unlock()
		}

		result := ExecutePeerAction(t.Context(), options, request)
		if result.OK || result.Code != ActionCodeUnknownOutcome {
			t.Fatalf("ExecutePeerAction() = %#v, want unknown outcome", result)
		}
	})

	t.Run("rename effect applied", func(t *testing.T) {
		fixture := newPeerFixture(t)
		options, live, request := fixture.options, fixture.live, fixture.request

		request.Operation = ActionRename
		request.NewName = "post-renamed"
		request.IntentDigest, _ = IntentDigest(request)

		live.renameErr = errTestTmuxRename
		live.renameHook = func() {
			live.mu.Lock()
			live.sessions[0].Name = "post-renamed"
			live.mu.Unlock()
		}

		result := ExecutePeerAction(t.Context(), options, request)
		if result.OK || result.Code != ActionCodeUnknownOutcome {
			t.Fatalf("ExecutePeerAction() = %#v, want unknown outcome", result)
		}
	})

	t.Run("kill effect applied", func(t *testing.T) {
		fixture := newPeerFixture(t)
		options, live, request := fixture.options, fixture.live, fixture.request

		live.killErr = errTestTmuxKill
		live.killHook = func() {
			live.mu.Lock()
			live.sessions = nil
			live.mu.Unlock()
		}

		result := ExecutePeerAction(t.Context(), options, request)
		if result.OK || result.Code != ActionCodeUnknownOutcome {
			t.Fatalf("ExecutePeerAction() = %#v, want unknown outcome", result)
		}
	})

	t.Run("cancellation during execution is unknown", func(t *testing.T) {
		fixture := newPeerFixture(t)
		options, live := fixture.options, fixture.live

		request := ActionRequest{
			Operation:    ActionCreate,
			ConnectionID: testConnectionID,
			ConfigDigest: testConfigDigest,
			PeerID:       testPeerID,
			SessionName:  "cancel-created",
			Directory:    t.TempDir(),
		}
		id, _ := NewInvocationID()
		request.InvocationID = id
		request.IntentDigest, _ = IntentDigest(request)

		canceledCtx, cancel := context.WithCancel(t.Context())

		live.createHook = func() {
			cancel()
		}
		live.createErr = context.Canceled

		result := ExecutePeerAction(canceledCtx, options, request)
		if result.OK || result.Code != ActionCodeUnknownOutcome {
			t.Fatalf("ExecutePeerAction(canceled) = %#v, want unknown outcome", result)
		}
	})
}

func TestExecutePeerActionSavedOperationsRequireHandlers(t *testing.T) {
	fixture := newPeerFixture(t)
	options, saved := fixture.options, fixture.saved

	request := ActionRequest{
		Operation:        ActionRestore,
		ConnectionID:     testConnectionID,
		ConfigDigest:     testConfigDigest,
		PeerID:           testPeerID,
		Kind:             string(session.KindSaved),
		TmuxServerSocket: "/tmp/tmux.sock",
		SessionID:        "saved-session",
		SessionName:      testSessionName,
		Generation:       "saved-generation",
	}

	id, err := NewInvocationID()
	if err != nil {
		t.Fatal(err)
	}

	request.InvocationID = id

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	result := ExecutePeerAction(t.Context(), options, request)
	if !result.OK || result.DurableOperationID != saved.operationID {
		t.Fatalf("ExecutePeerAction() = %#v, want the durable operation identity", result)
	}

	saved.mu.Lock()
	defer saved.mu.Unlock()

	if len(saved.restored) != 1 || saved.restored[0] != "/tmp/tmux.sock|saved-generation|saved-session" {
		t.Fatalf("restore calls = %#v, want the validated saved target", saved.restored)
	}
}

func TestExecutePeerActionSavedDeleteUsesExactGenerationAndSession(t *testing.T) {
	fixture := newPeerFixture(t)
	request := ActionRequest{
		Operation: ActionDelete, ConnectionID: testConnectionID, ConfigDigest: testConfigDigest, PeerID: testPeerID,
		Kind: string(session.KindSaved), TmuxServerSocket: "/tmp/tmux.sock",
		SessionID: "saved-session", SessionName: testSessionName, Generation: "saved-generation",
	}
	request.InvocationID, _ = NewInvocationID()
	request.IntentDigest, _ = IntentDigest(request)

	result := ExecutePeerAction(t.Context(), fixture.options, request)
	if !result.OK || result.DurableOperationID != fixture.saved.operationID {
		t.Fatalf("ExecutePeerAction() = %#v, want durable saved deletion", result)
	}

	fixture.saved.mu.Lock()

	defer fixture.saved.mu.Unlock()

	if len(fixture.saved.deleted) != 1 || fixture.saved.deleted[0] != "saved-generation|saved-session" {
		t.Fatalf("saved delete calls = %#v, want exact generation and session", fixture.saved.deleted)
	}
}

func TestExecutePeerActionPreservesNativePartialReceipt(t *testing.T) {
	fixture := newPeerFixture(t)
	options, saved, request := fixture.options, fixture.saved, fixture.request

	request.Operation = ActionSnapshot
	request.IntentDigest, _ = IntentDigest(request)
	saved.snapshotErr = errNativeOperationPartial

	result := ExecutePeerAction(t.Context(), options, request)
	if result.OK || result.Code != ActionCodePartial || result.DurableOperationID != saved.operationID {
		t.Fatalf("ExecutePeerAction() = %#v, want partial with durable operation ID", result)
	}

	if !errors.Is((&ActionOutcomeError{Result: result}).Unwrap(), ErrActionPartial) {
		t.Fatal("partial result does not preserve ErrActionPartial")
	}
}

func TestExecutePeerActionRequiresReceipts(t *testing.T) {
	fixture := newPeerFixture(t)
	options, request := fixture.options, fixture.request
	options.Receipts = nil

	if result := ExecutePeerAction(t.Context(), options, request); result.OK || result.Code != ActionCodeUnavailable {
		t.Fatalf("ExecutePeerAction() = %#v, want %s", result, ActionCodeUnavailable)
	}
}

func TestExecutePeerActionConflictingInvocationIntentFailsClosed(t *testing.T) {
	fixture := newPeerFixture(t)
	options, live, request := fixture.options, fixture.live, fixture.request

	if result := ExecutePeerAction(t.Context(), options, request); !result.OK {
		t.Fatalf("ExecutePeerAction() = %#v, want success", result)
	}

	conflicting := request
	conflicting.SessionName = "other"

	digest, err := IntentDigest(conflicting)
	if err != nil {
		t.Fatal(err)
	}

	conflicting.IntentDigest = digest

	result := ExecutePeerAction(t.Context(), options, conflicting)

	if result.OK || result.Code != ActionCodeInvocationConflict {
		t.Fatalf("ExecutePeerAction(conflicting) = %#v, want %s", result, ActionCodeInvocationConflict)
	}

	if _, killed, _ := live.counts(); killed != 1 {
		t.Fatal("conflicting intent executed another mutation")
	}
}

func TestValidateLiveTargetRejectsReplacementWork(t *testing.T) {
	request := liveActionRequest(t)
	request.Operation = ActionKill
	request.NewName = ""

	sessions := []session.Session{liveTargetSession()}

	if _, err := ValidateLiveTarget(request, sessions); err != nil {
		t.Fatalf("ValidateLiveTarget() error = %v", err)
	}

	replaced := liveTargetSession()
	replaced.RuntimeIdentity.TmuxSessionCreatedAt = time.Unix(300, 0).UTC()

	if _, err := ValidateLiveTarget(request, []session.Session{replaced}); !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("ValidateLiveTarget(replaced) error = %v, want %v", err, ErrTargetChanged)
	}

	renamed := liveTargetSession()
	renamed.Name = "renamed"

	if _, err := ValidateLiveTarget(request, []session.Session{renamed}); !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("ValidateLiveTarget(renamed) error = %v, want %v", err, ErrTargetChanged)
	}

	if _, err := ValidateLiveTarget(request, nil); !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("ValidateLiveTarget(absent) error = %v, want %v", err, ErrTargetChanged)
	}
}

func TestLookupPeerInvocationRequiresValidIdentity(t *testing.T) {
	receipts, _ := openTestReceiptDir(t)

	if _, err := LookupPeerInvocation(receipts, "short"); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("LookupPeerInvocation() error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestPeerPolicyScopesTargets(t *testing.T) {
	policy := PeerPolicy{Create: true, Kill: true, Sockets: []string{"/tmp/tmux-work"}}

	if err := policy.Check(ActionKill, "/tmp/tmux-work"); err != nil {
		t.Fatalf("Check() error = %v", err)
	}

	if err := policy.Check(ActionKill, "/tmp/tmux-other"); !errors.Is(err, ErrPeerForbidden) {
		t.Fatalf("Check(other socket) error = %v, want %v", err, ErrPeerForbidden)
	}

	if err := policy.Check(ActionKill, ""); err != nil {
		t.Fatalf("Check(saved target) error = %v, want a socket-scoped policy to ignore saved targets", err)
	}
}

func TestPeerStateDirectoryIsPrivate(t *testing.T) {
	dir, path := openTestCatalogDir(t)

	if _, err := LoadOrCreatePeerID(dir); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(path, peerIDName))
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("peer ID mode = %o, want 0600", info.Mode().Perm())
	}
}
