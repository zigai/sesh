//go:build linux || darwin

package remote

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validGrant(t *testing.T) BridgeGrant {
	t.Helper()

	attachmentID, err := NewAttachmentID()
	if err != nil {
		t.Fatal(err)
	}

	token, err := NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}

	return BridgeGrant{
		Version:      ProtocolVersion,
		AttachmentID: attachmentID,
		PeerID:       testPeerID,
		Token:        token,
		OriginSocket: "/run/user/1000/sesh-bridge.sock",
		OriginLabel:  "laptop",
		ExpiresAt:    time.Unix(4_000_000_000, 0).UTC(),
	}
}

func validTarget(t *testing.T) AttachTarget {
	t.Helper()

	return AttachTarget{
		PeerID:              testPeerID,
		TmuxServerSocket:    "/tmp/tmux-1000/default",
		TmuxServerPID:       42,
		TmuxServerStartedAt: time.Unix(100, 0).UTC(),
		SessionID:           "$1",
		SessionName:         testSessionName,
		SessionCreatedAt:    time.Unix(200, 0).UTC(),
		PaneID:              "%2",
		CatalogSequence:     7,
	}
}

func TestAttachBootstrapRoundTripPreservesValidatedTarget(t *testing.T) {
	bootstrap := AttachBootstrap{
		Version:       ProtocolVersion,
		Channel:       ChannelAttachPrepare,
		Target:        validTarget(t),
		Capability:    validGrant(t),
		BridgeSocket:  "/home/remote/.local/state/sesh/remote/bridge/attach-0123456789abcdef0123456789abcdef.sock",
		TimeoutMillis: 30_000,
	}

	payload, err := EncodeAttachBootstrap(bootstrap)
	if err != nil {
		t.Fatalf("EncodeAttachBootstrap() error = %v", err)
	}

	decoded, err := DecodeAttachBootstrap(payload)
	if err != nil {
		t.Fatalf("DecodeAttachBootstrap() error = %v", err)
	}

	if !reflect.DeepEqual(decoded, bootstrap) {
		t.Fatalf("attach bootstrap round trip = %#v, want %#v", decoded, bootstrap)
	}
}

