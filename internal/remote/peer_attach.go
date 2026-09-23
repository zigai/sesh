//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/session"
)

var (
	errNilAttachStreams = errors.New("remote attach requires terminal streams")
	errNilBridgeDir     = errors.New("remote attach requires the peer bridge directory")
)

type AttachStreams struct {
	In  *os.File
	Out *os.File
	Err *os.File
}

type AttachPrepareResult struct {
	Version      int       `json:"version"`
	AttachmentID string    `json:"attachmentId"`
	BridgeSocket string    `json:"bridgeSocket"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type PaneOwner interface {
	PaneSession(ctx context.Context, paneID string) (string, error)
}

type PeerAttachOptions struct {
	PeerID    string
	BridgeDir string
	Live      LiveServer
	PaneOwner PaneOwner
	Now       func() time.Time
}

type OriginAttacher interface {
	AttachWithOrigin(ctx context.Context, record StoredCapability, streams AttachStreams) error
}

func (result AttachPrepareResult) Validate() error {
	if result.Version != ProtocolVersion || !ValidAttachmentID(result.AttachmentID) ||
		!validSocketPath(result.BridgeSocket) || result.ExpiresAt.IsZero() {
		return ErrInvalidFrame
	}

	return nil
}

func ValidateAttachTarget(target AttachTarget, sessions []session.Session) (session.Session, error) {
	for _, candidate := range sessions {
		if candidate.ID != target.SessionID || candidate.Name != target.SessionName {
			continue
		}

		if candidate.TmuxServerSocket != target.TmuxServerSocket ||
			candidate.RuntimeIdentity.TmuxServerPID != target.TmuxServerPID ||
			!candidate.RuntimeIdentity.TmuxServerStartedAt.Equal(target.TmuxServerStartedAt) ||
			!candidate.RuntimeIdentity.TmuxSessionCreatedAt.Equal(target.SessionCreatedAt) {
			return session.Session{}, ErrTargetChanged
		}

		return candidate, nil
	}

	return session.Session{}, oops.In("remote").With("operation", "validate_attach_target").Wrap(ErrTargetChanged)
}

func EncodeAttachPrepareResult(result AttachPrepareResult) ([]byte, error) {
	if err := result.Validate(); err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, oops.In("remote").With("operation", "encode_attach_prepare_result").Wrap(err)
	}

	if len(encoded) > MaxBootstrapBytes {
		return nil, oops.In("remote").With("operation", "encode_attach_prepare_result").Wrap(ErrFrameTooLarge)
	}

	return encoded, nil
}

func DecodeAttachPrepareResult(payload []byte) (AttachPrepareResult, error) {
	result, err := decodeStrict[AttachPrepareResult](bytes.TrimSpace(payload))
	if err != nil {
		return AttachPrepareResult{}, err
	}

	if err := result.Validate(); err != nil {
		return AttachPrepareResult{}, err
	}

	return result, nil
}

func RunPeerAttachPrepare(ctx context.Context, options PeerAttachOptions, bootstrap AttachBootstrap) (AttachPrepareResult, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}

	socketPath, err := validateAttachPreflight(options, bootstrap, now())
	if err != nil {
		return AttachPrepareResult{}, err
	}

	dir, err := openPrivateDir(options.BridgeDir, true)
	if err != nil {
		return AttachPrepareResult{}, err
	}
	defer func() { _ = dir.Close() }()

	if err := CheckSocketAbsent(dir, bootstrap.Capability.AttachmentID); err != nil {
		return AttachPrepareResult{}, err
	}

	if err := validateTargetAndPane(ctx, options, bootstrap.Target); err != nil {
		return AttachPrepareResult{}, err
	}

	record := StoredCapability{
		Version:      ProtocolVersion,
		Grant:        bootstrap.Capability,
		Target:       bootstrap.Target,
		BridgeSocket: socketPath,
		WrittenAt:    now().UTC(),
	}
	if err := WriteCapability(dir, record); err != nil {
		return AttachPrepareResult{}, err
	}

	return AttachPrepareResult{
		Version:      ProtocolVersion,
		AttachmentID: bootstrap.Capability.AttachmentID,
		BridgeSocket: socketPath,
		ExpiresAt:    bootstrap.Capability.ExpiresAt.UTC(),
	}, nil
}

func RunPeerAttach(ctx context.Context, options PeerAttachOptions, attachmentID string, streams AttachStreams) (resultErr error) {
	if !ValidAttachmentID(attachmentID) {
		return oops.In("remote").With("operation", "run_peer_attach").Wrap(ErrInvalidFrame)
	}

	if options.Live == nil {
		return oops.In("remote").With("operation", "run_peer_attach").Wrap(ErrIncompleteDependencies)
	}

	if streams.In == nil || streams.Out == nil || streams.Err == nil {
		return oops.In("remote").With("operation", "run_peer_attach").Wrap(errNilAttachStreams)
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}

	dir, err := openPrivateDir(options.BridgeDir, false)
	if err != nil {
		return oops.In("remote").With("operation", "run_peer_attach").Wrap(err)
	}

	defer func() {
		resultErr = errors.Join(resultErr, dir.Close())
	}()

	record, err := ReadCapability(dir, attachmentID, now())
	if err != nil {
		return oops.In("remote").With("operation", "run_peer_attach").Wrap(err)
	}

	if record.Grant.PeerID != options.PeerID {
		return oops.In("remote").With("operation", "peer_attach").With("observed_peer_id", record.Grant.PeerID).Wrap(ErrPeerMismatch)
	}

	if err := validateTargetAndPane(ctx, options, record.Target); err != nil {
		return oops.In("remote").With("operation", "run_peer_attach").Wrap(err)
	}

	defer func() {
		resultErr = errors.Join(resultErr, RemoveCapability(dir, attachmentID))
	}()

	if attachErr := attachPeerTerminal(ctx, options.Live, record, streams); attachErr != nil {
		return oops.In("remote").With("operation", "run_peer_attach").Wrap(attachErr)
	}

	return nil
}

func CapabilityReader(stateHome string) (*privatefs.Dir, error) {
	dir, err := BridgeDir(stateHome)
	if err != nil {
		return nil, err
	}

	return openPrivateDir(dir, false)
}

func attachPeerTerminal(ctx context.Context, live LiveServer, record StoredCapability, streams AttachStreams) error {
	var err error
	if attacher, ok := live.(OriginAttacher); ok {
		err = attacher.AttachWithOrigin(ctx, record, streams)
	} else {
		err = live.Attach(ctx, record.Target.SessionID, streams)
	}

	if err != nil {
		return oops.In("remote").With("operation", "attach_peer_terminal").Wrap(err)
	}

	return nil
}

func validateCapabilityWindow(grant BridgeGrant, now time.Time) error {
	if err := grant.Validate(); err != nil {
		return err
	}

	if grant.Expired(now) {
		return oops.In("remote").With("operation", "validate_capability_window").Wrap(ErrCapabilityExpired)
	}

	if grant.ExpiresAt.Sub(now) > time.Duration(maxAttachTimeoutMillis)*time.Millisecond {
		return oops.In("remote").With("operation", "validate_capability_window").Wrap(ErrInvalidFrame)
	}

	return nil
}

func peerBridgeSocketPath(bridgeDir string, bootstrap AttachBootstrap) (string, error) {
	if bridgeDir == "" || !validSocketPath(bridgeDir) {
		return "", oops.In("remote").With("operation", "peer_bridge_socket_path").Wrap(errNilBridgeDir)
	}

	expected, err := BridgeSocketPath(bridgeDir, bootstrap.Capability.AttachmentID)
	if err != nil {
		return "", oops.In("remote").With("operation", "peer_bridge_socket_path").Wrap(err)
	}

	if bootstrap.BridgeSocket != expected {
		return "", oops.In("remote").With("operation", "peer_bridge_socket_path").With("bridge_socket", filepath.Base(bootstrap.BridgeSocket)).Wrap(ErrUnsafePath)
	}

	return expected, nil
}

func validatePaneOwnership(ctx context.Context, owner PaneOwner, validated session.Session, paneID string) error {
	if paneID == "" {
		return nil
	}

	if owner != nil {
		resolved, err := owner.PaneSession(ctx, paneID)
		if err != nil {
			return oops.In("remote").With("operation", "validate_pane_ownership").Wrap(err)
		}

		if resolved != validated.ID {
			return oops.In("remote").With("operation", "validate_pane_ownership").Wrap(ErrTargetChanged)
		}

		return nil
	}

	if validated.ActivePaneID == paneID {
		return nil
	}

	for _, agent := range validated.Agents {
		if agent.PaneID == paneID {
			return nil
		}
	}

	return oops.In("remote").With("operation", "validate_pane_ownership").Wrap(ErrTargetChanged)
}

func validateAttachPreflight(options PeerAttachOptions, bootstrap AttachBootstrap, now time.Time) (string, error) {
	if err := bootstrap.Validate(); err != nil {
		return "", err
	}

	if bootstrap.Target.PeerID != options.PeerID {
		return "", oops.In("remote").With("operation", "validate_attach_preflight").With("observed_peer_id", bootstrap.Target.PeerID).Wrap(ErrPeerMismatch)
	}

	if options.Live == nil {
		return "", oops.In("remote").With("operation", "validate_attach_preflight").Wrap(ErrIncompleteDependencies)
	}

	if err := validateCapabilityWindow(bootstrap.Capability, now); err != nil {
		return "", err
	}

	return peerBridgeSocketPath(options.BridgeDir, bootstrap)
}

func validateTargetAndPane(ctx context.Context, options PeerAttachOptions, target AttachTarget) error {
	sessions, err := options.Live.Sessions(ctx)
	if err != nil {
		return oops.In("remote").With("operation", "validate_target_and_pane").Wrap(err)
	}

	validated, err := ValidateAttachTarget(target, sessions)
	if err != nil {
		return err
	}

	return validatePaneOwnership(ctx, options.PaneOwner, validated, target.PaneID)
}
