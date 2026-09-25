//go:build linux || darwin

package remote

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
)

const (
	attachmentIDBytes = 16
	bridgeTokenBytes  = 32
	capabilityPrefix  = "attach-"
	capabilitySuffix  = ".json"
	socketSuffix      = ".sock"
)

var (
	ErrCapabilityExpired  = errors.New("remote bridge capability expired")
	ErrCapabilityMismatch = errors.New("remote bridge capability does not match")
	ErrForeignSocketPath  = errors.New("remote bridge socket path is not owned by this attachment")
)

type BridgeGrant struct {
	Version      int       `json:"version"`
	AttachmentID string    `json:"attachmentId"`
	PeerID       string    `json:"peerId"`
	Token        string    `json:"token"`
	OriginSocket string    `json:"originSocket"`
	OriginLabel  string    `json:"originLabel"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type StoredCapability struct {
	Version      int          `json:"version"`
	Grant        BridgeGrant  `json:"grant"`
	Target       AttachTarget `json:"target"`
	BridgeSocket string       `json:"bridgeSocket"`
	WrittenAt    time.Time    `json:"writtenAt"`
}

func (g BridgeGrant) Validate() error {
	if g.Version != ProtocolVersion || !ValidAttachmentID(g.AttachmentID) ||
		!ValidIdentifier(g.PeerID) || !validBridgeToken(g.Token) ||
		!validSocketPath(g.OriginSocket) || !ValidText(g.OriginLabel) {
		return ErrInvalidFrame
	}

	if g.ExpiresAt.IsZero() {
		return ErrInvalidFrame
	}

	return nil
}

func (g BridgeGrant) Expired(now time.Time) bool {
	return g.ExpiresAt.IsZero() || !now.Before(g.ExpiresAt)
}

func (g BridgeGrant) Matches(token string, now time.Time) bool {
	if g.Expired(now) {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(g.Token), []byte(token)) == 1
}

func (c StoredCapability) Validate() error {
	if c.Version != ProtocolVersion {
		return ErrInvalidFrame
	}

	if err := c.Grant.Validate(); err != nil {
		return err
	}

	if err := c.Target.Validate(); err != nil {
		return err
	}

	if c.Target.PeerID != c.Grant.PeerID {
		return ErrPeerMismatch
	}

	if !validSocketPath(c.BridgeSocket) {
		return ErrInvalidFrame
	}

	if c.WrittenAt.IsZero() {
		return ErrInvalidFrame
	}

	return nil
}

func NewAttachmentID() (string, error) {
	return randomHex(attachmentIDBytes)
}

func NewBridgeToken() (string, error) {
	return randomHex(bridgeTokenBytes)
}

func ValidAttachmentID(value string) bool {
	return len(value) == attachmentIDBytes*2 && validHex(value)
}

func CapabilityName(attachmentID string) (string, error) {
	if !ValidAttachmentID(attachmentID) {
		return "", ErrUnsafePath
	}

	return capabilityPrefix + attachmentID + capabilitySuffix, nil
}

func BridgeSocketName(attachmentID string) (string, error) {
	if !ValidAttachmentID(attachmentID) {
		return "", ErrUnsafePath
	}

	return capabilityPrefix + attachmentID + socketSuffix, nil
}

func BridgeSocketPath(bridgeDir, attachmentID string) (string, error) {
	if !validSocketPath(bridgeDir) {
		return "", ErrUnsafePath
	}

	name, err := BridgeSocketName(attachmentID)
	if err != nil {
		return "", err
	}

	return filepath.Join(bridgeDir, name), nil
}

func WriteCapability(dir *privatefs.Dir, record StoredCapability) error {
	if err := record.Validate(); err != nil {
		return oops.In("remote").With("operation", "write_capability").Wrap(err)
	}

	name, err := CapabilityName(record.Grant.AttachmentID)
	if err != nil {
		return oops.In("remote").With("operation", "write_capability").Wrap(err)
	}

	encoded, err := json.Marshal(record)
	if err != nil {
		return oops.In("remote").With("operation", "write_capability").Wrap(err)
	}

	if len(encoded) > MaxCapabilityBytes {
		return oops.In("remote").With("operation", "write_capability").Wrap(ErrFrameTooLarge)
	}

	return writeFileAtomic(dir, name, encoded)
}

func ReadCapability(dir *privatefs.Dir, attachmentID string, now time.Time) (StoredCapability, error) {
	name, err := CapabilityName(attachmentID)
	if err != nil {
		return StoredCapability{}, oops.In("remote").With("operation", "read_capability").Wrap(err)
	}

	content, err := dir.Read(name, int64(MaxCapabilityBytes))
	if err != nil {
		return StoredCapability{}, oops.In("remote").With("operation", "read_capability").Wrap(err)
	}

	record, err := decodeStrict[StoredCapability](bytes.TrimSpace(content))
	if err != nil {
		return StoredCapability{}, oops.In("remote").With("operation", "read_capability").Wrap(err)
	}

	if err := record.Validate(); err != nil {
		return StoredCapability{}, oops.In("remote").With("operation", "read_capability").Wrap(err)
	}

	if record.Grant.AttachmentID != attachmentID {
		return StoredCapability{}, oops.In("remote").With("operation", "read_capability").Wrap(ErrCapabilityMismatch)
	}

	if record.Grant.Expired(now) {
		return StoredCapability{}, oops.In("remote").With("operation", "read_capability").Wrap(ErrCapabilityExpired)
	}

	return record, nil
}

func RemoveCapability(dir *privatefs.Dir, attachmentID string) error {
	name, err := CapabilityName(attachmentID)
	if err != nil {
		return err
	}

	return removeFile(dir, name)
}

func CheckSocketAbsent(dir *privatefs.Dir, attachmentID string) error {
	name, err := BridgeSocketName(attachmentID)
	if err != nil {
		return oops.In("remote").With("operation", "check_socket_absent").Wrap(err)
	}

	names, err := dir.Names()
	if err != nil {
		return oops.In("remote").With("operation", "check_socket_absent").Wrap(err)
	}

	if slices.Contains(names, name) {
		return oops.In("remote").With("operation", "check_socket_absent").With("socket_name", name).Wrap(ErrForeignSocketPath)
	}

	return nil
}

func validSocketPath(value string) bool {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" {
		return false
	}

	if strings.HasSuffix(value, "/") || !ValidText(value) {
		return false
	}

	return true
}

func validBridgeToken(value string) bool {
	return len(value) == bridgeTokenBytes*2 && validHex(value)
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", oops.In("remote").With("operation", "random_hex").Wrap(err)
	}

	return hex.EncodeToString(value), nil
}
