//go:build integration

package task_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/task"
)

type rect struct{ left, top, width, height int }

func TestSplitsLayoutPlacesEachPaneWhereItsSplitPutsIt(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	mkdir(t, filepath.Join(dir, "docs"))

	f.cfg.Tasks.Panes = []config.TaskPaneConfig{
		{Name: "editor", Command: []string{"sleep", "300"}, Dir: "", Restart: true},
		{Name: "shell", Command: nil, Dir: "docs", Restart: false},
	}
	f.cfg.Tasks.Layouts = []config.TaskLayoutConfig{{
		Splits: []config.TaskSplitConfig{
			{Pane: "editor", Of: "agent", Direction: "right", Size: "40%"},
			{Pane: "shell", Of: "editor", Direction: "below", Size: "8"},
		},
		Focus: "editor",
	}}

	place := scratchPlace(t, f, dir)

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: sleepHarness})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	panes := rolePanes(t, f, place.Session, record.ID)
	want := map[string]rect{"agent": {0, 0, 47, 24}, "editor": {48, 0, 32, 15}, "shell": {48, 16, 32, 8}}

	for role, wanted := range want {
		pane := panes[role]
		if got := (rect{pane.Left, pane.Top, pane.Width, pane.Height}); got != wanted {
			t.Errorf("%s pane = %+v, want %+v", role, got, wanted)
		}
	}

	if !panes["editor"].Active {
		t.Error("the focus pane is not active")
	}

	if got := panes["shell"].CurrentPath; got != filepath.Join(dir, "docs") {
		t.Errorf("shell pane runs in %q, want its dir in the task directory", got)
	}

	var restart struct {
		Directory string   `json:"directory"`
		Argv      []string `json:"argv"`
	}

	value, _ := panes["editor"].Raw(task.OptionRestart)
	if err := json.Unmarshal(value, &restart); err != nil || restart.Directory != dir || len(restart.Argv) == 0 {
		t.Errorf("editor restart command = %q, want its command and directory", value)
	}

	if value, found := panes["shell"].Raw(task.OptionRestart); found && len(value) > 0 {
		t.Errorf("shell pane has restart command %q, want none", value)
	}
}

func TestStartTakesTheFirstLayoutTheWindowAndHarnessFit(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	f.cfg.Tasks.Layouts = []config.TaskLayoutConfig{
		{Name: "huge", When: config.TaskLayoutWhen{MinWidth: 1000}, Arrange: "even-horizontal"},
		{Name: "below", When: config.TaskLayoutWhen{Harness: []string{sleepHarness}}, Splits: []config.TaskSplitConfig{{Pane: "editor", Of: "agent", Direction: "below"}}},
		{Name: "beside", Arrange: config.ArrangeMainVertical, MainSize: "60%"},
	}

	place := scratchPlace(t, f, dir)

	tests := []struct {
		request task.Request
		below   bool
	}{
		{request: task.Request{Harness: envHarness}, below: false},
		{request: task.Request{Harness: sleepHarness}, below: true},
		{request: task.Request{Harness: sleepHarness, Layout: "beside"}, below: false},
	}

	for _, tt := range tests {
		record, err := f.service.Start(t.Context(), place, tt.request)
		if err != nil {
			t.Fatalf("Start(%+v) error = %v", tt.request, err)
		}

		panes := rolePanes(t, f, place.Session, record.ID)
		if below := panes["editor"].Top > 0; below != tt.below {
			t.Errorf("Start(%+v) editor pane at %d,%d; want below the agent: %v", tt.request, panes["editor"].Left, panes["editor"].Top, tt.below)
		}
	}

	if _, err := f.service.Start(t.Context(), place, task.Request{Harness: envHarness, Layout: "missing"}); !errors.Is(err, task.ErrUnknownLayout) {
		t.Fatalf("Start() with an unknown layout error = %v, want ErrUnknownLayout", err)
	}
}

