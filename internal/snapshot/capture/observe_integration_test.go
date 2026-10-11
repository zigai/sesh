//go:build integration

package capture

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestTitleChangesDoNotInvalidateObservation(t *testing.T) {
	server := tmuxtest.NewServer(t)
	session, pane := captureSessionFixture(t, server, "selected")

	before, baseline := captureBaseline(t, server, session)
	if err := pane.SetTitle(t.Context(), "title changed"); err != nil {
		t.Fatal(err)
	}

	info, err := pane.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if info.Title != "title changed" {
		t.Fatalf("title fixture not applied: %q", info.Title)
	}

	if err := compareGraph(t.Context(), server, before, []gotmux.SessionID{session.ID()}, &baseline, nil); err != nil {
		t.Fatal("title-only change prevented saving", err)
	}

	if string(baseline.Panes[0].Title) != info.Title {
		t.Fatal("accepted title lost from observation")
	}
}

func TestUnrelatedPaneExitDoesNotInvalidateSelection(t *testing.T) {
	server := tmuxtest.NewServer(t)
	session, _ := captureSessionFixture(t, server, "selected")
	unrelated, _ := captureSessionFixture(t, server, "unrelated")

	before, baseline := captureBaseline(t, server, session)
	if err := unrelated.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := compareGraph(t.Context(), server, before, []gotmux.SessionID{session.ID()}, &baseline, nil); err != nil {
		t.Fatal("unrelated work invalidated explicit selection", err)
	}
}

func TestSelectedMutationRejectsObservation(t *testing.T) {
	for _, mutation := range []string{"split", "respawn", "kill"} {
		t.Run(mutation, func(t *testing.T) {
			server := tmuxtest.NewServer(t)
			session, pane := captureSessionFixture(t, server, "selected")
			before, baseline := captureBaseline(t, server, session)
			mutatePane(t, pane, mutation)

			if err := compareGraph(t.Context(), server, before, []gotmux.SessionID{session.ID()}, &baseline, nil); err == nil {
				t.Fatal("accepted changed selected pane")
			}
		})
	}
}

func TestObservePreservesStyledTrailingEmptyCells(t *testing.T) {
	server := tmuxtest.NewServer(t)
	session, pane := scriptedSessionFixture(t, server, "trailing-cells", 20, `printf 'scrollback-marker\nline2\nline3\nline4\nline5\nline6\nanchor\033[41m\033[K\033[0m'; exec sleep 300`)

	waitForPaneText(t, pane, []byte("anchor"))

	observation, err := Observe(t.Context(), server, []gotmux.SessionID{session.ID()})
	if err != nil {
		t.Fatal(err)
	}

	if len(observation.Manifest.Panes) != 1 {
		t.Fatalf("captured %d panes, want 1", len(observation.Manifest.Panes))
	}

	artifactID := observation.Manifest.Panes[0].Terminal.Artifact

	data, ok := observation.Artifacts[artifactID]
	if !ok {
		t.Fatalf("terminal artifact %q missing", artifactID)
	}

	if !bytes.Contains(data, []byte("scrollback-marker")) {
		t.Fatalf("terminal artifact did not preserve scrollback history: %q", data)
	}

	want := []byte("anchor\x1b[41m              \n")
	if !bytes.Contains(data, want) {
		t.Fatalf("terminal artifact did not preserve styled trailing cells: %q", data)
	}
}

func TestObserveOmitsTerminalTextBeyondOutputLimit(t *testing.T) {
	fixture := tmuxtest.NewServer(t)
	small, smallPane := scriptedSessionFixture(t, fixture, "small", 20, `printf 'small-marker'; exec sleep 300`)
	large, largePane := scriptedSessionFixture(t, fixture, "large", 160, `i=0; while [ $i -lt 1500 ]; do printf '%0150d\n' $i; i=$((i+1)); done; printf 'large-marker'; exec sleep 300`)

	waitForPaneText(t, smallPane, []byte("small-marker"))
	waitForPaneText(t, largePane, []byte("large-marker"))

	server, err := gotmux.New(gotmux.Config{SocketPath: fixture.Endpoint().SocketPath, ConfigFile: "/dev/null", Limits: gotmux.Limits{OutputBytes: 64 << 10}})
	if err != nil {
		t.Fatal(err)
	}

	observation, err := Observe(t.Context(), server, []gotmux.SessionID{small.ID(), large.ID()})
	if err != nil {
		t.Fatal("oversized terminal text failed the observation:", err)
	}

	if err := observation.Manifest.Validate(); err != nil {
		t.Fatal(err)
	}

	if len(observation.Manifest.Panes) != 2 || len(observation.Artifacts) != 1 || observation.OmittedTerminalText() != 1 {
		t.Fatalf("got %d panes, %d artifacts, %d omitted; want 2, 1, 1", len(observation.Manifest.Panes), len(observation.Artifacts), observation.OmittedTerminalText())
	}

	for _, pane := range observation.Manifest.Panes {
		data, captured := observation.Artifacts[pane.Terminal.Artifact]
		if captured != bytes.Contains(data, []byte("small-marker")) {
			t.Fatalf("pane %s kept the wrong terminal text", pane.ID)
		}
	}
}

