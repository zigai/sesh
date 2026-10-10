//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"slices"
	"sync"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
)

var ErrActionNotOffered = errors.New("remote peer does not accept this action")

type ActionOriginOptions struct {
	Remote     config.RemoteConfig
	Connection config.RemoteConnectionConfig
	State      ConnectionState
}

// Peer dispatches actions to configured remote connections over SSH. It offers the actions each
// peer advertised in its last observed inventory and checks the current catalog before sending.
type Peer struct {
	remote config.RemoteConfig
	load   func() (Catalog, error)
	mu     sync.Mutex
	offers map[string][]action.ID
}

func NewPeer(remote config.RemoteConfig, load func() (Catalog, error)) *Peer {
	return &Peer{remote: remote, load: load, mu: sync.Mutex{}, offers: map[string][]action.ID{}}
}

// NewCatalogPeer dispatches through the catalog the remote monitor keeps under cacheHome.
func NewCatalogPeer(remote config.RemoteConfig, cacheHome string) *Peer {
	return NewPeer(remote, func() (Catalog, error) { return ReadCatalog(cacheHome) })
}

// Observe records the actions each connected peer accepts.
func (peer *Peer) Observe(catalog Catalog) {
	offers := make(map[string][]action.ID, len(catalog.Connections))
	for id, state := range catalog.Connections {
		if state.Connected {
			offers[id] = slices.Clone(state.Snapshot.Actions)
		}
	}

	peer.mu.Lock()
	peer.offers = offers
	peer.mu.Unlock()
}

func (peer *Peer) Accepts(at action.Location, id action.ID) bool {
	if at.Kind != action.LocationPeer || !peer.remote.Enabled {
		return false
	}

	peer.mu.Lock()
	defer peer.mu.Unlock()

	return slices.Contains(peer.offers[at.Connection], id)
}

func (peer *Peer) Dispatch(ctx context.Context, invocation action.Invocation) (any, error) {
	entry, ok := action.Lookup(invocation.Action)
	if !ok {
		return nil, action.Wrap(action.CodeUnsupported, action.ErrUnknownAction)
	}

	if invocation.At.Kind != action.LocationPeer {
		return nil, action.Wrap(action.CodeUnsupported, action.ErrLocationRejected)
	}

	options, err := peer.originOptions(invocation.At.Connection)
	if err != nil {
		return nil, err
	}

	if !slices.Contains(options.State.Snapshot.Actions, invocation.Action) {
		return nil, action.Wrap(action.CodeForbidden, ErrActionNotOffered)
	}

	input, err := entry.EncodeInput(invocation.Input)
	if err != nil {
		return nil, action.Wrap(action.CodeInvalidArgument, err)
	}

	request, err := NewActionRequest(options, invocation.Action, input)
	if err != nil {
		return nil, action.Wrap(action.CodeUnavailable, err)
	}

	result, err := RunAction(ctx, options, request)
	if err != nil {
		return nil, action.Wrap(action.CodeUncertain, &PeerError{Code: action.CodeUncertain, InvocationID: request.InvocationID, Err: err})
	}

	var output any
	if len(result.Output) > 0 {
		output, err = entry.DecodeOutput(result.Output)
		if err != nil {
			output = nil
		}
	}

	if result.OK {
		return output, nil
	}

	return output, action.Wrap(result.Code, &PeerError{Code: result.Code, InvocationID: result.InvocationID, Err: nil})
}

func (peer *Peer) originOptions(connectionID string) (ActionOriginOptions, error) {
	connection, err := FindConnection(peer.remote, connectionID)
	if err != nil {
		return ActionOriginOptions{}, action.Wrap(action.CodeNotFound, err)
	}

	catalog, err := peer.load()
	if err != nil {
		return ActionOriginOptions{}, action.Wrap(action.CodeUnavailable, err)
	}

	state, ok := catalog.Connections[connection.ID]
	if !ok {
		return ActionOriginOptions{}, action.Wrap(action.CodeUnavailable, ErrConnectionChanged)
	}

	return ActionOriginOptions{Remote: peer.remote, Connection: connection, State: state}, nil
}