func TestMainSizeSetsTheAgentsShareOfAnArrangement(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	f.cfg.Tasks.Layouts = []config.TaskLayoutConfig{{Arrange: config.ArrangeMainVertical, MainSize: "60%"}}

	place := scratchPlace(t, f, dir)

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: envHarness})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if got := rolePanes(t, f, place.Session, record.ID)["agent"].Width; got < 47 || got > 48 {
		t.Fatalf("agent pane width = %d, want about 60%% of 80", got)
	}
}

func TestRelayoutFitsTheWindowsNewSizeAndReopensClosedPanes(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	place := scratchPlace(t, f, dir)

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: envHarness})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	panes := rolePanes(t, f, place.Session, record.ID)
	if panes["editor"].Left == 0 {
		t.Fatalf("editor pane is not beside the agent in a landscape window: %+v", panes["editor"])
	}

	killPane(t, f.server, panes["editor"].ID)

	window, err := f.server.Window(t.Context(), panes["agent"].WindowID)
	if err != nil {
		t.Fatal(err)
	}

	if err := window.Resize(t.Context(), gotmux.Size{Width: 80, Height: 60}); err != nil {
		t.Fatal(err)
	}

	if err := f.service.Relayout(t.Context(), locatePane(t, f, panes["agent"].ID), ""); err != nil {
		t.Fatalf("Relayout() error = %v", err)
	}

	panes = rolePanes(t, f, place.Session, record.ID)
	if editor, ok := panes["editor"]; !ok || editor.Left != 0 || editor.Top == 0 {
		t.Fatalf("editor pane after relayout = %+v, want it reopened below the agent in a portrait window", editor)
	}
}

func TestRelayoutKeepsAPaneTheSplitsDoNotName(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	f.cfg.Tasks.Layouts = []config.TaskLayoutConfig{{Splits: []config.TaskSplitConfig{{Pane: "editor", Of: "agent", Direction: "right"}}}}
	place := scratchPlace(t, f, dir)

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: envHarness})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	agentID := rolePanes(t, f, place.Session, record.ID)["agent"].ID

	agent, err := f.server.Pane(t.Context(), agentID)
	if err != nil {
		t.Fatal(err)
	}

	extra, err := agent.Split(t.Context(), gotmux.SplitOptions{Direction: gotmux.DirectionVertical, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	if err := f.service.Relayout(t.Context(), locatePane(t, f, agentID), ""); err != nil {
		t.Fatalf("Relayout() error = %v", err)
	}

	panes := rolePanes(t, f, place.Session, record.ID)
	if got := panes[""].ID; got != extra.ID() {
		t.Fatalf("pane without a role after relayout = %q, want the added pane %q", got, extra.ID())
	}

	if panes["editor"].Left <= panes["agent"].Left || panes["editor"].Top != panes["agent"].Top {
		t.Errorf("editor pane at %d,%d, want it right of the agent at %d,%d", panes["editor"].Left, panes["editor"].Top, panes["agent"].Left, panes["agent"].Top)
	}
}

func TestSplitsLayoutPlacesPanesLeftOfAndAbove(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	f.cfg.Tasks.Panes = []config.TaskPaneConfig{
		{Name: "editor", Command: nil, Dir: "", Restart: false},
		{Name: "shell", Command: nil, Dir: "", Restart: false},
	}
	f.cfg.Tasks.Layouts = []config.TaskLayoutConfig{{Splits: []config.TaskSplitConfig{
		{Pane: "editor", Of: "agent", Direction: "left", Size: "30"},
		{Pane: "shell", Of: "editor", Direction: "above", Size: "6"},
	}}}
	place := scratchPlace(t, f, dir)

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: envHarness})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	panes := rolePanes(t, f, place.Session, record.ID)
	want := map[string]rect{"shell": {0, 0, 30, 6}, "editor": {0, 7, 30, 17}, "agent": {31, 0, 49, 24}}

	for role, wanted := range want {
		pane := panes[role]
		if got := (rect{pane.Left, pane.Top, pane.Width, pane.Height}); got != wanted {
			t.Errorf("%s pane = %+v, want %+v", role, got, wanted)
		}
	}
}