func TestAttachBootstrapRejectsUnvalidatedShapes(t *testing.T) {
	valid := AttachBootstrap{
		Version:       ProtocolVersion,
		Channel:       ChannelAttachPrepare,
		Target:        validTarget(t),
		Capability:    validGrant(t),
		BridgeSocket:  "/home/remote/.local/state/sesh/remote/bridge/attach-0123456789abcdef0123456789abcdef.sock",
		TimeoutMillis: 30_000,
	}

	tests := map[string]struct {
		mutate func(*AttachBootstrap)
		want   error
	}{
		"unsupported version":    {mutate: func(b *AttachBootstrap) { b.Version = ProtocolVersion + 1 }, want: ErrInvalidFrame},
		"wrong channel":          {mutate: func(b *AttachBootstrap) { b.Channel = ChannelAttach }, want: ErrInvalidFrame},
		"peer mismatch":          {mutate: func(b *AttachBootstrap) { b.Target.PeerID = "other" }, want: ErrPeerMismatch},
		"relative bridge socket": {mutate: func(b *AttachBootstrap) { b.BridgeSocket = "bridge.sock" }, want: ErrInvalidFrame},
		"dirty bridge socket":    {mutate: func(b *AttachBootstrap) { b.BridgeSocket = "/home/remote/../remote.sock" }, want: ErrInvalidFrame},
		"expired capability":     {mutate: func(b *AttachBootstrap) { b.Capability.ExpiresAt = time.Time{} }, want: ErrInvalidFrame},
		"negative timeout":       {mutate: func(b *AttachBootstrap) { b.TimeoutMillis = -1 }, want: ErrInvalidFrame},
		"unbounded timeout":      {mutate: func(b *AttachBootstrap) { b.TimeoutMillis = maxAttachTimeoutMillis + 1 }, want: ErrInvalidFrame},
		"missing session id":     {mutate: func(b *AttachBootstrap) { b.Target.SessionID = "" }, want: ErrInvalidFrame},
		"zero server pid":        {mutate: func(b *AttachBootstrap) { b.Target.TmuxServerPID = 0 }, want: ErrInvalidFrame},
		"missing start time":     {mutate: func(b *AttachBootstrap) { b.Target.TmuxServerStartedAt = time.Time{} }, want: ErrInvalidFrame},
		"control character":      {mutate: func(b *AttachBootstrap) { b.Target.SessionName = "dev\tforged" }, want: ErrInvalidFrame},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			bootstrap := valid
			test.mutate(&bootstrap)

			if _, err := EncodeAttachBootstrap(bootstrap); !errors.Is(err, test.want) {
				t.Fatalf("EncodeAttachBootstrap() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestAttachBootstrapRejectsUnknownMembers(t *testing.T) {
	valid := AttachBootstrap{
		Version:       ProtocolVersion,
		Channel:       ChannelAttachPrepare,
		Target:        validTarget(t),
		Capability:    validGrant(t),
		BridgeSocket:  "/home/remote/.local/state/sesh/remote/bridge/attach-0123456789abcdef0123456789abcdef.sock",
		TimeoutMillis: 30_000,
	}

	payload, err := EncodeAttachBootstrap(valid)
	if err != nil {
		t.Fatal(err)
	}

	injected := bytes.Replace(payload, []byte(`"timeoutMillis"`), []byte(`"forged":1,"timeoutMillis"`), 1)
	if bytes.Equal(injected, payload) {
		t.Fatal("fixture did not inject a member")
	}

	if _, err := DecodeAttachBootstrap(injected); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("DecodeAttachBootstrap() error = %v, want invalid frame", err)
	}
}

func TestAttachBootstrapStaysWithinItsBound(t *testing.T) {
	oversized := AttachBootstrap{
		Version:       ProtocolVersion,
		Channel:       ChannelAttachPrepare,
		Target:        validTarget(t),
		Capability:    validGrant(t),
		BridgeSocket:  "/home/remote/.local/state/sesh/remote/bridge/attach-0123456789abcdef0123456789abcdef.sock",
		TimeoutMillis: 30_000,
	}
	oversized.Target.TmuxServerSocket = strings.Repeat("x", MaxStringBytes)
	oversized.Capability.OriginSocket = "/" + strings.Repeat("x", MaxStringBytes-1)
	oversized.Capability.OriginLabel = strings.Repeat("x", MaxStringBytes)
	oversized.BridgeSocket = "/" + strings.Repeat("y", MaxStringBytes-1)

	if _, err := EncodeAttachBootstrap(oversized); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("EncodeAttachBootstrap() error = %v, want frame too large", err)
	}
}

func TestStreamBootstrapRoundTripAndValidation(t *testing.T) {
	bootstrap := StreamBootstrap{
		Version:           ProtocolVersion,
		TmuxSocket:        "/tmp/tmux-1000/default",
		PollInterval:      "1s",
		HeartbeatInterval: "10s",
	}

	payload, err := EncodeStreamBootstrap(bootstrap)
	if err != nil {
		t.Fatalf("EncodeStreamBootstrap() error = %v", err)
	}

	decoded, err := DecodeStreamBootstrap(payload)
	if err != nil {
		t.Fatalf("DecodeStreamBootstrap() error = %v", err)
	}

	if !reflect.DeepEqual(decoded, bootstrap) {
		t.Fatalf("stream bootstrap round trip = %#v, want %#v", decoded, bootstrap)
	}

	for name, mutate := range map[string]func(*StreamBootstrap){
		"unsupported version": func(b *StreamBootstrap) { b.Version = 0 },
		"invalid interval":    func(b *StreamBootstrap) { b.PollInterval = "soon" },
		"negative interval":   func(b *StreamBootstrap) { b.HeartbeatInterval = "-1s" },
		"control socket":      func(b *StreamBootstrap) { b.TmuxSocket = "/tmp/\x1b]0;x" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := bootstrap
			mutate(&invalid)

			if _, err := EncodeStreamBootstrap(invalid); !errors.Is(err, ErrInvalidFrame) {
				t.Fatalf("EncodeStreamBootstrap() error = %v, want invalid frame", err)
			}
		})
	}
}
