//go:build linux || darwin

package remote

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"time"

	"github.com/samber/oops"
)

const (
	channelCommand = "remote"
	peerCommand    = "peer"

	ChannelStream        = "stream"
	ChannelAction        = "action"
	ChannelLookup        = "lookup"
	ChannelAttachPrepare = "attach-prepare"
	ChannelAttach        = "attach"

	maxAttachTimeoutMillis = 24 * 60 * 60 * 1000
)

// AttachmentPaneOption marks a local pane while it shows a remote attachment, so sesh keys pressed
// there go on to the remote tmux.
const AttachmentPaneOption = "@sesh_remote"

var ErrTargetChanged = errors.New("remote attach target changed")

type Channel string

type AttachTarget struct {
	PeerID              string    `json:"peerId"`
	TmuxServerSocket    string    `json:"tmuxServerSocket"`
	TmuxServerPID       int       `json:"tmuxServerPid"`
	TmuxServerStartedAt time.Time `json:"tmuxServerStartedAt"`
	SessionID           string    `json:"sessionId"`
	SessionName         string    `json:"sessionName"`
	SessionCreatedAt    time.Time `json:"sessionCreatedAt"`
	PaneID              string    `json:"paneId"`
	CatalogSequence     uint64    `json:"catalogSequence"`
}

type StreamBootstrap struct {
	Version           int    `json:"version"`
	TmuxSocket        string `json:"tmuxSocket"`
	PollInterval      string `json:"pollInterval"`
	HeartbeatInterval string `json:"heartbeatInterval"`
}

type AttachBootstrap struct {
	Version       int          `json:"version"`
	Channel       Channel      `json:"channel"`
	Target        AttachTarget `json:"target"`
	Capability    BridgeGrant  `json:"capability"`
	BridgeSocket  string       `json:"bridgeSocket"`
	TimeoutMillis int          `json:"timeoutMillis"`
}

func (b StreamBootstrap) Validate() error {
	if b.Version != ProtocolVersion || !ValidText(b.TmuxSocket) {
		return ErrInvalidFrame
	}

	if b.PollInterval != "" && !validDuration(b.PollInterval) {
		return ErrInvalidFrame
	}

	if b.HeartbeatInterval != "" && !validDuration(b.HeartbeatInterval) {
		return ErrInvalidFrame
	}

	return nil
}

func (t AttachTarget) Validate() error {
	if !ValidIdentifier(t.PeerID) || !ValidText(t.TmuxServerSocket) ||
		!ValidIdentifier(t.SessionID) || !ValidIdentifier(t.SessionName) {
		return ErrInvalidFrame
	}

	if t.TmuxServerPID <= 0 || t.TmuxServerStartedAt.IsZero() || t.SessionCreatedAt.IsZero() {
		return ErrInvalidFrame
	}

	if t.PaneID != "" && !ValidIdentifier(t.PaneID) {
		return ErrInvalidFrame
	}

	return nil
}

func (b AttachBootstrap) Validate() error {
	if b.Version != ProtocolVersion || b.Channel != ChannelAttachPrepare {
		return ErrInvalidFrame
	}

	if err := b.Target.Validate(); err != nil {
		return err
	}

	if b.Target.PeerID != b.Capability.PeerID {
		return ErrPeerMismatch
	}

	if err := b.Capability.Validate(); err != nil {
		return err
	}

	if b.BridgeSocket != "" && !validSocketPath(b.BridgeSocket) {
		return ErrInvalidFrame
	}

	if b.TimeoutMillis < 0 || b.TimeoutMillis > maxAttachTimeoutMillis {
		return ErrInvalidFrame
	}

	return nil
}

func EncodeStreamBootstrap(bootstrap StreamBootstrap) ([]byte, error) {
	return encodeBounded(bootstrap, bootstrap.Validate, MaxBootstrapBytes)
}

func DecodeStreamBootstrap(payload []byte) (StreamBootstrap, error) {
	bootstrap, err := decodeStrict[StreamBootstrap](bytes.TrimSpace(payload))
	if err != nil {
		return StreamBootstrap{}, err
	}

	if err := bootstrap.Validate(); err != nil {
		return StreamBootstrap{}, err
	}

	return bootstrap, nil
}

func EncodeAttachBootstrap(bootstrap AttachBootstrap) ([]byte, error) {
	return encodeBounded(bootstrap, bootstrap.Validate, MaxBootstrapBytes)
}

func DecodeAttachBootstrap(payload []byte) (AttachBootstrap, error) {
	bootstrap, err := decodeStrict[AttachBootstrap](bytes.TrimSpace(payload))
	if err != nil {
		return AttachBootstrap{}, err
	}

	if err := bootstrap.Validate(); err != nil {
		return AttachBootstrap{}, err
	}

	return bootstrap, nil
}

func encodeBounded[T any](value T, validate func() error, limit int) ([]byte, error) {
	if err := validate(); err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, oops.In("remote").With("operation", "encode_bootstrap").Wrap(err)
	}

	if len(encoded) > limit {
		return nil, oops.In("remote").With("operation", "encode_bootstrap").Wrap(ErrFrameTooLarge)
	}

	return encoded, nil
}

func validDuration(value string) bool {
	parsed, err := time.ParseDuration(value)

	return err == nil && parsed > 0
}
