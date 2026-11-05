//go:build (linux || darwin) && integration

package session_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/session"
)

func moveSessionRow(t *testing.T, server *gotmux.Server, id gotmux.SessionID) session.Session {
	t.Helper()

	rows, err := session.ListLive(t.Context(), server, "")
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range rows {
		if row.ID == string(id) {
			return row
		}
	}

	t.Fatalf("missing session %s", id)

	return session.Session{}
}

func TestMoveWindowPreservesAllPanesProcessesAndFocusAndAppendsAfterGaps(t *testing.T) {
	fixture := newWorkflowFixture(t)
	ctx := t.Context()
	source := fixture.newLiveSession(t, "source")
	dest := fixture.fixtureSession(t)

	sourceWindows, err := source.Handle().Windows(ctx)
	if err != nil || len(sourceWindows) != 1 {
		t.Fatalf("source windows = %v, %v", sourceWindows, err)
	}

	window := sourceWindows[0].Window()

	panes, err := window.Panes(ctx)
	if err != nil {
		t.Fatal(err)
	}

	splitOpts := gotmux.SplitOptions{
		Direction: gotmux.DirectionVertical,
		Program:   gotmux.Exec("/bin/sh"),
		Select:    false,
	}

	if _, err := panes[0].Handle().Split(ctx, splitOpts); err != nil {
		t.Fatal(err)
	}

	before, err := window.Panes(ctx)
	if err != nil || len(before) != 2 {
		t.Fatalf("panes = %+v, %v", before, err)
	}

	if err := window.Options().SetUser(ctx, "@sesh_task", "T7"); err != nil {
		t.Fatal(err)
	}

	for _, pane := range before {
		if err := pane.Handle().Options().SetUser(ctx, "@sesh_task", "T7"); err != nil {
			t.Fatal(err)
		}

		if err := pane.Handle().Options().SetUser(ctx, "@sesh_role", "agent"); err != nil {
			t.Fatal(err)
		}
	}

	// Keep a source window alive and select it, so neither client's focus needs
	// to change as a consequence of removing the moved source link.
	keep, err := source.Handle().NewWindow(ctx, gotmux.NewWindowOptions{Name: "keep", Select: true})
	if err != nil {
		t.Fatal(err)
	}

	index := 8

	destLast, err := dest.Handle().
		NewWindow(ctx, gotmux.NewWindowOptions{Name: "last", Index: &index, Select: false})
	if err != nil {
		t.Fatal(err)
	}

	destActive, err := activeMoveWindow(ctx, dest.Handle())
	if err != nil {
		t.Fatal(err)
	}

	client := fixture.attachClient(t, source.ID)
	otherClient := fixture.attachClient(t, dest.ID)
	sourceRow := moveSessionRow(t, fixture.server, source.ID)
	destRow := moveSessionRow(t, fixture.server, dest.ID)
	wf := session.NewWorkflow(fixture.server, nil, fixture.historyPath)
	sourceIdentity := session.Identity{
		Origin: "local",
		Socket: sourceRow.TmuxServerSocket,
		Name:   source.Name,
	}
	destIdentity := session.Identity{
		Origin: "local",
		Socket: destRow.TmuxServerSocket,
		Name:   dest.Name,
	}

	if _, err := session.UpdateState(ctx, fixture.historyPath, func(state *session.State) {
		state.RecordAgentViewAt(sourceIdentity, string(before[0].ID), time.Unix(0, 123))
	}); err != nil {
		t.Fatal(err)
	}

	out, err := wf.MoveWindow(
		ctx,
		session.MoveWindowOptions{
			Source:      sourceRow,
			Destination: destRow,
			PaneID:      string(before[0].ID),
		},
	)
	if err != nil || !out.Moved || !out.HistoryUpdated || out.Index != index+1 ||
		out.WindowID != string(window.ID()) {
		t.Fatalf("move = %+v, %v", out, err)
	}

	after, err := window.Panes(ctx)
	if err != nil || len(after) != len(before) {
		t.Fatalf("after panes = %+v, %v", after, err)
	}

	for i, pane := range before {
		if after[i].ID != pane.ID || after[i].PID != pane.PID || after[i].TTY != pane.TTY ||
			!slices.Contains(out.PaneIDs, string(pane.ID)) {
			t.Fatalf("pane identity changed: before=%+v after=%+v", pane, after[i])
		}
	}

	value, err := window.Options().User(ctx, "@sesh_task")
	if err != nil {
		t.Fatal(err)
	}

	if task, ok := value.Local.Get(); !ok || task != "T7" {
		t.Fatal("window task option was lost")
	}

	for _, pane := range after {
		for name, want := range map[string]string{"@sesh_task": "T7", "@sesh_role": "agent"} {
			value, err := pane.Handle().Options().User(ctx, name)
			if err != nil {
				t.Fatal(err)
			}

			if got, ok := value.Local.Get(); !ok || got != want {
				t.Fatalf("pane %s option %s = %q", pane.ID, name, got)
			}
		}
	}

	links, err := window.Links(ctx)
	if err != nil || len(links) != 1 || links[0].Session().ID() != dest.ID {
		t.Fatalf("moved links = %+v, %v", links, err)
	}

	if fixture.clientSession(t, client) != source.ID ||
		fixture.clientSession(t, otherClient) != dest.ID {
		t.Fatal("move switched client sessions")
	}

	sourceActive, err := activeMoveWindow(ctx, source.Handle())
	if err != nil || sourceActive.ID() != keep.Window().ID() {
		t.Fatalf("source focus changed: %v", err)
	}

	active, err := activeMoveWindow(ctx, dest.Handle())
	if err != nil || active.ID() != destActive.ID() {
		t.Fatalf("destination focus changed: %v", err)
	}

	if _, err := destLast.Info(ctx); err != nil {
		t.Fatalf("existing destination window overwritten: %v", err)
	}

	state, err := session.LoadState(fixture.historyPath)
	if err != nil || state.GetLastAgentView(sourceIdentity, string(before[0].ID)) != 0 ||
		state.GetLastAgentView(destIdentity, string(before[0].ID)) != 123 ||
		state.GetLastAgentView(destIdentity, string(before[1].ID)) != 0 {
		t.Fatalf("seen/unseen migration failed: %+v, %v", state, err)
	}
}

