//go:build linux || darwin

package remote

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/action"
)

var errTestDispatch = errors.New("dispatch failed")

type fakeDispatcher struct {
	mu       sync.Mutex
	calls    []action.Invocation
	dispatch func(ctx context.Context, invocation action.Invocation) (any, error)
}

func (dispatcher *fakeDispatcher) Accepts(at action.Location, _ action.ID) bool {
	return at.Kind == action.LocationLocal
}

func (dispatcher *fakeDispatcher) Dispatch(ctx context.Context, invocation action.Invocation) (any, error) {
	dispatcher.mu.Lock()
	dispatcher.calls = append(dispatcher.calls, invocation)
	dispatcher.mu.Unlock()

	if dispatcher.dispatch == nil {
		input, err := action.SessionKill.Input(invocation.Input)
		if err != nil {
			return nil, fmt.Errorf("fake dispatch: %w", err)
		}

		return action.KilledSession{Session: input.Session}, nil
	}

	return dispatcher.dispatch(ctx, invocation)
}

func (dispatcher *fakeDispatcher) callCount() int {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()

	return len(dispatcher.calls)
}

func peerActionFixture(t *testing.T) (PeerActionOptions, *fakeDispatcher) {
	t.Helper()

	options, dispatcher, _ := peerActionFixtureAt(t)

	return options, dispatcher
}

func peerActionFixtureAt(t *testing.T) (PeerActionOptions, *fakeDispatcher, string) {
	t.Helper()

	receipts, receiptsPath := openTestReceiptDir(t)
	dispatcher := &fakeDispatcher{mu: sync.Mutex{}, calls: nil, dispatch: nil}

	return PeerActionOptions{
		PeerID:     testPeerID,
		Receipts:   receipts,
		Allowed:    func(id action.ID) bool { return id == action.SessionKill.ID },
		Dispatcher: dispatcher,
		Now:        func() time.Time { return time.Unix(2000, 0).UTC() },
	}, dispatcher, receiptsPath
}

func resignedRequest(t *testing.T, request ActionRequest) ActionRequest {
	t.Helper()

	digest, err := IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest = digest

	return request
}

func TestExecutePeerActionRunsTheTypedInputLocally(t *testing.T) {
	options, dispatcher := peerActionFixture(t)

	result, _ := ExecutePeerAction(t.Context(), options, liveActionRequest(t))
	if !result.OK || result.Action != action.SessionKill.ID {
		t.Fatalf("result = %#v, want success", result)
	}

	if len(dispatcher.calls) != 1 || dispatcher.calls[0].At != action.Local() {
		t.Fatalf("dispatched %#v, want one local invocation", dispatcher.calls)
	}

	input, err := action.SessionKill.Input(dispatcher.calls[0].Input)
	if err != nil || input.Session.ID != "$1" || input.Session.ServerPID != 42 {
		t.Fatalf("dispatched input = %#v, %v; want the decoded session reference", input, err)
	}

	output, err := action.SessionKill.DecodeOutput(result.Output)
	if killed, ok := output.(action.KilledSession); err != nil || !ok || killed.Session.Name != "work" {
		t.Fatalf("result output = %s, %v; want the typed output", result.Output, err)
	}
}

func TestExecutePeerActionRefusesBeforeRunning(t *testing.T) {
	tests := map[string]struct {
		mutate func(*PeerActionOptions, *ActionRequest)
		code   action.Code
	}{
		"not allowed by the peer": {
			mutate: func(opts *PeerActionOptions, _ *ActionRequest) {
				opts.Allowed = func(action.ID) bool { return false }
			},
			code: action.CodeForbidden,
		},
		"unregistered action": {
			mutate: func(_ *PeerActionOptions, request *ActionRequest) { request.Action = "session.explode" },
			code:   action.CodeUnsupported,
		},
		"input for another action": {
			mutate: func(_ *PeerActionOptions, request *ActionRequest) {
				request.Input = jsontext.Value(`{"unexpected":true}`)
			},
			code: action.CodeInvalidArgument,
		},
		"another peer": {
			mutate: func(_ *PeerActionOptions, request *ActionRequest) { request.PeerID = "replacement-peer" },
			code:   action.CodeTargetChanged,
		},
		"no receipts": {
			mutate: func(opts *PeerActionOptions, _ *ActionRequest) { opts.Receipts = nil },
			code:   action.CodeUnavailable,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			options, dispatcher := peerActionFixture(t)
			request := liveActionRequest(t)
			test.mutate(&options, &request)
			request = resignedRequest(t, request)

			result, _ := ExecutePeerAction(t.Context(), options, request)
			if result.OK || result.Code != test.code || dispatcher.callCount() != 0 {
				t.Fatalf("result = %#v after %d dispatches, want %s without running", result, dispatcher.callCount(), test.code)
			}
		})
	}
}

