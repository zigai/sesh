//go:build linux || darwin

package picker

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui"
)

func TestPickerSwitchHandoffCarriesSelectionAndTreatsEmptyFileAsCancel(t *testing.T) {
	path, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("handoff permissions: %v, %v", info, err)
	}

	if _, selected, err := readPickerSwitchHandoff(path); err != nil || selected {
		t.Fatalf("empty handoff = %t, %v", selected, err)
	}

	target := session.Session{
		ID:               "$8",
		Name:             "agent-target",
		TmuxServerSocket: "/tmp/tmux-1000/default",
		Kind:             session.KindLive,
		Origin:           session.OriginLocal,
		RuntimeIdentity: session.RuntimeIdentity{
			TmuxServerPID:        4321,
			TmuxServerStartedAt:  time.Unix(100, 0),
			TmuxSessionCreatedAt: time.Unix(200, 0),
		},
		ActivePaneID: "%3",
	}
	if err := writePickerSwitchHandoff(path, &target, "%4", "", nil); err != nil {
		t.Fatal(err)
	}

	handoff, selected, err := readPickerSwitchHandoff(path)
	if err != nil || !selected {
		t.Fatalf("handoff read = %t, %v", selected, err)
	}

	assertHandoffMatches(t, handoff, target, "%4")
}

func assertHandoffMatches(t *testing.T, handoff pickerSwitchHandoff, target session.Session, paneID string) {
	t.Helper()

	if handoff.SessionID != target.ID || handoff.SessionName != target.Name {
		t.Fatalf("handoff identity = (%s, %s), want (%s, %s)", handoff.SessionID, handoff.SessionName, target.ID, target.Name)
	}

	if handoff.TmuxServerSocket != target.TmuxServerSocket || handoff.PaneID != paneID || handoff.ActivePaneID != target.ActivePaneID {
		t.Fatalf("handoff routing = (%s, %s, %s), want (%s, %s, %s)", handoff.TmuxServerSocket, handoff.PaneID, handoff.ActivePaneID, target.TmuxServerSocket, paneID, target.ActivePaneID)
	}

	if handoff.RuntimeIdentity.TmuxServerPID != target.RuntimeIdentity.TmuxServerPID ||
		!handoff.RuntimeIdentity.TmuxServerStartedAt.Equal(target.RuntimeIdentity.TmuxServerStartedAt) ||
		!handoff.RuntimeIdentity.TmuxSessionCreatedAt.Equal(target.RuntimeIdentity.TmuxSessionCreatedAt) {
		t.Fatalf("handoff runtime = %+v, want %+v", handoff.RuntimeIdentity, target.RuntimeIdentity)
	}
}

func TestPickerSwitchHandoffRejectsInvalidSelection(t *testing.T) {
	path, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	err = writePickerSwitchHandoff(path, &session.Session{Name: "missing-id"}, "", "", nil)
	if !errors.Is(err, errPickerHandoffInvalid) {
		t.Fatalf("invalid selection error = %v", err)
	}

	writeOops, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected oops error, got %T", err)
	}

	if writeOops.Domain() != "cli" || writeOops.Context()["operation"] != "write_picker_handoff" {
		t.Fatalf("unexpected oops metadata: domain=%q context=%v", writeOops.Domain(), writeOops.Context())
	}

	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err = readPickerSwitchHandoff(path)
	if !errors.Is(err, errPickerHandoffInvalid) {
		t.Fatalf("invalid payload error = %v", err)
	}

	readOops, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected oops error, got %T", err)
	}

	if readOops.Domain() != "cli" || readOops.Context()["operation"] != "read_picker_handoff" {
		t.Fatalf("unexpected oops metadata: domain=%q context=%v", readOops.Domain(), readOops.Context())
	}
}

func TestCompletePickerSwitchHandoffRejectsInvalidSelection(t *testing.T) {
	path, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	target := session.Session{
		ID:               "$1",
		Name:             "valid-session",
		Kind:             session.KindLive,
		Origin:           session.OriginLocal,
		TmuxServerSocket: "/tmp/tmux.sock",
	}
	if err := writePickerSwitchHandoff(path, &target, "%1", "", nil); err != nil {
		t.Fatal(err)
	}

	err = completePickerSwitchHandoff(t.Context(), nil, path, "", "origin-session")
	if !errors.Is(err, errPickerHandoffInvalid) {
		t.Fatalf("expected errPickerHandoffInvalid for missing clientName, got %v", err)
	}

	completeOops, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected oops error, got %T", err)
	}

	if completeOops.Domain() != "cli" || completeOops.Context()["operation"] != "complete_picker_handoff" {
		t.Fatalf("unexpected oops metadata: domain=%q context=%v", completeOops.Domain(), completeOops.Context())
	}
}

