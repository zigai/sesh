//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/privatefs"
)

type InvocationLookupRequest struct {
	Version      int    `json:"version"`
	PeerID       string `json:"peerId"`
	InvocationID string `json:"invocationId"`
}

func NewInvocationLookupRequest(options ActionOriginOptions, invocationID string) (InvocationLookupRequest, error) {
	if !options.Remote.Enabled {
		return InvocationLookupRequest{}, ErrRemoteDisabled
	}

	if options.Connection.ID != options.State.ID ||
		ConnectionDigest(options.Connection) != options.State.ConfigDigest || !ValidIdentifier(options.State.PeerID) {
		return InvocationLookupRequest{}, ErrConnectionChanged
	}

	request := InvocationLookupRequest{Version: ProtocolVersion, PeerID: options.State.PeerID, InvocationID: invocationID}
	if err := ValidateInvocationLookupRequest(request); err != nil {
		return InvocationLookupRequest{}, err
	}

	return request, nil
}

func ValidateInvocationLookupRequest(request InvocationLookupRequest) error {
	if request.Version != ProtocolVersion || !ValidIdentifier(request.PeerID) || !ValidInvocationID(request.InvocationID) {
		return ErrInvalidFrame
	}

	return nil
}

func EncodeInvocationLookupRequest(request InvocationLookupRequest) ([]byte, error) {
	if err := ValidateInvocationLookupRequest(request); err != nil {
		return nil, oops.In("remote").With("operation", "encode_invocation_lookup_request").Wrap(err)
	}

	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, oops.In("remote").With("operation", "encode_invocation_lookup_request").Wrap(err)
	}

	if len(encoded) > MaxActionBytes {
		return nil, oops.In("remote").With("operation", "encode_invocation_lookup_request").Wrap(ErrFrameTooLarge)
	}

	return encoded, nil
}

func DecodeInvocationLookupRequest(payload []byte) (InvocationLookupRequest, error) {
	if len(payload) == 0 || len(payload) > MaxActionBytes {
		return InvocationLookupRequest{}, ErrInvalidFrame
	}

	request, err := decodeStrict[InvocationLookupRequest](bytes.TrimSpace(payload))
	if err != nil {
		return InvocationLookupRequest{}, err
	}

	return request, ValidateInvocationLookupRequest(request)
}

func ExecutePeerLookup(dir *privatefs.Dir, peerID string, request InvocationLookupRequest) ActionResult {
	if err := ValidateInvocationLookupRequest(request); err != nil || request.PeerID != peerID {
		return ActionResult{InvocationID: request.InvocationID, Action: "", OK: false, Code: action.CodeTargetChanged, Output: nil}
	}

	result, err := LookupPeerInvocation(dir, request.InvocationID)
	if errors.Is(err, ErrInvocationNotFound) {
		return ActionResult{InvocationID: request.InvocationID, Action: "", OK: false, Code: action.CodeNotFound, Output: nil}
	}

	if err != nil {
		return ActionResult{InvocationID: request.InvocationID, Action: "", OK: false, Code: action.CodeUnavailable, Output: nil}
	}

	return result
}

func RunInvocationLookup(ctx context.Context, options ActionOriginOptions, request InvocationLookupRequest) (ActionResult, error) {
	if err := ValidateInvocationLookupRequest(request); err != nil {
		return ActionResult{}, oops.In("remote").With("operation", "run_invocation_lookup").Wrap(err)
	}

	if request.PeerID != options.State.PeerID || options.Connection.ID != options.State.ID ||
		ConnectionDigest(options.Connection) != options.State.ConfigDigest {
		return ActionResult{}, oops.In("remote").With("operation", "run_invocation_lookup").Wrap(ErrConnectionChanged)
	}

	payload, err := EncodeInvocationLookupRequest(request)
	if err != nil {
		return ActionResult{}, oops.In("remote").With("operation", "run_invocation_lookup").Wrap(err)
	}

	lookupCtx, cancel := context.WithTimeout(ctx, ParsedDuration(options.Remote.ActionTimeout, defaultAction))
	defer cancel()

	command, err := NewSSHCommand(lookupCtx, options.Remote, options.Connection, SSHCommandOptions{
		Interactive: false, Forward: nil, Command: nil,
	}, PeerEntryPoint(options.Connection, ChannelLookup)...)
	if err != nil {
		return ActionResult{}, oops.In("remote").With("operation", "run_invocation_lookup").Wrap(err)
	}

	command.Stdin = bytes.NewReader(payload)
	diagnostics := NewDiagnosticsBuffer(maxDiagnosticsBytes)
	command.Stderr = diagnostics

	output, runErr := command.Output()
	if runErr != nil {
		cause := ClassifySSHFailure(lookupCtx.Err(), diagnostics.String())

		return ActionResult{InvocationID: request.InvocationID, Action: "", OK: false, Code: action.CodeUnavailable, Output: nil},
			errors.Join(cause, oops.In("remote").With("operation", "run_invocation_lookup").Wrap(runErr))
	}

	result, err := DecodeActionResult(output)
	if err != nil {
		return ActionResult{}, oops.In("remote").With("operation", "run_invocation_lookup").Wrap(err)
	}

	if result.InvocationID != request.InvocationID {
		return ActionResult{}, oops.In("remote").With("operation", "run_invocation_lookup").Wrap(ErrInvocationConflict)
	}

	return result, nil
}
