package action_test

import (
	"reflect"
	"testing"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/session"
)

func TestMoveWindowBindsPaneAndStableDestination(t *testing.T) {
	t.Parallel()

	source := session.Session{ID: "$1", Name: "source", Kind: session.KindLive}
	subject := action.Subject{Sessions: []session.Session{source}, Pane: "%4"}

	input, err := action.SessionMoveWindow.Bind(subject, action.Values{"destination": {"$2"}})
	if err != nil {
		t.Fatal(err)
	}

	if input.Source.ID != "$1" || input.Pane != "%4" || input.Window != "" ||
		input.Destination.ID != "$2" ||
		input.Destination.Kind != session.KindLive {
		t.Fatalf("input = %+v", input)
	}

	encoded, err := action.SessionMoveWindow.EncodeInput(input)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := action.SessionMoveWindow.DecodeInput(encoded)
	if err != nil || !reflect.DeepEqual(decoded, input) {
		t.Fatalf("round trip = %+v, %v", decoded, err)
	}

	if _, err := action.SessionMoveWindow.DecodeInput([]byte(`{"unknown":true}`)); err == nil {
		t.Fatal("accepted an unknown payload field")
	}

	if action.SessionMoveWindow.Remote != action.RemoteNever ||
		action.PolicyFor(nil, action.SessionMoveWindow.Describe()).AllowRemote {
		t.Fatal("window move must never run remotely")
	}

	if _, err := action.SessionMoveWindow.Bind(subject, nil); err == nil {
		t.Fatal("accepted missing destination")
	}

	if _, err := action.SessionMoveWindow.Bind(
		action.Subject{Sessions: subject.Sessions},
		action.Values{"destination": {"$2"}},
	); err == nil {
		t.Fatal("accepted missing pane")
	}
}
