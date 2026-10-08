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
func ExecutePeerAction(ctx context.Context, options PeerActionOptions, request ActionRequest) ActionResult {
	refused := func(code action.Code) ActionResult {
		return ActionResult{InvocationID: request.InvocationID, Action: knownAction(request.Action), OK: false, Code: code, Output: nil}
	}

	entry, input, code := acceptRequest(options, request)
	if code != "" {
		return refused(code)
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}

	invocation, admitted, err := AdmitInvocation(ctx, options.Receipts, request, now())
	if err != nil {
		return refused(admissionCode(err))
	}

	if !admitted {
		return invocation.Result()
	}

	result := runPeerAction(ctx, options.Dispatcher, entry, request, input)

	if err := ResolveInvocation(ctx, options.Receipts, invocation.ID, OutcomeOf(result), result.Output, now()); err != nil {
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

// acceptRequest decodes a request this peer will run, or reports the code that refuses it. A
// request is checked before it is admitted, so a refused request leaves no receipt.
func acceptRequest(options PeerActionOptions, request ActionRequest) (action.Entry, any, action.Code) {
	if ValidateActionRequest(request) != nil || VerifyIntentDigest(request) != nil {
		return nil, nil, action.CodeInvalidArgument
	}

	if request.PeerID != options.PeerID {
		return nil, nil, action.CodeTargetChanged
	}

	entry, ok := action.Lookup(request.Action)
	if !ok || options.Dispatcher == nil {
		return nil, nil, action.CodeUnsupported
	}

	if options.Allowed == nil || !options.Allowed(request.Action) {
		return nil, nil, action.CodeForbidden
	}

	input, err := entry.DecodeInput(request.Input)
	if err != nil {
		return nil, nil, action.CodeInvalidArgument
	}

	if options.Receipts == nil {
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