func TestMoveLastWindowRemovesSourceButPreservesWindow(t *testing.T) {
	fixture := newWorkflowFixture(t)
	source := fixture.newLiveSession(t, "last-source")
	dest := fixture.fixtureSession(t)

	windows, err := source.Handle().Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	out, err := session.NewWorkflow(fixture.server, nil, "").
		MoveWindow(t.Context(), session.MoveWindowOptions{
			Source: moveSessionRow(
				t,
				fixture.server,
				source.ID,
			),
			Destination: moveSessionRow(t, fixture.server, dest.ID),
			WindowID:    string(windows[0].Window().ID()),
		})
	if err != nil || !out.Moved {
		t.Fatalf("move = %+v, %v", out, err)
	}

	if _, err := source.Handle().Info(t.Context()); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("last source session still exists: %v", err)
	}

	if _, err := windows[0].Window().Info(t.Context()); err != nil {
		t.Fatalf("window lost: %v", err)
	}
}

func TestMoveWindowRefusesStaleDestinationSourcePaneAndLinkedWindow(t *testing.T) {
	scenarios := []string{
		"stale destination",
		"stale pane",
		"stale window",
		"wrong owner",
		"linked",
		"different window",
		"different socket",
		"replaced server",
	}

	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			fixture := newWorkflowFixture(t)
			source := fixture.newLiveSession(t, "source")
			dest := fixture.fixtureSession(t)

			windows, err := source.Handle().Windows(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			panes, err := windows[0].Window().Panes(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			opts := session.MoveWindowOptions{
				Source:      moveSessionRow(t, fixture.server, source.ID),
				Destination: moveSessionRow(t, fixture.server, dest.ID),
				PaneID:      string(panes[0].ID),
			}

			switch scenario {
			case "stale destination":
				err = dest.Handle().Kill(t.Context())
			case "stale pane":
				opts.PaneID = "%999999"
			case "stale window":
				opts.PaneID = ""
				opts.WindowID = "@999999"
			case "wrong owner":
				opts.Source, opts.Destination = opts.Destination, opts.Source
			case "linked":
				_, err = windows[0].Window().Link(t.Context(), dest.Handle(), gotmux.LinkOptions{})
			case "different window":
				other, queryErr := activeMoveWindow(t.Context(), dest.Handle())
				err = queryErr
				opts.WindowID = string(other.ID())
			case "different socket":
				opts.Destination.TmuxServerSocket += "-other"
			case "replaced server":
				opts.Source.RuntimeIdentity.TmuxServerPID++
			}

			if err != nil {
				t.Fatal(err)
			}

			out, err := session.NewWorkflow(fixture.server, nil, "").MoveWindow(t.Context(), opts)
			if err == nil || out.Moved {
				t.Fatalf("accepted %s: %+v, %v", scenario, out, err)
			}

			if _, err := windows[0].Info(t.Context()); err != nil {
				t.Fatalf("source changed after refusal: %v", err)
			}
		})
	}
}

func activeMoveWindow(ctx context.Context, target gotmux.Session) (gotmux.Window, error) {
	_, links, err := target.WindowInfos(ctx)
	if err != nil {
		return gotmux.Window{}, errors.Join(err)
	}

	for _, link := range links {
		if link.Active {
			return link.Handle().Window(), nil
		}
	}

	return gotmux.Window{}, gotmux.ErrNotFound
}

func TestMoveWindowReportsCompletedMoveWhenHistoryFails(t *testing.T) {
	fixture := newWorkflowFixture(t)
	source := fixture.newLiveSession(t, "source")
	dest := fixture.fixtureSession(t)

	windows, err := source.Handle().Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(fixture.historyPath, []byte("invalid history"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := session.NewWorkflow(fixture.server, nil, fixture.historyPath).
		MoveWindow(t.Context(), session.MoveWindowOptions{
			Source: moveSessionRow(
				t,
				fixture.server,
				source.ID,
			),
			Destination: moveSessionRow(t, fixture.server, dest.ID),
			WindowID:    string(windows[0].Window().ID()),
		})
	if err == nil || !out.Moved || out.HistoryUpdated {
		t.Fatalf("history failure result = %+v, %v", out, err)
	}

	links, err := windows[0].Window().Links(t.Context())
	if err != nil || len(links) != 1 || links[0].Session().ID() != dest.ID {
		t.Fatalf("window not safely moved: %+v, %v", links, err)
	}
}
