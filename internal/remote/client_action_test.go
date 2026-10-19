//go:build linux || darwin

package remote

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

var errTestCatalog = errors.New("catalog unreadable")

func actionOriginFixture() (ActionOriginOptions, action.SessionRef) {
	connection := config.RemoteConnectionConfig{ID: "peer-a", Label: "Peer A", Host: "peer.example", SeshPath: "sesh", TmuxSocket: "/tmp/peer.sock"}
	remoteConfig := config.RemoteConfig{
		Enabled: true, SSHPath: "ssh", ActionTimeout: "5s",
		Connections: []config.RemoteConnectionConfig{connection},
	}
	state := ConnectionState{
		ID: connection.ID, ConfigDigest: ConnectionDigest(connection), PeerID: "peer-id", Connected: true,
		Snapshot: Snapshot{GeneratedAt: time.Time{}, Sessions: nil, Actions: []action.ID{action.SessionKill.ID}, Health: UnavailableHealth()},
	}
	target := action.SessionRef{
		Kind: session.KindLive, ID: "$1", Name: "work", Socket: connection.TmuxSocket,
		ServerPID: 42, ServerStartedAt: time.Unix(10, 0).UTC(), CreatedAt: time.Unix(11, 0).UTC(), Generation: "",
	}

	return ActionOriginOptions{Remote: remoteConfig, Connection: connection, State: state}, target
}

func fixturePeer(opts ActionOriginOptions) *Peer {
	catalog := Catalog{Version: ProtocolVersion, GeneratedAt: time.Time{}, Connections: map[string]ConnectionState{opts.State.ID: opts.State}}
	peer := NewPeer(opts.Remote, func() (Catalog, error) { return catalog, nil })
	peer.Observe(catalog)

	return peer
}

func killInvocation(opts ActionOriginOptions, target action.SessionRef) action.Invocation {
	return action.Invocation{
		Action: action.SessionKill.ID,
		At:     action.Peer(opts.Connection.ID),
		Caller: action.Caller{Client: "", Pane: "", Session: "", Width: "", Height: ""},
		Input:  action.KillSession{Session: target, Force: false},
	}
}

