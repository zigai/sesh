//go:build linux || darwin

package remote

import (
	"context"
	"encoding/json/v2"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareAttachmentDerivesScopedSocketAndGrant(t *testing.T) {
	remote := testRemoteConfig()
	bridgeDir := "/home/remote/.local/state/sesh/remote/bridge"

	now := time.Unix(1000, 0).UTC()

	bootstrap, err := PrepareAttachment(AttachOriginOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		BridgeDir:    bridgeDir,
		OriginSocket: "/run/user/1000/sesh-bridge.sock",
		OriginLabel:  "laptop",
		Target:       attachTargetFor(defaultSessionTarget()),
		Timeout:      2 * time.Minute,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("PrepareAttachment() error = %v", err)
	}

	if bootstrap.Channel != ChannelAttachPrepare || bootstrap.Version != ProtocolVersion {
		t.Fatalf("bootstrap = %#v, want the native prepare channel", bootstrap)
	}

	wantSocket, err := BridgeSocketPath(bridgeDir, bootstrap.Capability.AttachmentID)
	if err != nil {
		t.Fatal(err)
	}

	if bootstrap.BridgeSocket != wantSocket {
		t.Fatalf("bridge socket = %q, want %q", bootstrap.BridgeSocket, wantSocket)
	}

	if !filepath.IsAbs(bootstrap.BridgeSocket) || filepath.Dir(bootstrap.BridgeSocket) != bridgeDir {
		t.Fatalf("bridge socket = %q, want a path inside the peer bridge directory", bootstrap.BridgeSocket)
	}

	if bootstrap.Capability.ExpiresAt != now.Add(2*time.Minute) {
		t.Fatalf("capability expiry = %v, want %v", bootstrap.Capability.ExpiresAt, now.Add(2*time.Minute))
	}

	if bootstrap.TimeoutMillis != int((2 * time.Minute).Milliseconds()) {
		t.Fatalf("timeout = %dms, want %dms", bootstrap.TimeoutMillis, (2 * time.Minute).Milliseconds())
	}

	if !bootstrap.Capability.Matches(bootstrap.Capability.Token, now) {
		t.Fatal("generated capability does not authenticate its own token")
	}
}

func TestPrepareAttachmentRejectsIncompleteInputs(t *testing.T) {
	remote := testRemoteConfig()
	remote.Enabled = false

	options := AttachOriginOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		BridgeDir:    "/home/remote/bridge",
		OriginSocket: "/run/user/1000/sesh-bridge.sock",
		Target:       attachTargetFor(defaultSessionTarget()),
	}

	if _, err := PrepareAttachment(options); !errors.Is(err, ErrRemoteDisabled) {
		t.Fatalf("PrepareAttachment(disabled) error = %v, want %v", err, ErrRemoteDisabled)
	}

	options.Remote.Enabled = true
	options.BridgeDir = "relative/bridge"

	if _, err := PrepareAttachment(options); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("PrepareAttachment(relative bridge dir) error = %v, want %v", err, ErrUnsafePath)
	}

	options.BridgeDir = "/home/remote/bridge"
	options.Target = AttachTarget{}

	if _, err := PrepareAttachment(options); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("PrepareAttachment(empty target) error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestRunAttachPrepareReadsPeerConfirmation(t *testing.T) {
	remote := testRemoteConfig()

	bootstrap, err := PrepareAttachment(AttachOriginOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		BridgeDir:    "/home/remote/.local/state/sesh/remote/bridge",
		OriginSocket: "/run/user/1000/sesh-bridge.sock",
		OriginLabel:  "laptop",
		Target:       attachTargetFor(defaultSessionTarget()),
		Timeout:      time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := EncodeAttachPrepareResult(AttachPrepareResult{
		Version:      ProtocolVersion,
		AttachmentID: bootstrap.Capability.AttachmentID,
		BridgeSocket: bootstrap.BridgeSocket,
		ExpiresAt:    bootstrap.Capability.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	script := "cat > /dev/null\nprintf '%s\\n' '" + string(result) + "'\n"

	confirmed, err := RunAttachPrepare(t.Context(), remote, remote.Connections[0], bootstrap, AttachCommandOptions{
		Command: scriptSSH(t, script),
	})
	if err != nil {
		t.Fatalf("RunAttachPrepare() error = %v", err)
	}

	if confirmed.AttachmentID != bootstrap.Capability.AttachmentID || confirmed.BridgeSocket != bootstrap.BridgeSocket {
		t.Fatalf("confirmation = %#v, want the prepared attachment", confirmed)
	}
}

func TestRunAttachPrepareRejectsMismatchedConfirmation(t *testing.T) {
	remote := testRemoteConfig()

	bootstrap, err := PrepareAttachment(AttachOriginOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		BridgeDir:    "/home/remote/.local/state/sesh/remote/bridge",
		OriginSocket: "/run/user/1000/sesh-bridge.sock",
		Target:       attachTargetFor(defaultSessionTarget()),
	})
	if err != nil {
		t.Fatal(err)
	}

	other, err := NewAttachmentID()
	if err != nil {
		t.Fatal(err)
	}

	confirmation := AttachPrepareResult{
		Version:      ProtocolVersion,
		AttachmentID: other,
		BridgeSocket: bootstrap.BridgeSocket,
		ExpiresAt:    bootstrap.Capability.ExpiresAt,
	}

	encoded, err := json.Marshal(confirmation)
	if err != nil {
		t.Fatal(err)
	}

	script := "printf '%s\\n' '" + string(encoded) + "'\n"

	if _, err := RunAttachPrepare(t.Context(), remote, remote.Connections[0], bootstrap, AttachCommandOptions{
		Command: scriptSSH(t, script),
	}); !errors.Is(err, ErrCapabilityMismatch) {
		t.Fatalf("RunAttachPrepare() error = %v, want %v", err, ErrCapabilityMismatch)
	}
}

func TestRunAttachPrepareFailsClosed(t *testing.T) {
	remote := testRemoteConfig()

	bootstrap, err := PrepareAttachment(AttachOriginOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		BridgeDir:    "/home/remote/.local/state/sesh/remote/bridge",
		OriginSocket: "/run/user/1000/sesh-bridge.sock",
		Target:       attachTargetFor(defaultSessionTarget()),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := RunAttachPrepare(t.Context(), remote, remote.Connections[0], bootstrap, AttachCommandOptions{
		Command: scriptSSH(t, "exit 0\n"),
	}); !errors.Is(err, errAttachResultMissing) {
		t.Fatalf("RunAttachPrepare(no result) error = %v, want %v", err, errAttachResultMissing)
	}

	if _, err := RunAttachPrepare(t.Context(), remote, remote.Connections[0], bootstrap, AttachCommandOptions{
		Command: scriptSSH(t, "echo 'Permission denied (publickey).' >&2\nexit 255\n"),
	}); !errors.Is(err, ErrSSHAuthUnavailable) {
		t.Fatalf("RunAttachPrepare(auth failure) error = %v, want %v", err, ErrSSHAuthUnavailable)
	}
}

func TestAttachPrepareResultRoundTrip(t *testing.T) {
	attachmentID, err := NewAttachmentID()
	if err != nil {
		t.Fatal(err)
	}

	result := AttachPrepareResult{
		Version:      ProtocolVersion,
		AttachmentID: attachmentID,
		BridgeSocket: "/home/remote/.local/state/sesh/remote/bridge/attach-" + attachmentID + ".sock",
		ExpiresAt:    time.Unix(1000, 0).UTC(),
	}

	encoded, err := EncodeAttachPrepareResult(result)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := DecodeAttachPrepareResult(encoded)
	if err != nil {
		t.Fatalf("DecodeAttachPrepareResult() error = %v", err)
	}

	if decoded != result {
		t.Fatalf("decoded result = %#v, want %#v", decoded, result)
	}

	if _, err := DecodeAttachPrepareResult([]byte(`{"version":1}`)); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("DecodeAttachPrepareResult(incomplete) error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestNewAttachCommandRequiresTerminalStreams(t *testing.T) {
	remote := testRemoteConfig()

	bootstrap, err := PrepareAttachment(AttachOriginOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		BridgeDir:    "/home/remote/.local/state/sesh/remote/bridge",
		OriginSocket: "/run/user/1000/sesh-bridge.sock",
		Target:       attachTargetFor(defaultSessionTarget()),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewAttachCommand(context.Background(), remote, remote.Connections[0], bootstrap, AttachCommandOptions{}); !errors.Is(err, errNilAttachStreams) {
		t.Fatalf("NewAttachCommand() error = %v, want %v", err, errNilAttachStreams)
	}
}
