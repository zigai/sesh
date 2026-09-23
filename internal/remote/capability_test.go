//go:build linux || darwin

package remote

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/privatefs"
)

func openTestBridgeDir(t *testing.T) (*privatefs.Dir, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "bridge")

	dir, err := openPrivateDir(path, true)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = dir.Close() })

	return dir, path
}

func TestBridgeMaterialIsUnpredictable(t *testing.T) {
	seen := make(map[string]bool)

	for range 32 {
		attachmentID, err := NewAttachmentID()
		if err != nil {
			t.Fatal(err)
		}

		token, err := NewBridgeToken()
		if err != nil {
			t.Fatal(err)
		}

		if !ValidAttachmentID(attachmentID) || !validBridgeToken(token) {
			t.Fatalf("generated material = (%q, %q), want valid identifiers", attachmentID, token)
		}

		if seen[attachmentID] || seen[token] {
			t.Fatal("generated bridge material repeated")
		}

		seen[attachmentID] = true
		seen[token] = true
	}
}

func TestCapabilityRoundTripIsPrivate(t *testing.T) {
	dir, path := openTestBridgeDir(t)

	grant := validGrant(t)
	now := time.Unix(1000, 0).UTC()

	record := StoredCapability{
		Version:      ProtocolVersion,
		Grant:        grant,
		Target:       validTarget(t),
		BridgeSocket: filepath.Join(path, "attach-"+grant.AttachmentID+".sock"),
		WrittenAt:    now,
	}

	if err := WriteCapability(dir, record); err != nil {
		t.Fatalf("WriteCapability() error = %v", err)
	}

	name, err := CapabilityName(grant.AttachmentID)
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(path, name))
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("capability mode = %o, want 0600", info.Mode().Perm())
	}

	loaded, err := ReadCapability(dir, grant.AttachmentID, now)
	if err != nil {
		t.Fatalf("ReadCapability() error = %v", err)
	}

	if loaded.Grant.Token != grant.Token || loaded.Target.SessionID != record.Target.SessionID || loaded.BridgeSocket != record.BridgeSocket {
		t.Fatalf("loaded capability = %#v, want the stored record", loaded)
	}

	if err := RemoveCapability(dir, grant.AttachmentID); err != nil {
		t.Fatalf("RemoveCapability() error = %v", err)
	}

	if _, err := ReadCapability(dir, grant.AttachmentID, now); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadCapability() after removal error = %v, want not-exist", err)
	}

	if err := RemoveCapability(dir, grant.AttachmentID); err != nil {
		t.Fatalf("repeated RemoveCapability() error = %v", err)
	}
}

func TestCapabilityRejectsExpiredAndMismatchedRecords(t *testing.T) {
	dir, path := openTestBridgeDir(t)

	grant := validGrant(t)
	now := time.Unix(1000, 0).UTC()

	record := StoredCapability{
		Version:      ProtocolVersion,
		Grant:        grant,
		Target:       validTarget(t),
		BridgeSocket: filepath.Join(path, "attach-"+grant.AttachmentID+".sock"),
		WrittenAt:    now,
	}

	if err := WriteCapability(dir, record); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadCapability(dir, grant.AttachmentID, grant.ExpiresAt); !errors.Is(err, ErrCapabilityExpired) {
		t.Fatalf("ReadCapability(expired) error = %v, want %v", err, ErrCapabilityExpired)
	}

	other, err := NewAttachmentID()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ReadCapability(dir, other, now); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadCapability(other attachment) error = %v, want not-exist", err)
	}

	if _, err := ReadCapability(dir, "short", now); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("ReadCapability(invalid ID) error = %v, want %v", err, ErrUnsafePath)
	}
}