func TestBuildPickerModelUnknownPresentation(t *testing.T) {
	t.Parallel()

	_, err := buildPickerModel(t.Context(), pickerPresentation(255), nil, nil, tui.Services{})
	if !errors.Is(err, errUnknownPickerPresentation) {
		t.Fatalf("expected errUnknownPickerPresentation, got %v", err)
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected oops error, got %T", err)
	}

	if oopsErr.Domain() != "cli" || oopsErr.Context()["operation"] != "initialize_picker_model" {
		t.Fatalf("unexpected oops metadata: domain=%q context=%v", oopsErr.Domain(), oopsErr.Context())
	}
}

func TestPickerHandoffWithoutResultStaysEmpty(t *testing.T) {
	path, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if err := writePickerSwitchHandoff(path, nil, "", "", nil); err != nil {
		t.Fatal(err)
	}

	if handoff, selected, err := readPickerSwitchHandoff(path); err != nil || selected || len(handoff.AgentViews) != 0 {
		t.Fatalf("handoff without result = %+v, %t, %v; want empty", handoff, selected, err)
	}
}

func TestPickerHandoffCarriesDeferredAgentViewsWithoutSelection(t *testing.T) {
	path, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	views := []tui.AgentView{
		{Session: session.Identity{Origin: "local", Socket: "/tmp/tmux.sock", Name: "alpha"}, PaneID: "%1"},
		{Session: session.Identity{Origin: "local", Socket: "/tmp/tmux.sock", Name: "beta"}, PaneID: "%7"},
	}
	if err := writePickerSwitchHandoff(path, nil, "", "", views); err != nil {
		t.Fatal(err)
	}

	handoff, selected, err := readPickerSwitchHandoff(path)
	if err != nil || selected {
		t.Fatalf("views-only handoff read = %t, %v; want unselected", selected, err)
	}

	got := make([]tui.AgentView, 0, len(handoff.AgentViews))
	for _, view := range handoff.AgentViews {
		got = append(got, tui.AgentView{Session: view.Session, PaneID: view.PaneID})
	}

	if !slices.Equal(got, views) {
		t.Fatalf("handoff views = %+v, want %+v", got, views)
	}
}

func TestPopupPickerDefersAgentViewsInMemory(t *testing.T) {
	t.Parallel()

	alpha := session.Identity{Origin: "local", Socket: "/tmp/tmux.sock", Name: "alpha"}
	beta := session.Identity{Origin: "local", Socket: "/tmp/tmux.sock", Name: "beta"}

	inline := &pickerServices{popupChild: false}
	if inline.DeferAgentViews([]tui.AgentView{{Session: alpha, PaneID: "%1"}}) {
		t.Fatal("an inline picker deferred agent views; it must record them itself")
	}

	popup := &pickerServices{popupChild: true}
	if !popup.DeferAgentViews([]tui.AgentView{{Session: alpha, PaneID: "%1"}, {Session: beta, PaneID: "%2"}}) {
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

func TestCompletePickerHandoffRecordsDeferredViewsAfterCancel(t *testing.T) {
	path, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	identity := session.Identity{Origin: "local", Socket: "/tmp/tmux.sock", Name: "alpha"}
	if err := writePickerSwitchHandoff(path, nil, "", "", []tui.AgentView{{Session: identity, PaneID: "%3"}}); err != nil {
		t.Fatal(err)
	}

	historyDir := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(historyDir, 0o700); err != nil {
		t.Fatal(err)
	}

	historyPath := filepath.Join(historyDir, "history.json")
	services := &pickerServices{
		historyPath: historyPath,
		workflow:    session.NewWorkflow(nil, nil, historyPath),
	}

	if err := completePickerSwitchHandoff(t.Context(), services, path, "", ""); err != nil {
		t.Fatalf("complete views-only handoff: %v", err)
	}

	state, err := session.LoadState(historyPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(state.AgentViewHistory) != 1 || state.AgentViewHistory[0].Session != identity || state.AgentViewHistory[0].PaneID != "%3" {
		t.Fatalf("agent view history = %+v, want the deferred view", state.AgentViewHistory)
	}
}

func TestPickerHandoffCarriesReplayKeyWithoutSelection(t *testing.T) {
	path, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if err := writePickerSwitchHandoff(path, nil, "", tui.ReplayKeyFloatingTerminal, nil); err != nil {
		t.Fatal(err)
	}

	handoff, selected, err := readPickerSwitchHandoff(path)
	if err != nil {
		t.Fatal(err)
	}

	if selected || handoff.ReplayKey != tui.ReplayKeyFloatingTerminal {
		t.Fatalf("selected=%v replayKey=%q", selected, handoff.ReplayKey)
	}
}