func TestObserveOmitsFloatingPanes(t *testing.T) {
	server := tmuxtest.NewServer(t)
	requireFloatingPanes(t, server)

	session, first, second := floatingPaneFixture(t, server)

	observation, err := Observe(t.Context(), server, []gotmux.SessionID{session.ID()})
	if err != nil {
		t.Fatalf("observation with a floating pane failed: %v", err)
	}

	if observation.FloatingPanesOmitted != 1 {
		t.Fatalf("floating panes omitted = %d, want 1", observation.FloatingPanesOmitted)
	}

	panes := observation.Manifest.Panes
	if len(panes) != 2 || panes[0].ID != ref("p", string(first.ID())) || panes[1].ID != ref("p", string(second.ID())) {
		t.Fatalf("saved panes = %+v, want only the two tiled panes", panes)
	}

	if panes[1].Index != panes[0].Index+1 {
		t.Fatalf("tiled pane indexes = %d, %d, want consecutive", panes[0].Index, panes[1].Index)
	}

	if panes[0].Active == panes[1].Active {
		t.Fatal("exactly one tiled pane must take over from the active floating pane")
	}

	layoutEvidence := observation.Manifest.Evidence[slices.IndexFunc(observation.Manifest.Evidence, func(e model.Evidence) bool { return e.Domain == "S03" })]
	if layoutEvidence.Reason != string(model.ReasonFloatingPanesOmitted) {
		t.Fatalf("layout evidence = %+v, want floating panes omitted", layoutEvidence)
	}
}

// floatingPaneFixture opens an active floating pane between two tiled panes, so the floating
// pane takes the middle pane index.
func floatingPaneFixture(t *testing.T, server *gotmux.Server) (gotmux.Session, gotmux.Pane, gotmux.Pane) {
	t.Helper()

	session, first := captureSessionFixture(t, server, "floating")

	second, err := first.Split(t.Context(), gotmux.SplitOptions{Program: gotmux.Exec("sleep", "300")})
	if err != nil {
		t.Fatal(err)
	}

	if err := second.Select(t.Context()); err != nil {
		t.Fatal(err)
	}

	floating, err := first.NewPane(t.Context(), gotmux.NewPaneOptions{Width: "20", Height: "5", X: "3", Y: "3", Program: gotmux.Exec("sleep", "300"), Select: true})
	if err != nil {
		t.Fatal(err)
	}

	info, err := floating.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if !info.Active {
		t.Fatal("floating pane fixture is not active")
	}

	return session, first, second
}

func scriptedSessionFixture(t *testing.T, server *gotmux.Server, name string, width int, script string) (gotmux.Session, gotmux.Pane) {
	t.Helper()

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    name,
		Program: gotmux.Exec("sh", "-c", script),
		Start:   gotmux.StartPolicyExistingOnly,
		Size:    gotmux.Size{Width: width, Height: 5},
	})
	if err != nil {
		t.Fatal(err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	pane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return session, pane
}

func waitForPaneText(t *testing.T, pane gotmux.Pane, marker []byte) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		data, err := pane.Capture(ctx, gotmux.CaptureOptions{PreserveSpaces: true})
		if err != nil {
			t.Fatal(err)
		}

		if bytes.Contains(data, marker) {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("pane did not emit marker: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func mutatePane(t *testing.T, pane gotmux.Pane, mutation string) {
	t.Helper()

	var err error

	switch mutation {
	case "split":
		_, err = pane.Split(t.Context(), gotmux.SplitOptions{Program: gotmux.Exec("sleep", "300")})
	case "respawn":
		err = pane.Respawn(t.Context(), gotmux.RespawnOptions{KillRunning: true, Program: gotmux.Exec("sleep", "300")})
	case "kill":
		err = pane.Kill(t.Context())
	}

	if err != nil {
		t.Fatal(err)
	}
}

func captureSessionFixture(t *testing.T, server *gotmux.Server, name string) (gotmux.Session, gotmux.Pane) {
	t.Helper()

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{Name: name, Program: gotmux.Exec("sleep", "300"), Start: gotmux.StartPolicyExistingOnly, Size: gotmux.Size{Width: 100, Height: 35}})
	if err != nil {
		t.Fatal(err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	pane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return session, pane
}

func captureBaseline(t *testing.T, server *gotmux.Server, session gotmux.Session) (gotmux.Snapshot, model.Manifest) {
	t.Helper()

	before, err := server.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	m, _, err := graph(before, []gotmux.SessionID{session.ID()})
	if err != nil {
		t.Fatal(err)
	}

	return before, m
}

func requireFloatingPanes(t *testing.T, server *gotmux.Server) {
	t.Helper()

	info, err := server.Probe(t.Context())
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}

	if !info.Version.AtLeast(3, 7) {
		t.Skipf("floating panes need tmux 3.7 or newer, test server runs %s", info.Version)
	}
}
