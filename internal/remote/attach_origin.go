//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
)

const (
	defaultAttachTimeout = 2 * time.Minute
	attachResultGrace    = 30 * time.Second
)

var errAttachResultMissing = errors.New("remote attach prepare returned no result")

type AttachOriginOptions struct {
	Remote       config.RemoteConfig
	Connection   config.RemoteConnectionConfig
	BridgeDir    string
	OriginSocket string
	OriginLabel  string
	Target       AttachTarget
	Timeout      time.Duration
	Now          func() time.Time
}

type AttachCommandOptions struct {
	Streams AttachStreams
	Command func(ctx context.Context, executable string, arguments ...string) *exec.Cmd
}

func PrepareAttachment(opts AttachOriginOptions) (AttachBootstrap, error) {
	if !opts.Remote.Enabled {
		return AttachBootstrap{}, ErrRemoteDisabled
	}

	if err := opts.Target.Validate(); err != nil {
		return AttachBootstrap{}, err
	}

	if opts.Connection.ID == "" || opts.Target.PeerID == "" {
		return AttachBootstrap{}, ErrIncompleteDependencies
	}

	if !validSocketPath(opts.OriginSocket) || !validSocketPath(opts.BridgeDir) {
		return AttachBootstrap{}, ErrUnsafePath
	}

	attachmentID, err := NewAttachmentID()
	if err != nil {
		return AttachBootstrap{}, err
	}

	token, err := NewBridgeToken()
	if err != nil {
		return AttachBootstrap{}, err
	}

	bridgeSocket, err := BridgeSocketPath(opts.BridgeDir, attachmentID)
	if err != nil {
		return AttachBootstrap{}, err
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	timeout := positiveDuration(opts.Timeout, defaultAttachTimeout)

	return AttachBootstrap{
		Version: ProtocolVersion,
		Channel: ChannelAttachPrepare,
		Target:  opts.Target,
		Capability: BridgeGrant{
			Version:      ProtocolVersion,
			AttachmentID: attachmentID,
			PeerID:       opts.Target.PeerID,
			Token:        token,
			OriginSocket: opts.OriginSocket,
			OriginLabel:  opts.OriginLabel,
			ExpiresAt:    now().Add(timeout).UTC(),
		},
		BridgeSocket:  bridgeSocket,
		TimeoutMillis: int(timeout.Milliseconds()),
	}, nil
}

func NewAttachPrepareCommand(ctx context.Context, remote config.RemoteConfig, connection config.RemoteConnectionConfig, bootstrap AttachBootstrap, opts AttachCommandOptions) (*exec.Cmd, error) {
	payload, err := EncodeAttachBootstrap(bootstrap)
	if err != nil {
		return nil, err
	}

	command, err := NewSSHCommand(ctx, remote, connection, SSHCommandOptions{
		Interactive: false,
		Forward:     nil,
		Command:     opts.Command,
	}, PeerEntryPoint(connection, ChannelAttachPrepare)...)
	if err != nil {
		return nil, err
	}

	command.Stdin = bytes.NewReader(payload)

	return command, nil
}

func NewAttachCommand(ctx context.Context, remote config.RemoteConfig, connection config.RemoteConnectionConfig, bootstrap AttachBootstrap, opts AttachCommandOptions) (*exec.Cmd, error) {
	if err := bootstrap.Validate(); err != nil {
		return nil, err
	}

	if opts.Streams.In == nil || opts.Streams.Out == nil || opts.Streams.Err == nil {
		return nil, errNilAttachStreams
	}

	arguments := append(PeerEntryPoint(connection, ChannelAttach), bootstrap.Capability.AttachmentID)

	command, err := NewSSHCommand(ctx, remote, connection, SSHCommandOptions{
		Interactive: true,
		Forward: []SocketForward{{
			RemotePath: bootstrap.BridgeSocket,
			LocalPath:  bootstrap.Capability.OriginSocket,
		}},
		Command: opts.Command,
	}, arguments...)
	if err != nil {
		return nil, err
	}

	command.Stdin = opts.Streams.In
	command.Stdout = opts.Streams.Out
	command.Stderr = opts.Streams.Err

	return command, nil
}

func RunAttachPrepare(ctx context.Context, remote config.RemoteConfig, connection config.RemoteConnectionConfig, bootstrap AttachBootstrap, opts AttachCommandOptions) (AttachPrepareResult, error) {
	prepareContext, cancel := context.WithTimeout(ctx, ParsedDuration(remote.ConnectTimeout, defaultConnect)+attachResultGrace)
	defer cancel()

	command, err := NewAttachPrepareCommand(prepareContext, remote, connection, bootstrap, opts)
	if err != nil {
		return AttachPrepareResult{}, err
	}

	diagnostics := NewDiagnosticsBuffer(maxDiagnosticsBytes)

	command.Stderr = diagnostics

	output, err := command.Output()
	if err != nil {
		return AttachPrepareResult{}, errors.Join(ClassifySSHFailure(prepareContext.Err(), diagnostics.String()),
			oops.In("remote").With("operation", "run_attach_prepare").Wrap(err))
	}

	if len(output) == 0 {
		return AttachPrepareResult{}, oops.In("remote").With("operation", "run_attach_prepare").Wrap(errAttachResultMissing)
	}

	result, err := DecodeAttachPrepareResult(output)
	if err != nil {
		return AttachPrepareResult{}, oops.In("remote").With("operation", "run_attach_prepare").Wrap(err)
	}

	if result.AttachmentID != bootstrap.Capability.AttachmentID || result.BridgeSocket != bootstrap.BridgeSocket {
		return AttachPrepareResult{}, oops.In("remote").With("operation", "run_attach_prepare").Wrap(ErrCapabilityMismatch)
	}

	return result, nil
}
