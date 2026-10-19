//go:build linux || darwin

package remote

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"io/fs"
	"time"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/privatefs"
)

// PeerActionOptions configures how a peer runs invocations. Allowed is the peer's own policy;
// Dispatcher runs admitted actions on the peer.
type PeerActionOptions struct {
	PeerID     string
	Receipts   *privatefs.Dir
	Allowed    func(action.ID) bool
	Dispatcher action.Dispatcher
	Now        func() time.Time
}

// ExecutePeerAction runs one invocation at most once. A repeated invocation ID returns the
// recorded result instead of running the action again.
func ExecutePeerAction(ctx context.Context, opts PeerActionOptions, request ActionRequest) ActionResult {
	refused := func(code action.Code) ActionResult {
		return ActionResult{InvocationID: request.InvocationID, Action: knownAction(request.Action), OK: false, Code: code, Output: nil}
	}

	entry, input, code := acceptRequest(opts, request)
	if code != "" {
		return refused(code)
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	invocation, admitted, err := AdmitInvocation(ctx, opts.Receipts, request, now())
	if err != nil {
		return refused(admissionCode(err))
	}

	if !admitted {
		return invocation.Result()
	}

	result := runPeerAction(ctx, opts.Dispatcher, entry, request, input)

	if err := ResolveInvocation(ctx, opts.Receipts, invocation.ID, OutcomeOf(result), result.Output, now()); err != nil {
		return ActionResult{InvocationID: invocation.ID, Action: request.Action, OK: false, Code: action.CodeUncertain, Output: result.Output}
	}

	return result
}

func LookupPeerInvocation(dir *privatefs.Dir, invocationID string) (ActionResult, error) {
	invocation, err := LookupInvocation(dir, invocationID)
	if errors.Is(err, fs.ErrNotExist) {
		return ActionResult{}, ErrInvocationNotFound
	}

	if err != nil {
		return ActionResult{}, err
	}

	return invocation.Result(), nil
}

func acceptRequest(opts PeerActionOptions, request ActionRequest) (action.Entry, any, action.Code) {
	if ValidateActionRequest(request) != nil || VerifyIntentDigest(request) != nil {
		return nil, nil, action.CodeInvalidArgument
	}

	if request.PeerID != opts.PeerID {
		return nil, nil, action.CodeTargetChanged
	}

	entry, ok := action.Lookup(request.Action)
	if !ok || opts.Dispatcher == nil {
		return nil, nil, action.CodeUnsupported
	}

	if opts.Allowed == nil || !opts.Allowed(request.Action) {
		return nil, nil, action.CodeForbidden
	}

	input, err := entry.DecodeInput(request.Input)
	if err != nil {
		return nil, nil, action.CodeInvalidArgument
	}

	if opts.Receipts == nil {
		return nil, nil, action.CodeUnavailable
	}

	return entry, input, ""
}

func runPeerAction(ctx context.Context, dispatcher action.Dispatcher, entry action.Entry, request ActionRequest, input any) ActionResult {
	output, runErr := dispatcher.Dispatch(ctx, action.Invocation{
		Action: request.Action,
		At:     action.Local(),
		Caller: action.Caller{Client: "", Pane: "", Session: "", Width: "", Height: ""},
		Input:  input,
	})

	var encoded jsontext.Value

	if output != nil {
		data, err := entry.EncodeOutput(output)
		if err == nil {
			encoded = data
		}
	}

	if runErr == nil {
		return ActionResult{InvocationID: request.InvocationID, Action: request.Action, OK: true, Code: "", Output: encoded}
	}

	code := action.CodeOf(runErr)
	if ctx.Err() != nil {
		code = action.CodeUncertain
	}

	return ActionResult{InvocationID: request.InvocationID, Action: request.Action, OK: false, Code: code, Output: encoded}
}

func knownAction(id action.ID) action.ID {
	if id.Valid() {
		return id
	}

	return ""
}

func admissionCode(err error) action.Code {
	switch {
	case errors.Is(err, ErrInvocationConflict):
		return action.CodeConflict
	case errors.Is(err, ErrInvalidFrame):
		return action.CodeInvalidArgument
	default:
		return action.CodeUnavailable
	}
}
