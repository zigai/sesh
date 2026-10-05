//go:build linux && integration

package picker

import (
	"slices"
	"testing"
	"time"

	"github.com/zigai/aht/v2/pkg/aht"
	"github.com/zigai/aht/v2/pkg/registry"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/tui"
)

func TestPickerAgentSnapshotsCarryPanesObservedWithAgents(t *testing.T) {
	server := tmuxtest.NewServer(t)
	origin := pickerOriginSession(t, server)
	livePane := pickerFirstPane(t, origin.Handle())

	services := newPickerInitialReadsServices(t, server, "", "")
	services.agentStore = pickerNameWatchSource{sessions: []aht.Session{
		pickerPaneAgent(server, "live-agent", livePane),
		pickerPaneAgent(server, "vanished-agent", "%999"),
	}}

	loaded, err := services.LoadAgentSummaries(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	assertPickerAgentPanes(t, "load", loaded, livePane)

	published := 0

	err = services.WatchAgentSummaries(t.Context(), func(_ uint64, watched tui.AgentSnapshot, publishErr error) error {
		if publishErr != nil {
			return publishErr
		}

		assertPickerAgentPanes(t, "watch", watched, livePane)

		published++

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if published == 0 {
		t.Fatal("watch published no snapshots")
	}
}

func TestPickerAgentSnapshotLeavesPanesUnobservedWhenTmuxFails(t *testing.T) {
	server := tmuxtest.NewServer(t)
	origin := pickerOriginSession(t, server)
	livePane := pickerFirstPane(t, origin.Handle())

	services := newPickerInitialReadsServices(t, server, "", "")
	services.agentStore = pickerNameWatchSource{sessions: []aht.Session{pickerPaneAgent(server, "live-agent", livePane)}}

	if err := server.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	snapshot, err := services.LoadAgentSummaries(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if snapshot.PanesObserved {
		t.Fatalf("panes observed on a dead server: %+v", snapshot.Panes)
	}

	if len(snapshot.Summaries) != 1 || len(snapshot.Summaries[0].Agents) != 1 {
		t.Fatalf("summaries = %+v, want the one loaded agent", snapshot.Summaries)
	}
}

func assertPickerAgentPanes(t *testing.T, label string, snapshot tui.AgentSnapshot, livePane string) {
	t.Helper()

	if !snapshot.PanesObserved {
		t.Fatalf("%s: panes not observed", label)
	}

	paneIDs := make([]string, 0, len(snapshot.Panes))
	for _, pane := range snapshot.Panes {
		paneIDs = append(paneIDs, pane.PaneID)
	}

	if !slices.Contains(paneIDs, livePane) || slices.Contains(paneIDs, "%999") {
		t.Fatalf("%s: observed panes = %v, want %s and not %%999", label, paneIDs, livePane)
	}

	agentCount := 0
	for _, summary := range snapshot.Summaries {
		agentCount += len(summary.Agents)
	}

	if agentCount != 2 {
		t.Fatalf("%s: agents = %d, want both loaded agents before pruning", label, agentCount)
	}
}

func pickerPaneAgent(server *gotmux.Server, id, pane string) aht.Session {
	now := time.Now().UTC()
	identity := registry.ProcessIdentity{PID: 42, StartIdentity: "fixture:" + id}

	return aht.Session{
		SchemaVersion: 3,
		ID:            id,
		Harness:       aht.HarnessPi,
		Liveness:      aht.Live{Activity: aht.ActivityIdle},
		SessionID:     id + "-native",
		Process:       &identity,
		Location: aht.Location{
			Kind:        aht.MultiplexerTmux,
			ServerID:    server.Endpoint().SocketPath,
			SessionID:   "$0",
			SessionName: "workspace",
			PaneID:      pane,
			PanePID:     42,
		},
		Observations: registry.Observations{Native: &registry.NativeObservation{
			Process: identity, Attributes: map[string]string{"aht_interaction_mode": "tui"}, ObservedAt: now,
		}},
		CreatedAt: now, UpdatedAt: now, PresenceChangedAt: now, ActivityChangedAt: now,
	}
}