func TestExecutePeerActionRejectsTamperedIntent(t *testing.T) {
	options, dispatcher := peerActionFixture(t)
	request := liveActionRequest(t)
	request.Input = jsontext.Value(`{"session":{"id":"$2","name":"other"},"force":true}`)

	result, _ := ExecutePeerAction(t.Context(), options, request)
	if result.OK || result.Code != action.CodeInvalidArgument || dispatcher.callCount() != 0 {
		t.Fatalf("tampered request = %#v, want refusal without running", result)
	}
}

func TestExecutePeerActionRunsEachInvocationOnce(t *testing.T) {
	options, dispatcher := peerActionFixture(t)
	request := liveActionRequest(t)

	first, _ := ExecutePeerAction(t.Context(), options, request)
	replayed, _ := ExecutePeerAction(t.Context(), options, request)

	if dispatcher.callCount() != 1 {
		t.Fatalf("invocation ran %d times, want once", dispatcher.callCount())
	}

	if !replayed.OK || string(replayed.Output) != string(first.Output) {
		t.Fatalf("replay = %#v, want the recorded result %#v", replayed, first)
	}

	conflicting := request
	conflicting.Input = jsontext.Value(`{"session":{"id":"$2","name":"other"},"force":false}`)
	conflicting = resignedRequest(t, conflicting)

	if result, _ := ExecutePeerAction(t.Context(), options, conflicting); result.Code != action.CodeConflict || dispatcher.callCount() != 1 {
		t.Fatalf("reused invocation ID = %#v, want conflict without running", result)
	}
}

func TestExecutePeerActionReportsTheActionsFailure(t *testing.T) {
	options, dispatcher := peerActionFixture(t)
	dispatcher.dispatch = func(_ context.Context, invocation action.Invocation) (any, error) {
		input, err := action.SessionKill.Input(invocation.Input)
		if err != nil {
			return nil, fmt.Errorf("fake dispatch: %w", err)
		}

		return action.KilledSession{Session: input.Session}, action.Wrap(action.CodeAttached, errTestDispatch)
	}

	request := liveActionRequest(t)

	result, _ := ExecutePeerAction(t.Context(), options, request)
	if result.OK || result.Code != action.CodeAttached || len(result.Output) == 0 {
		t.Fatalf("result = %#v, want the action's code and output", result)
	}

	recorded, err := LookupPeerInvocation(options.Receipts, request.InvocationID)
	if err != nil || recorded.Code != action.CodeAttached {
		t.Fatalf("receipt = %#v, %v; want the failure recorded", recorded, err)
	}
}

func TestExecutePeerActionInterruptedWhileRunningIsUncertain(t *testing.T) {
	options, dispatcher := peerActionFixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	dispatcher.dispatch = func(context.Context, action.Invocation) (any, error) {
		cancel()

		return nil, context.Canceled
	}

	result, _ := ExecutePeerAction(ctx, options, liveActionRequest(t))
	if result.OK || result.Code != action.CodeUncertain {
		t.Fatalf("result = %#v, want an uncertain outcome", result)
	}
}

func TestExecutePeerActionIsUncertainWhenTheReceiptCannotBeResolved(t *testing.T) {
	options, dispatcher, receiptsPath := peerActionFixtureAt(t)
	request := liveActionRequest(t)

	dispatcher.dispatch = func(context.Context, action.Invocation) (any, error) {
		if err := os.Remove(filepath.Join(receiptsPath, request.InvocationID+invocationFileSuffix)); err != nil {
			t.Fatal(err)
		}

		return action.KilledSession{Session: action.SessionRef{}}, nil
	}

	result, _ := ExecutePeerAction(t.Context(), options, request)
	if result.OK || result.Code != action.CodeUncertain {
		t.Fatalf("result = %#v, want an uncertain outcome when the receipt is lost", result)
	}
}

func TestPeerStateDirectoryIsPrivate(t *testing.T) {
	dir, path := openTestCatalogDir(t)

	if _, err := LoadOrCreatePeerID(dir); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(path, peerIDName))
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("peer ID mode = %o, want 0600", info.Mode().Perm())
	}
}
