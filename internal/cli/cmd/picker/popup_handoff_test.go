//go:build linux || darwin

package picker

import (
	"errors"
	"testing"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui"
)

func TestBuildPickerModelUnknownPresentation(t *testing.T) {
	t.Parallel()

	_, err := buildPickerModel(t.Context(), presentation(255), nil, nil, tui.Services{})
	if !errors.Is(err, errUnknownPickerPresentation) {
		t.Fatalf("expected errUnknownPickerPresentation, got %v", err)
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected oops error, got %T", err)
	}

	if oopsErr.Domain() != "cli" || oopsErr.Context()["operation"] != "initialize_picker_model" {
		t.Fatalf(
			"unexpected oops metadata: domain=%q context=%v",
			oopsErr.Domain(),
			oopsErr.Context(),
		)
	}
}

func TestPopupPickerDefersAgentViewsInMemory(t *testing.T) {
	t.Parallel()

	alpha := session.Identity{Origin: "local", Socket: "/tmp/tmux.sock", Name: "alpha"}
	beta := session.Identity{Origin: "local", Socket: "/tmp/tmux.sock", Name: "beta"}

	inline := &services{popupChild: false}
	if inline.DeferAgentViews([]tui.AgentView{{Session: alpha, PaneID: "%1"}}) {
		t.Fatal("an inline picker deferred agent views; it must record them itself")
	}

	popup := &services{popupChild: true}
	if !popup.DeferAgentViews(
		[]tui.AgentView{{Session: alpha, PaneID: "%1"}, {Session: beta, PaneID: "%2"}},
	) {
		t.Fatal("a popup picker recorded agent views itself; it must defer them")
	}

	popup.DeferAgentViews([]tui.AgentView{{Session: alpha, PaneID: "%1"}})

	got := popup.takeDeferredAgentViews()
	want := []tui.AgentView{{Session: beta, PaneID: "%2"}, {Session: alpha, PaneID: "%1"}}

	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("deferred views = %+v, want each view once, latest last: %+v", got, want)
	}

	if rest := popup.takeDeferredAgentViews(); len(rest) != 0 {
		t.Fatalf("deferred views after take = %+v, want none", rest)
	}
}
