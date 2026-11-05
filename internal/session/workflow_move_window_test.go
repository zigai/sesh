//go:build linux || darwin

package session

import (
	"errors"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
)

func TestWindowMoveRejectsUnstableRemoteAndIdenticalTargets(t *testing.T) {
	t.Parallel()

	valid := MoveWindowOptions{
		Source:      Session{ID: "$1", Kind: KindLive},
		Destination: Session{ID: "$2", Kind: KindLive},
		PaneID:      "%7",
	}
	if err := validateWindowMove(valid); err != nil {
		t.Fatal(err)
	}

	for _, mutate := range []func(*MoveWindowOptions){
		func(opts *MoveWindowOptions) { opts.Destination.ID = opts.Source.ID },
		func(opts *MoveWindowOptions) { opts.Source.ID = "source" },
		func(opts *MoveWindowOptions) { opts.Destination.ID = "dest" },
		func(opts *MoveWindowOptions) { opts.PaneID = "1" },
		func(opts *MoveWindowOptions) { opts.PaneID = "" },
		func(opts *MoveWindowOptions) { opts.WindowID = "source:0" },
		func(opts *MoveWindowOptions) { opts.Source.Kind = KindSaved },
		func(opts *MoveWindowOptions) { opts.Destination.Kind = KindSaved },
		func(opts *MoveWindowOptions) { opts.Source.Origin = OriginSSH },
		func(opts *MoveWindowOptions) { opts.Destination.Origin = OriginBridge },
		func(opts *MoveWindowOptions) { opts.Source.RuntimeIdentity.ConnectionID = "peer" },
	} {
		opts := valid
		mutate(&opts)

		if err := validateWindowMove(opts); !errors.Is(err, ErrInvalidWindowMove) {
			t.Fatalf("validateWindowMove(%+v) = %v", opts, err)
		}
	}
}

func TestMovingAgentViewsPreservesSeenAndUnseenForEveryPane(t *testing.T) {
	t.Parallel()

	source := Identity{Origin: "local", Socket: "/socket", Name: "source"}
	destination := Identity{Origin: "local", Socket: "/socket", Name: "dest"}
	other := Identity{Origin: "ssh", ConnectionID: "other", Name: "source"}
	state := defaultState()
	state.AgentViewHistory = []AgentViewHistoryEntry{
		{Session: source, PaneID: "%1", ViewedAtUnixNano: 100},
		{Session: source, PaneID: "%2", ViewedAtUnixNano: 200},
		{Session: source, PaneID: "%3", ViewedAtUnixNano: 300},
		{Session: other, PaneID: "%1", ViewedAtUnixNano: 400},
		{Session: destination, PaneID: "%2", ViewedAtUnixNano: 500},
	}
	state.MoveAgentViews(source, destination, []string{"%1", "%2", "%4"})

	if state.GetLastAgentView(source, "%1") != 0 || state.GetLastAgentView(source, "%2") != 0 ||
		state.GetLastAgentView(
			destination,
			"%1",
		) != 100 || state.GetLastAgentView(destination, "%2") != 500 ||
		state.GetLastAgentView(
			destination,
			"%4",
		) != 0 || state.GetLastAgentView(source, "%3") != 300 ||
		state.GetLastAgentView(other, "%1") != 400 || len(state.AgentViewHistory) != 4 {
		t.Fatalf("moved history = %+v", state.AgentViewHistory)
	}

	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowMoveMutationErrorsPreserveUncertainty(t *testing.T) {
	effects := []gotmux.Effect{
		gotmux.EffectUnspecified, gotmux.EffectUnknown, gotmux.EffectConfirmed,
		gotmux.EffectNotSent, gotmux.EffectRejected,
	}

	for _, effect := range effects {
		cause := &gotmux.OperationError{
			Operation: "WindowLink.Move",
			Outcome:   gotmux.Outcome{Effect: effect},
			Err:       gotmux.ErrLinkChanged,
		}
		err := windowMoveMutationError(cause)

		uncertain := effect != gotmux.EffectNotSent && effect != gotmux.EffectRejected
		if errors.Is(err, ErrWindowMoveUncertain) != uncertain ||
			!errors.Is(err, gotmux.ErrLinkChanged) {
			t.Fatalf("effect %v = %v", effect, err)
		}
	}
}
