//go:build linux || darwin

package app

import (
	"testing"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

func TestMoveWindowDispatchRejectsInvalidInputAndNonlocalRouting(t *testing.T) {
	t.Parallel()

	local := NewLocal(LocalOptions{})
	input := action.MoveWindow{
		Source:      action.SessionRef{Kind: session.KindLive, ID: "$1"},
		Destination: action.SessionRef{Kind: session.KindLive, ID: "$1"},
		Pane:        "%3",
	}

	invocation := action.Invocation{Action: action.SessionMoveWindow.ID, Input: input}
	if !local.Accepts(invocation.At, invocation.Action) {
		t.Fatal("move action was not registered")
	}

	if _, err := local.Dispatch(
		t.Context(),
		invocation,
	); action.CodeOf(
		err,
	) != action.CodeInvalidArgument {
		t.Fatalf("same session error = %v", err)
	}

	invocation.Input = action.SwitchSession{}
	if _, err := local.Dispatch(
		t.Context(),
		invocation,
	); action.CodeOf(
		err,
	) != action.CodeInvalidArgument {
		t.Fatalf("wrong input type error = %v", err)
	}

	invocation.Input = input

	invocation.At.Kind = action.LocationPeer
	if local.Accepts(invocation.At, invocation.Action) {
		t.Fatal("accepted a peer move")
	}

	if _, err := local.Dispatch(
		t.Context(),
		invocation,
	); action.CodeOf(
		err,
	) != action.CodeUnsupported {
		t.Fatalf("peer route error = %v", err)
	}
}