func scriptedSSHPath(t *testing.T, script string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-ssh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestNewActionRequestBindsCachedPeerAndServer(t *testing.T) {
	options, target := actionOriginFixture()

	input, err := action.SessionKill.EncodeInput(action.KillSession{Session: target, Force: false})
	if err != nil {
		t.Fatal(err)
	}

	request, err := NewActionRequest(options, action.SessionKill.ID, input)
	if err != nil {
		t.Fatalf("NewActionRequest() error = %v", err)
	}

	if request.PeerID != options.State.PeerID || request.ConfigDigest != options.State.ConfigDigest || request.Socket != options.Connection.TmuxSocket {
		t.Fatalf("request lost cached identity: %#v", request)
	}

	if err := VerifyIntentDigest(request); err != nil {
		t.Fatalf("VerifyIntentDigest() error = %v", err)
	}
}

func TestNewActionRequestRefusesStaleOrDisabledConnections(t *testing.T) {
	for name, mutate := range map[string]func(*ActionOriginOptions){
		"connection changed": func(opts *ActionOriginOptions) { opts.Connection.Host = "replacement.example" },
		"disconnected":       func(opts *ActionOriginOptions) { opts.State.Connected = false },
		"remote disabled":    func(opts *ActionOriginOptions) { opts.Remote.Enabled = false },
	} {
		t.Run(name, func(t *testing.T) {
			options, _ := actionOriginFixture()
			mutate(&options)

			if _, err := NewActionRequest(options, action.SessionKill.ID, []byte(`{}`)); err == nil {
				t.Fatal("NewActionRequest() accepted a connection it cannot trust")
			}
		})
	}
}

func TestPeerOffersOnlyAdvertisedActionsOfConnectedPeers(t *testing.T) {
	options, _ := actionOriginFixture()
	peer := fixturePeer(options)
	at := action.Peer(options.Connection.ID)

	if !peer.Accepts(at, action.SessionKill.ID) {
		t.Fatal("advertised action was not offered")
	}

	if peer.Accepts(at, action.SessionDelete.ID) || peer.Accepts(action.Local(), action.SessionKill.ID) ||
		peer.Accepts(action.Peer("other"), action.SessionKill.ID) {
		t.Fatal("peer offered an action its peer did not advertise")
	}

	disconnected := options.State
	disconnected.Connected = false
	peer.Observe(Catalog{Version: ProtocolVersion, GeneratedAt: time.Time{}, Connections: map[string]ConnectionState{disconnected.ID: disconnected}})

	if peer.Accepts(at, action.SessionKill.ID) {
		t.Fatal("disconnected peer still offered actions")
	}
}

func TestPeerDispatchChecksTheCurrentCatalogBeforeSending(t *testing.T) {
	options, target := actionOriginFixture()
	options.Remote.SSHPath = scriptedSSHPath(t, "echo sent >> \""+filepath.Join(t.TempDir(), "sent")+"\"\nexit 1\n")

	withdrawn := options.State
	withdrawn.Snapshot.Actions = nil

	peer := NewPeer(options.Remote, func() (Catalog, error) {
		return Catalog{Version: ProtocolVersion, GeneratedAt: time.Time{}, Connections: map[string]ConnectionState{withdrawn.ID: withdrawn}}, nil
	})

	if _, err := peer.Dispatch(t.Context(), killInvocation(options, target)); action.CodeOf(err) != action.CodeForbidden || !errors.Is(err, ErrActionNotOffered) {
		t.Fatalf("Dispatch(withdrawn action) error = %v, want forbidden", err)
	}

	unreadable := NewPeer(options.Remote, func() (Catalog, error) { return Catalog{}, errTestCatalog })
	if _, err := unreadable.Dispatch(t.Context(), killInvocation(options, target)); action.CodeOf(err) != action.CodeUnavailable {
		t.Fatalf("Dispatch(unreadable catalog) error = %v, want unavailable", err)
	}
}

func TestPeerDispatchDecodesThePeersTypedOutput(t *testing.T) {
	options, target := actionOriginFixture()
	options.Remote.SSHPath = scriptedSSHPath(t, `request=$(cat)
id=$(printf '%s' "$request" | sed -n 's/.*"invocationId":"\([0-9a-f]*\)".*/\1/p')
printf '{"invocationId":"%s","action":"session.kill","ok":false,"code":"targetChanged","output":{"session":{"name":"work"}}}\n' "$id"
`)

	output, err := fixturePeer(options).Dispatch(t.Context(), killInvocation(options, target))
	if action.CodeOf(err) != action.CodeTargetChanged {
		t.Fatalf("Dispatch() error = %v, want the peer's code", err)
	}

	peerErr, ok := errors.AsType[*PeerError](err)
	if !ok || !ValidInvocationID(peerErr.InvocationID) {
		t.Fatalf("Dispatch() error = %#v, want the invocation to look up", err)
	}

	killed, ok := output.(action.KilledSession)
	if !ok || killed.Session.Name != "work" {
		t.Fatalf("Dispatch() output = %#v, want the peer's typed output", output)
	}
}

func TestPeerDispatchTransportFailureIsUncertainAndKeepsTheInvocation(t *testing.T) {
	options, target := actionOriginFixture()
	options.Remote.SSHPath = scriptedSSHPath(t, "cat >/dev/null\nexit 255\n")

	_, err := fixturePeer(options).Dispatch(t.Context(), killInvocation(options, target))
	if action.CodeOf(err) != action.CodeUncertain {
		t.Fatalf("Dispatch() error = %v, want an uncertain outcome", err)
	}

	peerErr, ok := errors.AsType[*PeerError](err)
	if !ok || !ValidInvocationID(peerErr.InvocationID) || peerErr.Err == nil {
		t.Fatalf("Dispatch() error = %#v, want the invocation and transport cause", err)
	}
}

func TestPeerDispatchRejectsAnswersForAnotherInvocation(t *testing.T) {
	options, target := actionOriginFixture()
	options.Remote.SSHPath = scriptedSSHPath(t, `cat >/dev/null
printf '{"invocationId":"0123456789abcdef0123456789abcdef","action":"session.kill","ok":true,"code":""}\n'
`)

	if _, err := fixturePeer(options).Dispatch(t.Context(), killInvocation(options, target)); action.CodeOf(err) != action.CodeUncertain {
		t.Fatalf("Dispatch() error = %v, want an uncertain outcome", err)
	}
}
