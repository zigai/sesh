package model

import "testing"

func TestActionStateCanBecomeForEveryPair(t *testing.T) {
	states := []ActionState{
		ActionStatePlanned,
		ActionStateIssued,
		ActionStateNotSent,
		ActionStateUnknown,
		ActionStateConfirmed,
		ActionStateVerified,
	}
	allowed := map[ActionState]map[ActionState]bool{
		ActionStatePlanned:   {ActionStateIssued: true},
		ActionStateIssued:    {ActionStateNotSent: true, ActionStateUnknown: true, ActionStateConfirmed: true},
		ActionStateNotSent:   {ActionStatePlanned: true},
		ActionStateUnknown:   {ActionStateConfirmed: true},
		ActionStateConfirmed: {ActionStateVerified: true},
		ActionStateVerified:  {},
	}

	for _, before := range states {
		if !before.Valid() {
			t.Fatalf("valid state %q rejected", before)
		}

		for _, after := range states {
			if got, want := before.CanBecome(after), allowed[before][after]; got != want {
				t.Errorf("%q -> %q = %t, want %t", before, after, got, want)
			}
		}
	}

	if ActionState("invalid").Valid() || ActionState("invalid").CanBecome(ActionStateIssued) || ActionStatePlanned.CanBecome(ActionState("invalid")) {
		t.Fatal("invalid action state accepted")
	}
}

func TestActionKindValid(t *testing.T) {
	for _, kind := range []ActionKind{
		ActionKindCreateSession,
		ActionKindCreateWindow,
		ActionKindCreatePane,
		ActionKindLink,
		ActionKindConfigure,
		ActionKindLaunch,
		ActionKindVerify,
	} {
		if !kind.Valid() {
			t.Errorf("valid kind %q rejected", kind)
		}
	}

	if ActionKind("invalid").Valid() {
		t.Fatal("invalid kind accepted")
	}
}
