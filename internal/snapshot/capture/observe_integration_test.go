//go:build integration

package capture

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/zigai/gotmux/tmux"
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

	if err := compareGraph(t.Context(), server, before, []tmux.SessionID{session.ID()}, &baseline, nil); err != nil {
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

	if err := compareGraph(t.Context(), server, before, []tmux.SessionID{session.ID()}, &baseline, nil); err != nil {
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

			if err := compareGraph(t.Context(), server, before, []tmux.SessionID{session.ID()}, &baseline, nil); err == nil {
				t.Fatal("accepted changed selected pane")
			}
		})
	}
}

func TestObservePreservesStyledTrailingEmptyCells(t *testing.T) {
	server := tmuxtest.NewServer(t)
	session, pane := scriptedSessionFixture(t, server, "trailing-cells", 20, `printf 'scrollback-marker\nline2\nline3\nline4\nline5\nline6\nanchor\033[41m\033[K\033[0m'; exec sleep 300`)

	waitForPaneText(t, pane, []byte("anchor"))

	observation, err := Observe(t.Context(), server, []tmux.SessionID{session.ID()})
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

	server, err := tmux.New(tmux.Config{SocketPath: fixture.Endpoint().SocketPath, ConfigFile: "/dev/null", Limits: tmux.Limits{OutputBytes: 64 << 10}})
	if err != nil {
		t.Fatal(err)
	}

	observation, err := Observe(t.Context(), server, []tmux.SessionID{small.ID(), large.ID()})
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

func scriptedSessionFixture(t *testing.T, server *tmux.Server, name string, width int, script string) (tmux.Session, tmux.Pane) {
	t.Helper()

	session, err := server.NewSession(t.Context(), tmux.NewSessionOptions{
		Name:    name,
		Program: tmux.Exec("sh", "-c", script),
		Start:   tmux.ExistingOnly,
		Size:    tmux.Size{Width: width, Height: 5},
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

func waitForPaneText(t *testing.T, pane tmux.Pane, marker []byte) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		data, err := pane.Capture(ctx, tmux.CaptureOptions{PreserveSpaces: true})
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

func mutatePane(t *testing.T, pane tmux.Pane, mutation string) {
	t.Helper()

	var err error

	switch mutation {
	case "split":
		_, err = pane.Split(t.Context(), tmux.SplitOptions{Program: tmux.Exec("sleep", "300")})
	case "respawn":
		err = pane.Respawn(t.Context(), tmux.RespawnOptions{KillRunning: true, Program: tmux.Exec("sleep", "300")})
	case "kill":
		err = pane.Kill(t.Context())
	}

	if err != nil {
		t.Fatal(err)
	}
}

func captureSessionFixture(t *testing.T, server *tmux.Server, name string) (tmux.Session, tmux.Pane) {
	t.Helper()

	session, err := server.NewSession(t.Context(), tmux.NewSessionOptions{Name: name, Program: tmux.Exec("sleep", "300"), Start: tmux.ExistingOnly, Size: tmux.Size{Width: 100, Height: 35}})
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

func captureBaseline(t *testing.T, server *tmux.Server, session tmux.Session) (tmux.Snapshot, model.Manifest) {
	t.Helper()

	before, err := server.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	m, err := graph(before, []tmux.SessionID{session.ID()})
	if err != nil {
		t.Fatal(err)
	}

	return before, m
}