func TestCapabilityValidationRejectsUnsafeRecords(t *testing.T) {
	dir, path := openTestBridgeDir(t)

	grant := validGrant(t)
	now := time.Unix(1000, 0).UTC()

	valid := StoredCapability{
		Version:      ProtocolVersion,
		Grant:        grant,
		Target:       validTarget(t),
		BridgeSocket: filepath.Join(path, "attach-"+grant.AttachmentID+".sock"),
		WrittenAt:    now,
	}

	tests := map[string]struct {
		mutate func(*StoredCapability)
		want   error
	}{
		"unsupported version":  {mutate: func(c *StoredCapability) { c.Version = ProtocolVersion + 1 }, want: ErrInvalidFrame},
		"relative socket":      {mutate: func(c *StoredCapability) { c.BridgeSocket = "attach.sock" }, want: ErrInvalidFrame},
		"dirty socket":         {mutate: func(c *StoredCapability) { c.BridgeSocket = "/tmp/../tmp/bridge.sock" }, want: ErrInvalidFrame},
		"missing written time": {mutate: func(c *StoredCapability) { c.WrittenAt = time.Time{} }, want: ErrInvalidFrame},
		"peer mismatch":        {mutate: func(c *StoredCapability) { c.Target.PeerID = "other" }, want: ErrPeerMismatch},
		"control token":        {mutate: func(c *StoredCapability) { c.Grant.Token = strings.Repeat("a", 64) + "\x1b" }, want: ErrInvalidFrame},
		"short token":          {mutate: func(c *StoredCapability) { c.Grant.Token = "abc" }, want: ErrInvalidFrame},
		"missing expiry":       {mutate: func(c *StoredCapability) { c.Grant.ExpiresAt = time.Time{} }, want: ErrInvalidFrame},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			record := valid
			test.mutate(&record)

			if err := WriteCapability(dir, record); !errors.Is(err, test.want) {
				t.Fatalf("WriteCapability() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCapabilityNameAndSocketNameAreScoped(t *testing.T) {
	attachmentID, err := NewAttachmentID()
	if err != nil {
		t.Fatal(err)
	}

	name, err := CapabilityName(attachmentID)
	if err != nil {
		t.Fatal(err)
	}

	if name != capabilityPrefix+attachmentID+capabilitySuffix {
		t.Fatalf("CapabilityName() = %q, want the attachment-scoped name", name)
	}

	socket, err := BridgeSocketName(attachmentID)
	if err != nil {
		t.Fatal(err)
	}

	if socket != capabilityPrefix+attachmentID+socketSuffix {
		t.Fatalf("BridgeSocketName() = %q, want the attachment-scoped socket", socket)
	}

	if _, err := CapabilityName("../../escape"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("CapabilityName(escape) error = %v, want %v", err, ErrUnsafePath)
	}
}

func TestBridgeSocketPathIsScopedToThePeerDirectory(t *testing.T) {
	attachmentID, err := NewAttachmentID()
	if err != nil {
		t.Fatal(err)
	}

	path, err := BridgeSocketPath("/home/remote/.local/state/sesh/remote/bridge", attachmentID)
	if err != nil {
		t.Fatalf("BridgeSocketPath() error = %v", err)
	}

	want := "/home/remote/.local/state/sesh/remote/bridge/attach-" + attachmentID + ".sock"
	if path != want {
		t.Fatalf("BridgeSocketPath() = %q, want %q", path, want)
	}

	for _, invalid := range []string{"relative/bridge", "/home/remote/../bridge", "/", ""} {
		if _, err := BridgeSocketPath(invalid, attachmentID); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("BridgeSocketPath(%q) error = %v, want %v", invalid, err, ErrUnsafePath)
		}
	}
}

func TestCheckSocketAbsentNeverAdoptsExistingEntries(t *testing.T) {
	dir, path := openTestBridgeDir(t)

	attachmentID, err := NewAttachmentID()
	if err != nil {
		t.Fatal(err)
	}

	if err := CheckSocketAbsent(dir, attachmentID); err != nil {
		t.Fatalf("CheckSocketAbsent() error = %v, want an empty directory", err)
	}

	socketName, err := BridgeSocketName(attachmentID)
	if err != nil {
		t.Fatal(err)
	}

	socketPath := filepath.Join(path, socketName)

	if err := os.WriteFile(socketPath, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CheckSocketAbsent(dir, attachmentID); !errors.Is(err, ErrForeignSocketPath) {
		t.Fatalf("CheckSocketAbsent(existing) error = %v, want %v", err, ErrForeignSocketPath)
	}

	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("CheckSocketAbsent() removed a foreign entry: %v", err)
	}
}

func TestGrantMatchingIsConstantTimeAndTimeBound(t *testing.T) {
	grant := validGrant(t)
	now := time.Unix(1000, 0).UTC()

	if !grant.Matches(grant.Token, now) {
		t.Fatal("Matches() rejected the correct token")
	}

	if grant.Matches(grant.Token+"0", now) || grant.Matches("", now) {
		t.Fatal("Matches() accepted a wrong token")
	}

	if grant.Matches(grant.Token, grant.ExpiresAt) {
		t.Fatal("Matches() accepted an expired capability")
	}

	if !(BridgeGrant{}).Expired(now) {
		t.Fatal("Expired() accepted a grant without an expiry")
	}
}
