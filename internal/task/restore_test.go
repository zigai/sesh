package task_test

import (
	"maps"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/snapshot/apps"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/task"
)

const seshBinary = "/opt/sesh/bin/sesh"

// restoreFixture is a snapshot with three awake task agents created in order t1, t2, t3, plus an
// editor pane and a pane outside any task, and a ledger that knows the three tasks.
type restoreFixture struct {
	store    task.Store
	manifest model.Manifest
}

func TestRestoreLaunchesKeepsCapturedLaunchesWhenWakingThePrevious(t *testing.T) {
	f := newRestoreFixture(t)

	launches, err := task.NewRestoreLaunches(f.store, config.WakeOnRestorePrevious, seshBinary).Override(t.Context(), f.manifest)
	if err != nil || len(launches) != 0 {
		t.Fatalf("Override() = %v, %v; want no overrides", launches, err)
	}
}

func TestRestoreLaunchesBringsEveryTaskBackAsleepWithNone(t *testing.T) {
	f := newRestoreFixture(t)

	launches, err := task.NewRestoreLaunches(f.store, config.WakeOnRestoreNone, seshBinary).Override(t.Context(), f.manifest)
	if err != nil {
		t.Fatal(err)
	}

	if got := slices.Sorted(maps.Keys(launches)); !slices.Equal(got, []string{"%1", "%3", "%5"}) {
		t.Fatalf("overridden panes = %v, want every agent pane", got)
	}

	placeholder := launches["%3"]
	if string(placeholder.Executable) != seshBinary || placeholder.Recovery != model.RecoveryRestart ||
		!slices.Equal(arguments(placeholder), []string{"sesh", "task", "placeholder", "t2"}) ||
		string(placeholder.Directory) != "/worktrees/t2" {
		t.Fatalf("launch of t2's agent = %+v, want its placeholder in its worktree", placeholder)
	}

	if _, captured := apps.Prepare(placeholder, nil); captured != apps.ReasonTaskPlaceholder {
		t.Fatalf("a restored placeholder is captured as %q, want it recognized again", captured)
	}
}

func TestRestoreLaunchesResumesOnlyTheNewestTasksWithACount(t *testing.T) {
	f := newRestoreFixture(t)

	launches, err := task.NewRestoreLaunches(f.store, "2", seshBinary).Override(t.Context(), f.manifest)
	if err != nil {
		t.Fatal(err)
	}

	if got := slices.Sorted(maps.Keys(launches)); !slices.Equal(got, []string{"%1"}) {
		t.Fatalf("overridden panes = %v, want only the oldest task's agent", got)
	}
}