func NewActionRequest(opts ActionOriginOptions, id action.ID, input jsontext.Value) (ActionRequest, error) {
	if !opts.Remote.Enabled {
		return ActionRequest{}, ErrRemoteDisabled
	}

	if opts.Connection.ID != opts.State.ID ||
		ConnectionDigest(opts.Connection) != opts.State.ConfigDigest || !opts.State.Connected {
		return ActionRequest{}, ErrConnectionChanged
	}

	invocationID, err := NewInvocationID()
	if err != nil {
		return ActionRequest{}, err
	}

	request := ActionRequest{
		InvocationID: invocationID,
		IntentDigest: "",
		Action:       id,
		ConnectionID: opts.Connection.ID,
		ConfigDigest: opts.State.ConfigDigest,
		PeerID:       opts.State.PeerID,
		Socket:       opts.Connection.TmuxSocket,
		Input:        input,
	}

	request.IntentDigest, err = IntentDigest(request)
	if err != nil {
		return ActionRequest{}, err
	}

	if err := ValidateActionRequest(request); err != nil {
		return ActionRequest{}, err
	}

	return request, nil
}

// RunAction sends one invocation over SSH. When the answer is lost the outcome is unknown, and
// `sesh remote lookup` reads the receipt the peer kept.
func RunAction(ctx context.Context, opts ActionOriginOptions, request ActionRequest) (ActionResult, error) {
	if err := ValidateActionRequest(request); err != nil {
		return ActionResult{}, oops.In("remote").With("operation", "run_action").Wrap(err)
	}

	if request.ConnectionID != opts.Connection.ID || request.ConfigDigest != opts.State.ConfigDigest ||
		request.PeerID != opts.State.PeerID || ConnectionDigest(opts.Connection) != opts.State.ConfigDigest {
		return ActionResult{}, oops.In("remote").With("operation", "run_action").Wrap(ErrConnectionChanged)
	}

	payload, err := EncodeActionRequest(request)
	if err != nil {
		return ActionResult{}, oops.In("remote").With("operation", "run_action").Wrap(err)
	}

	actionCtx, cancel := context.WithTimeout(ctx, ParsedDuration(opts.Remote.ActionTimeout, defaultAction))
	defer cancel()

	command, err := NewSSHCommand(actionCtx, opts.Remote, opts.Connection, SSHCommandOptions{
		Interactive: false,
		Forward:     nil,
		Command:     nil,
	}, PeerEntryPoint(opts.Connection, ChannelAction)...)
	if err != nil {
		return ActionResult{}, oops.In("remote").With("operation", "run_action").Wrap(err)
	}

	command.Stdin = bytes.NewReader(payload)

	diagnostics := NewDiagnosticsBuffer(maxDiagnosticsBytes)
	command.Stderr = diagnostics

	uncertain := ActionResult{InvocationID: request.InvocationID, Action: request.Action, OK: false, Code: action.CodeUncertain, Output: nil}

	output, runErr := command.Output()
	if runErr != nil {
		cause := ClassifySSHFailure(actionCtx.Err(), diagnostics.String())

		return uncertain, errors.Join(cause, oops.In("remote").With("operation", "run_action").With("invocation", request.InvocationID).Wrap(runErr))
	}

	result, err := DecodeActionResult(output)
	if err != nil {
		return uncertain, oops.In("remote").With("operation", "run_action").With("invocation", request.InvocationID).Wrap(err)
	}

	if result.InvocationID != request.InvocationID || result.Action != request.Action {
		return uncertain, oops.In("remote").With("operation", "run_action").With("invocation", request.InvocationID).Wrap(ErrInvocationConflict)
	}

	return result, nil
}