func TestStartRefusesAPaneDirectoryThatDoesNotExist(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	f.cfg.Tasks.Panes = []config.TaskPaneConfig{{Name: "docs", Command: nil, Dir: "missing", Restart: false}}
	place := scratchPlace(t, f, dir)

	if _, err := f.service.Start(t.Context(), place, task.Request{Harness: envHarness}); !errors.Is(err, task.ErrPaneDirectory) {
		t.Fatalf("Start() error = %v, want ErrPaneDirectory", err)
	}
}

func TestProjectTaskWindowsOpenTheProjectsPanes(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	f.cfg.Projects.Overrides = map[string]config.ProjectConfig{"shop": {
		TaskPanes: []config.TaskPaneConfig{{Name: "server", Command: []string{"sleep", "300"}, Dir: "", Restart: false}},
	}}
	place := projectPlace(t, f, repo)

	record, err := f.service.Start(t.Context(), place, inNewWorktree(envHarness))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	panes := rolePanes(t, f, place.Session, record.ID)
	if _, ok := panes["server"]; !ok || len(panes) != 3 {
		t.Fatalf("project task panes = %v, want agent, editor and the project's server", panes)
	}
}

func TestARestartingPaneRunsAgainWithItsTaskAfterARestore(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	marker := filepath.Join(f.base, "marker")
	f.cfg.Tasks.Panes = []config.TaskPaneConfig{{
		Name: "server", Command: []string{"sh", "-c", `echo "$SESH_TASK" > "$0"; exec sleep 300`, marker}, Dir: "", Restart: true,
	}}
	place := scratchPlace(t, f, dir)

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: envHarness})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	waitForFile(t, marker)

	repo, err := store.Open(t.Context(), filepath.Join(f.base, "snapshots"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = repo.Close() })

	leases := filepath.Join(f.base, "leases")

	saved, err := engine.SaveObservation(t.Context(), repo, f.server, []gotmux.SessionID{place.Session}, engine.SaveConfig{
		LeaseRoot: leases, ProcessEnrichment: true, Profile: engine.RecoveryPractical,
	})
	if err != nil {
		t.Fatalf("SaveObservation() error = %v", err)
	}

	session, err := f.server.Session(t.Context(), place.Session)
	if err != nil {
		t.Fatal(err)
	}

	if err := session.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	if _, err := engine.Restore(t.Context(), repo, f.server, engine.RestoreConfig{
		LeaseRoot: leases, Generation: saved.Generation, LaunchProcesses: true, Profile: engine.RecoveryPractical,
		Launches: task.NewRestoreLaunches(f.store, config.WakeOnRestorePrevious, "/nonexistent/sesh"),
	}); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	if got := waitForFile(t, marker); got != record.ID+"\n" {
		t.Fatalf("restarted pane wrote task %q, want %q", got, record.ID)
	}
}

func scratchPlace(t *testing.T, f fixture, dir string) task.Place {
	t.Helper()

	session := newSession(t, f.server, "scratch", dir)

	return locate(t, f, sessionWindows(t, f.server, session.ID())[0].Window())
}

func locatePane(t *testing.T, f fixture, pane gotmux.PaneID) task.Place {
	t.Helper()

	place, err := f.service.Locate(t.Context(), pane)
	if err != nil {
		t.Fatalf("Locate() error = %v", err)
	}

	return place
}

func rolePanes(t *testing.T, f fixture, session gotmux.SessionID, id string) map[string]gotmux.PaneInfo {
	t.Helper()

	window, err := f.server.Window(t.Context(), taskWindow(t, f.server, session, id).ID)
	if err != nil {
		t.Fatal(err)
	}

	infos, err := window.PanesWith(t.Context(), gotmux.QueryOptions{Filter: "", ExtraFields: []string{task.OptionRole, task.OptionRestart}})
	if err != nil {
		t.Fatal(err)
	}

	panes := make(map[string]gotmux.PaneInfo, len(infos))
	for _, info := range infos {
		role, _ := info.Raw(task.OptionRole)
		panes[string(role)] = info
	}

	return panes
}

func killPane(t *testing.T, server *gotmux.Server, id gotmux.PaneID) {
	t.Helper()

	pane, err := server.Pane(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	if err := pane.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}
}
