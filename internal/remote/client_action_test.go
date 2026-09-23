//go:build linux || darwin

package remote

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func actionOriginFixture() (ActionOriginOptions, session.Session) {
	connection := config.RemoteConnectionConfig{ID: "peer-a", Label: "Peer A", Host: "peer.example", SeshPath: "sesh", TmuxSocket: "/tmp/peer.sock"}
	remoteConfig := config.RemoteConfig{
		Enabled: true, SSHPath: "ssh", ActionTimeout: "1s",
		Permissions: config.RemotePermissionConfig{Create: true, Rename: true, Kill: true, Delete: true, Snapshot: true, Restore: true},
		Connections: []config.RemoteConnectionConfig{connection},
	}
	state := ConnectionState{ID: connection.ID, ConfigDigest: ConnectionDigest(connection), PeerID: "peer-id", Connected: true}
	target := session.Session{
		ID: "$1", Name: "work", Kind: session.KindLive, TmuxServerSocket: connection.TmuxSocket,
		RuntimeIdentity: session.RuntimeIdentity{TmuxServerPID: 42, TmuxServerStartedAt: time.Unix(10, 0).UTC(), TmuxSessionCreatedAt: time.Unix(11, 0).UTC()},
	}

	return ActionOriginOptions{Remote: remoteConfig, Connection: connection, State: state}, target
}

func TestNewActionRequestBindsCachedIncarnationAndCallerIdentity(t *testing.T) {
	options, target := actionOriginFixture()

	request, err := NewActionRequest(options, target, ActionRename, "renamed", "")
	if err != nil {
		t.Fatalf("NewActionRequest() error = %v", err)
	}

	if !ValidInvocationID(request.InvocationID) || request.IntentDigest == "" {
		t.Fatalf("request identity is incomplete: %#v", request)
	}

	if request.PeerID != options.State.PeerID || request.ConfigDigest != options.State.ConfigDigest || request.SessionID != target.ID {
		t.Fatalf("request lost cached identity: %#v", request)
	}

	if err := VerifyIntentDigest(request); err != nil {
		t.Fatalf("VerifyIntentDigest() error = %v", err)
	}
}

func TestNewActionRequestRejectsChangedConnectionBeforeDispatch(t *testing.T) {
	options, target := actionOriginFixture()
	options.Connection.Host = "replacement.example"

	if _, err := NewActionRequest(options, target, ActionKill, "", ""); !errors.Is(err, ErrConnectionChanged) {
		t.Fatalf("NewActionRequest() error = %v, want ErrConnectionChanged", err)
	}
}

func TestNewActionRequestRetainsExactSavedGeneration(t *testing.T) {
	options, _ := actionOriginFixture()
	target := session.Session{
		ID: "saved-session", Name: "work", Kind: session.KindSaved, Generation: "saved-generation",
		TmuxServerSocket: options.Connection.TmuxSocket,
	}

	request, err := NewActionRequest(options, target, ActionRestore, "", "")
	if err != nil {
		t.Fatalf("NewActionRequest() error = %v", err)
	}

	if request.Generation != target.Generation || request.SessionID != target.ID {
		t.Fatalf("saved identity = %q/%q, want %q/%q", request.Generation, request.SessionID, target.Generation, target.ID)
	}
}

func TestRunActionTransportFailurePreservesInvocationAsUnknown(t *testing.T) {
	options, target := actionOriginFixture()

	request, err := NewActionRequest(options, target, ActionKill, "", "")
	if err != nil {
		t.Fatal(err)
	}

	options.Remote.SSHPath = "/definitely/missing/ssh"

	result, err := RunAction(t.Context(), options, request)
	if err == nil {
		t.Fatal("RunAction() unexpectedly succeeded")
	}

	if result.InvocationID != request.InvocationID || result.Code != ActionCodeUnknownOutcome {
		t.Fatalf("result = %#v, want preserved unknown invocation", result)
	}
}

func TestNativeSavedDeleteRequiresDependencies(t *testing.T) {
	service := &NativeSavedSessions{}

	operation, err := service.Delete(context.Background(), "generation", "saved-session")
	if operation != "" || !errors.Is(err, ErrIncompleteDependencies) {
		t.Fatalf("Delete() = %q, %v", operation, err)
	}
}