func TestRestoreLaunchesCommitRecordsResumeOnlyForPlannedPanes(t *testing.T) {
	f := newRestoreFixture(t)
	launches := task.NewRestoreLaunches(f.store, config.WakeOnRestoreNone, seshBinary)

	if _, err := launches.Override(t.Context(), f.manifest); err != nil {
		t.Fatal(err)
	}

	if err := launches.Commit(t.Context(), []string{"%3"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	ledger, err := f.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	slept, _ := ledger.Find("t2")
	want := task.Resume{Harness: "pi", Session: "/sessions/t2.jsonl", Command: []string{"/usr/bin/pi", "--session", "/sessions/t2.jsonl"}}

	if slept.State != task.StateSleeping || slept.Resume.Harness != want.Harness || slept.Resume.Session != want.Session ||
		!slices.Equal(slept.Resume.Command, want.Command) {
		t.Fatalf("t2 = %s with %+v, want sleeping with %+v", slept.State, slept.Resume, want)
	}

	for _, id := range []string{"t1", "t3"} {
		if record, _ := ledger.Find(id); record.State != task.StateActive {
			t.Fatalf("%s state = %s, want a task outside the restore left active", id, record.State)
		}
	}
}

func TestRestoreLaunchesReconcilesCapturedAgentStates(t *testing.T) {
	for _, policy := range []string{config.WakeOnRestorePrevious, config.WakeOnRestoreNone, "1"} {
		t.Run(policy, func(t *testing.T) {
			f := newRestoreFixture(t)
			resume := task.Resume{Harness: "pi", Session: "/sessions/t2.jsonl", Command: []string{"/usr/bin/pi", "--session", "/sessions/t2.jsonl"}}
			f.manifest.Panes[2].Launch = apps.PlaceholderLaunch(seshBinary, "t2", f.manifest.Panes[2].Launch)

			if _, err := f.store.Update(t.Context(), func(ledger *task.Ledger) error {
				sleeping, _ := ledger.Find("t2")
				sleeping.Resume = resume
				ledger.Put(sleeping)
				awake, _ := ledger.Find("t3")
				awake.State = task.StateSleeping
				ledger.Put(awake)

				return nil
			}); err != nil {
				t.Fatal(err)
			}

			launches := task.NewRestoreLaunches(f.store, policy, seshBinary)
			if _, err := launches.Override(t.Context(), f.manifest); err != nil {
				t.Fatal(err)
			}

			if err := launches.Commit(t.Context(), []string{"%3", "%5"}); err != nil {
				t.Fatal(err)
			}

			ledger, err := f.store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			assertRestoredAgentStates(t, ledger, resume, policy)
		})
	}
}

func assertRestoredAgentStates(t *testing.T, ledger task.Ledger, resume task.Resume, policy string) {
	t.Helper()

	sleeping, _ := ledger.Find("t2")
	if sleeping.State != task.StateSleeping || sleeping.Resume.Harness != resume.Harness || sleeping.Resume.Session != resume.Session || !slices.Equal(sleeping.Resume.Command, resume.Command) {
		t.Fatalf("restored sleeping task = %+v, want sleeping with its resume evidence", sleeping)
	}

	awake, _ := ledger.Find("t3")

	wantState := task.StateActive
	if policy == config.WakeOnRestoreNone {
		wantState = task.StateSleeping
	}

	if awake.State != wantState {
		t.Fatalf("restored awake task state = %s, want %s", awake.State, wantState)
	}

	untouched, _ := ledger.Find("t1")
	if untouched.State != task.StateActive {
		t.Fatalf("unrestored task state = %s, want active", untouched.State)
	}
}

func newRestoreFixture(t *testing.T) restoreFixture {
	t.Helper()

	store := task.NewStore(filepath.Join(t.TempDir(), "state", "tasks.json"))

	if _, err := store.Update(t.Context(), func(ledger *task.Ledger) error {
		for i, id := range []string{"t1", "t2", "t3"} {
			record := projectRecord(ledger.NextID())
			record.CreatedAt += int64(i)
			record.Directory = "/worktrees/" + id
			ledger.Put(record)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	manifest := model.Manifest{}
	panes := []struct{ id, window, role, task string }{
		{"%1", "@1", task.RoleAgent, "t1"},
		{"%2", "@1", task.RoleEditor, "t1"},
		{"%3", "@2", task.RoleAgent, "t2"},
		{"%5", "@3", task.RoleAgent, "t3"},
		{"%6", "@4", "", ""},
	}

	for _, window := range []struct{ id, task string }{{"@1", "t1"}, {"@2", "t2"}, {"@3", "t3"}, {"@4", ""}} {
		manifest.Windows = append(manifest.Windows, model.Window{
			ID: window.id, Options: userOption("window", task.OptionTask, window.task),
		})
	}

	for _, pane := range panes {
		launch := resumeLaunch("/worktrees/" + pane.task)
		if pane.role != task.RoleAgent {
			launch = model.Launch{Executable: model.Bytes("/bin/sh"), Arguments: []model.Bytes{model.Bytes("sh")}, Directory: model.Bytes("/"), Environment: nil, Birth: nil, Recovery: model.RecoveryRestart, Reason: ""}
		}

		manifest.Panes = append(manifest.Panes, model.Pane{
			ID: pane.id, Window: pane.window, Options: userOption("pane", task.OptionRole, pane.role), Launch: launch,
		})
	}

	return restoreFixture{store: store, manifest: manifest}
}

func resumeLaunch(dir string) model.Launch {
	return model.Launch{
		Executable:  model.Bytes("/usr/bin/pi"),
		Arguments:   []model.Bytes{model.Bytes("pi"), model.Bytes("--session"), model.Bytes("/sessions/" + filepath.Base(dir) + ".jsonl")},
		Directory:   model.Bytes(dir),
		Environment: nil,
		Birth:       nil,
		Recovery:    model.RecoveryResume,
		Reason:      "",
	}
}

func userOption(scope, name, value string) []model.Option {
	if value == "" {
		return nil
	}

	return []model.Option{{Scope: scope, Name: model.Bytes(name), Value: model.Bytes(value), Present: true, Inherited: false}}
}

func arguments(launch model.Launch) []string {
	values := make([]string, 0, len(launch.Arguments))
	for _, argument := range launch.Arguments {
		values = append(values, string(argument))
	}

	return values
}
