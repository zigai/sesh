//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

type ActionOriginOptions struct {
	Remote     config.RemoteConfig
	Connection config.RemoteConnectionConfig
	State      ConnectionState
}

func NewActionRequest(options ActionOriginOptions, target session.Session, action Action, newName, directory string) (ActionRequest, error) {
	if !options.Remote.Enabled || !PermissionsAllow(options.Remote.Permissions, action) {
		return ActionRequest{}, ErrRemoteDisabled
	}

	if options.Connection.ID != options.State.ID ||
		ConnectionDigest(options.Connection) != options.State.ConfigDigest || !options.State.Connected {
		return ActionRequest{}, ErrConnectionChanged
	}

	id, err := NewInvocationID()
	if err != nil {
		return ActionRequest{}, err
	}

	request := ActionRequest{
		InvocationID: id, IntentDigest: "", Operation: action,
		ConnectionID: options.Connection.ID, ConfigDigest: options.State.ConfigDigest,
		PeerID: options.State.PeerID, Kind: string(target.Kind),
		TmuxServerSocket:    target.TmuxServerSocket,
		TmuxServerPID:       target.RuntimeIdentity.TmuxServerPID,
		TmuxServerStartedAt: target.RuntimeIdentity.TmuxServerStartedAt,
		SessionID:           target.ID, SessionName: target.Name,
		Generation:       target.Generation,
		SessionCreatedAt: target.RuntimeIdentity.TmuxSessionCreatedAt,
		NewName:          newName, Directory: directory,
	}
	if action == ActionCreate {
		request.Kind = ""
		request.TmuxServerSocket = options.Connection.TmuxSocket
		request.TmuxServerPID = 0
		request.TmuxServerStartedAt = time.Time{}
		request.SessionID = ""
		request.Generation = ""
		request.SessionCreatedAt = time.Time{}
	}

	if target.Kind == session.KindSaved {
		request.TmuxServerSocket = options.Connection.TmuxSocket
		request.TmuxServerPID = 0
		request.TmuxServerStartedAt = time.Time{}
		request.SessionCreatedAt = time.Time{}
	}

	request.IntentDigest, err = IntentDigest(request)
	if err != nil {
		return ActionRequest{}, err
	}

	if err := ValidateActionRequest(request, false); err != nil {
		return ActionRequest{}, err
	}

	return request, nil
}

func RunAction(ctx context.Context, options ActionOriginOptions, request ActionRequest) (ActionResult, error) {
	if err := ValidateActionRequest(request, false); err != nil {
		return ActionResult{}, oops.In("remote").With("operation", "run_action").Wrap(err)
	}

	if request.ConnectionID != options.Connection.ID || request.ConfigDigest != options.State.ConfigDigest ||
		request.PeerID != options.State.PeerID || ConnectionDigest(options.Connection) != options.State.ConfigDigest {
		return ActionResult{}, oops.In("remote").With("operation", "run_action").Wrap(ErrConnectionChanged)
	}

	payload, err := EncodeActionRequest(request)
	if err != nil {
		return ActionResult{}, oops.In("remote").With("operation", "run_action").Wrap(err)
	}

	actionCtx, cancel := context.WithTimeout(ctx, ParsedDuration(options.Remote.ActionTimeout, defaultAction))
	defer cancel()

	command, err := NewSSHCommand(actionCtx, options.Remote, options.Connection, SSHCommandOptions{
		Interactive: false,
		Forward:     nil,
		Command:     nil,
	}, PeerEntryPoint(options.Connection, ChannelAction)...)
	if err != nil {
		return ActionResult{}, oops.In("remote").With("operation", "run_action").Wrap(err)
	}

	command.Stdin = bytes.NewReader(payload)

	diagnostics := NewDiagnosticsBuffer(maxDiagnosticsBytes)
	command.Stderr = diagnostics

	output, runErr := command.Output()
	if runErr != nil {
		cause := ClassifySSHFailure(actionCtx.Err(), diagnostics.String())

		return ActionResult{InvocationID: request.InvocationID, OK: false, Code: ActionCodeUnknownOutcome, DurableOperationID: ""},
			errors.Join(cause, oops.In("remote").With("operation", "run_action").Wrap(runErr))
	}

	result, err := DecodeActionResult(output)
	if err != nil {
		return ActionResult{InvocationID: request.InvocationID, OK: false, Code: ActionCodeUnknownOutcome, DurableOperationID: ""},
			oops.In("remote").With("operation", "run_action").Wrap(err)
	}

	if result.InvocationID != request.InvocationID {
		return ActionResult{InvocationID: request.InvocationID, OK: false, Code: ActionCodeUnknownOutcome, DurableOperationID: ""},
			oops.In("remote").With("operation", "run_action").Wrap(ErrInvocationConflict)
	}

	return result, nil
}
