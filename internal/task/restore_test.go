package task_test

import (
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/snapshot/apps"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/task"
)

const seshBinary = "/opt/sesh/bin/sesh"

type restoreFixture struct {
	store    task.Store
	manifest model.Manifest
}

func TestRestoreLaunchesKeepsCapturedLaunchesWhenWakingThePrevious(t *testing.T) {
	f := newRestoreFixture(t)

	launches, err := task.NewRestoreLaunches(f.store, wakePolicy(t, config.WakeOnRestorePrevious), seshBinary).Override(t.Context(), f.manifest)
	if err != nil {
		t.Fatal(err)
	}

	for _, pane := range f.manifest.Panes {
		launch, ok := launches[pane.ID]
		if !ok {
			continue
		}

		launch.Environment = pane.Launch.Environment
		if !reflect.DeepEqual(launch, pane.Launch) {
			t.Errorf("launch of %s = %+v, want its captured launch %+v", pane.ID, launch, pane.Launch)
		}
	}
}

func TestRestoreLaunchesStartTaskPanesWithTheTaskEnvironment(t *testing.T) {
	f := newRestoreFixture(t)
	f.manifest.Panes[0].Launch.Environment = []model.Variable{
		{Name: model.Bytes("PATH"), Value: model.Bytes("/bin"), State: "set", Hidden: false},
		{Name: model.Bytes(task.EnvTask), Value: model.Bytes("stale"), State: "set", Hidden: false},
	}

	for _, policy := range []string{config.WakeOnRestorePrevious, config.WakeOnRestoreNone} {
		launches, err := task.NewRestoreLaunches(f.store, wakePolicy(t, policy), seshBinary).Override(t.Context(), f.manifest)
		if err != nil {
			t.Fatal(err)
		}

		if got := slices.Sorted(maps.Keys(launches)); !slices.Equal(got, []string{"%1", "%2", "%3", "%5"}) {
			t.Fatalf("%s: overridden panes = %v, want every launched task pane", policy, got)
		}

		env := environment(launches["%1"])
		if env["PATH"] != "/bin" || env[task.EnvTask] != "t1" || env[task.EnvWorktree] != "/worktrees/t1" || env[task.EnvProject] == "" {
			t.Errorf("%s: t1's agent environment = %v, want the captured one with t1's task variables", policy, env)
		}

		if env := environment(launches["%2"]); env[task.EnvTask] != "t1" {
			t.Errorf("%s: t1's editor environment = %v, want t1's task variables", policy, env)
		}
	}
}

func TestRestoreLaunchesRunsTheRecordedCommandOfRestartingTaskPanesOnly(t *testing.T) {
	f := newRestoreFixture(t)

	restart := func(pane, value string) {
		index := slices.IndexFunc(f.manifest.Panes, func(p model.Pane) bool { return p.ID == pane })
		f.manifest.Panes[index].Options = append(f.manifest.Panes[index].Options, userOption("pane", task.OptionRestart, value)...)
	}

	restart("%2", `{"directory":"/worktrees/t1/web","argv":["/bin/sh","-c","npm run dev"]}`)
	restart("%6", `{"directory":"/srv","argv":["/bin/sh","-c","serve"]}`)

	launches, err := task.NewRestoreLaunches(f.store, wakePolicy(t, config.WakeOnRestorePrevious), seshBinary).Override(t.Context(), f.manifest)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := launches["%6"]; ok {
		t.Fatal("a pane outside any task window is overridden")
	}

	launch := launches["%2"]
	if string(launch.Executable) != "/bin/sh" || !slices.Equal(arguments(launch), []string{"/bin/sh", "-c", "npm run dev"}) ||
		string(launch.Directory) != "/worktrees/t1/web" || launch.Recovery != model.RecoveryRestart {
		t.Fatalf("restarting pane launch = %+v, want its recorded command in its directory", launch)
	}
}

func TestRestoreLaunchesIgnoresAnUnreadableRestartCommand(t *testing.T) {
	for _, value := range []string{`not json`, `{"directory":"/w","argv":[]}`, `{"directory":"/w","argv":["sh"]}`, `{"directory":"w","argv":["/bin/sh"]}`} {
		f := newRestoreFixture(t)
		f.manifest.Panes[1].Options = append(f.manifest.Panes[1].Options, userOption("pane", task.OptionRestart, value)...)

		launches, err := task.NewRestoreLaunches(f.store, wakePolicy(t, config.WakeOnRestorePrevious), seshBinary).Override(t.Context(), f.manifest)
		if launch := launches["%2"]; err != nil || !slices.Equal(arguments(launch), []string{"sh"}) {
			t.Errorf("Override() with restart command %s = %+v, %v; want the captured launch", value, launch, err)
		}
	}
}

func TestRestoreLaunchesBringsEveryTaskBackAsleepWithNone(t *testing.T) {
	f := newRestoreFixture(t)

	launches, err := task.NewRestoreLaunches(f.store, wakePolicy(t, config.WakeOnRestoreNone), seshBinary).Override(t.Context(), f.manifest)
	if err != nil {
		t.Fatal(err)
	}

	if got := placeholders(launches); !slices.Equal(got, []string{"%1", "%3", "%5"}) {
		t.Fatalf("panes behind a placeholder = %v, want every agent pane", got)
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

	launches, err := task.NewRestoreLaunches(f.store, wakePolicy(t, "2"), seshBinary).Override(t.Context(), f.manifest)
	if err != nil {
		t.Fatal(err)
	}

	if got := placeholders(launches); !slices.Equal(got, []string{"%1"}) {
		t.Fatalf("panes behind a placeholder = %v, want only the oldest task's agent", got)
	}
}

func TestRestoreLaunchesCommitRecordsResumeOnlyForPlannedPanes(t *testing.T) {
	f := newRestoreFixture(t)
	launches := task.NewRestoreLaunches(f.store, wakePolicy(t, config.WakeOnRestoreNone), seshBinary)

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

			launches := task.NewRestoreLaunches(f.store, wakePolicy(t, policy), seshBinary)
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
		{"%2", "@1", "editor", "t1"},
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

func placeholders(launches map[string]model.Launch) []string {
	var panes []string

	for _, pane := range slices.Sorted(maps.Keys(launches)) {
		if string(launches[pane].Executable) == seshBinary {
			panes = append(panes, pane)
		}
	}

	return panes
}

func environment(launch model.Launch) map[string]string {
	values := make(map[string]string, len(launch.Environment))
	for _, variable := range launch.Environment {
		values[string(variable.Name)] = string(variable.Value)
	}

	return values
}

func wakePolicy(t *testing.T, value string) config.WakePolicy {
	t.Helper()

	policy, err := config.ParseWakeOnRestore(value)
	if err != nil {
		t.Fatal(err)
	}

	return policy
}
