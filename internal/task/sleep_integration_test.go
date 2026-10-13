//go:build integration

package task_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/project"
	"github.com/zigai/sesh/internal/task"
)

const placeholderMarker = "placeholder-shown"

// paneAgent reports one agent with native resume evidence in every pane it is asked about, the
// way AHT reports the agent it tracks there.
type paneAgent struct {
	server     *gotmux.Server
	activity   string
	harness    string
	executable string
	session    string
	missing    bool
}

type sleepFixture struct {
	fixture

	agents  *paneAgent
	harness string
	record  task.Record
	pane    gotmux.PaneID
}

func TestSleepStopsAnIdleAgentAndRecordsHowToResumeIt(t *testing.T) {
	f := newSleepFixture(t)
	pid := harnessPID(t, f)

	record, err := f.service.Sleep(t.Context(), f.record.ID)
	if err != nil {
		t.Fatalf("Sleep() error = %v", err)
	}

	if record.State != task.StateSleeping {
		t.Fatalf("Sleep() state = %s, want sleeping", record.State)
	}

	want := []string{f.harness, "--session", f.agents.session}
	if record.Resume.Harness != fakeHarnessName || !slices.Equal(record.Resume.Command, want) {
		t.Fatalf("resume evidence = %+v, want harness %s and %v", record.Resume, fakeHarnessName, want)
	}

	if processAlive(pid) {
		t.Fatal("agent process still runs after sleep")
	}

	waitForFile(t, filepath.Join(f.record.Directory, placeholderMarker))
	assertStoredState(t, f, task.StateSleeping)
}

func TestSleepRefusesAnAgentThatIsNotIdle(t *testing.T) {
	f := newSleepFixture(t)
	f.agents.activity = "running"

	assertSleepRefused(t, f, task.ErrAgentBusy)
}

func TestSleepRefusesAHarnessWhoseStopIsNotVerified(t *testing.T) {
	f := newSleepFixture(t)
	f.agents.harness = "cursor"

	assertSleepRefused(t, f, task.ErrSleepUnsupported)
}

func TestSleepRefusesAPaneWithoutTrackedAgent(t *testing.T) {
	f := newSleepFixture(t)
	f.agents.missing = true

	assertSleepRefused(t, f, task.ErrNoAgent)
}

func TestSleepRefusesWhenThePaneDoesNotRunTheHarness(t *testing.T) {
	f := newSleepFixture(t)

	if err := syscall.Kill(harnessPID(t, f), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}

	waitForForeground(t, f, func(command string) bool { return command != fakeHarnessName })

	assertSleepRefused(t, f, task.ErrNoResume)
}

func TestInterruptedSleepRetainsResumeAndCanWakeSafely(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(fmt.Sprintf("harness_running=%t", running), func(t *testing.T) {
			f := newSleepFixtureWith(t, func(dir string) {
				if err := os.WriteFile(filepath.Join(dir, sleepSignalGate), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			})
			waitForFile(t, filepath.Join(f.record.Directory, sleepSignalReady))
			pid := harnessPID(t, f)

			interrupted := interruptSleep(t, f)

			assertSleepResume(t, f, interrupted)

			ledger, err := f.store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			stored, _ := ledger.Find(f.record.ID)
			assertSleepResume(t, f, stored)

			if _, err := os.Stat(filepath.Join(f.record.Directory, placeholderMarker)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("placeholder after interrupted sleep: %v, want absent", err)
			}

			if !running {
				releaseStoppedHarness(t, f, pid)
			}

			assertWakeAfterInterruptedSleep(t, f, pid, running)
		})
	}
}

func releaseStoppedHarness(t *testing.T, f sleepFixture, pid int) {
	t.Helper()

	if err := os.Remove(filepath.Join(f.record.Directory, sleepSignalGate)); err != nil {
		t.Fatal(err)
	}

	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}

	waitForForeground(t, f, func(command string) bool { return command != fakeHarnessName })
}

func interruptSleep(t *testing.T, f sleepFixture) task.Record {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	type result struct {
		record task.Record
		err    error
	}

	finished := make(chan result, 1)
	done := make(chan struct{})

	go func() {
		record, err := f.service.Sleep(ctx, f.record.ID)
		finished <- result{record: record, err: err}

		close(done)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitForFile(t, filepath.Join(f.record.Directory, sleepSignalReceived))
	cancel()

	got := <-finished
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Sleep() error = %v, want cancellation after the stop signal", got.err)
	}

	return got.record
}

func assertSleepResume(t *testing.T, f sleepFixture, record task.Record) {
	t.Helper()

	want := []string{f.harness, "--session", f.agents.session}
	if record.State != task.StateSleeping || record.Resume.Harness != fakeHarnessName ||
		record.Resume.Session != f.agents.session || !slices.Equal(record.Resume.Command, want) {
		t.Fatalf("sleep record = %+v, want sleeping with resume %v", record, want)
	}
}

func assertWakeAfterInterruptedSleep(t *testing.T, f sleepFixture, pid int, running bool) {
	t.Helper()

	woken, err := f.service.Wake(t.Context(), f.record.ID)
	if err != nil {
		t.Fatalf("Wake() after interrupted sleep: %v", err)
	}

	if woken.Awake != running || woken.Record.State != task.StateActive {
		t.Fatalf("Wake() = %+v, want active with Awake=%t", woken, running)
	}

	if running {
		if !processAlive(pid) || readArgs(t, f) != "" {
			t.Fatal("Wake() replaced the already-running harness")
		}
	} else {
		if got := waitForArgs(t, f, "--session "+f.agents.session); got != "--session "+f.agents.session {
			t.Fatalf("resumed harness arguments = %q", got)
		}
	}

	assertStoredState(t, f, task.StateActive)
}

func TestSleepTimeoutRetainsResumeAndWakeKeepsTheRunningAgent(t *testing.T) {
	f := newSleepFixtureWith(t, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, fakeHarnessIgnoreTerm), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	pid := harnessPID(t, f)

	_, err := f.service.Sleep(t.Context(), f.record.ID)
	if !errors.Is(err, task.ErrAgentDidNotStop) {
		t.Fatalf("Sleep() error = %v, want %v", err, task.ErrAgentDidNotStop)
	}

	if !processAlive(pid) {
		t.Fatal("sleep escalated past the stop signal")
	}

	assertStoredState(t, f, task.StateSleeping)

	ledger, err := f.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	record, _ := ledger.Find(f.record.ID)

	assertSleepResume(t, f, record)

	woken, err := f.service.Wake(t.Context(), f.record.ID)
	if err != nil {
		t.Fatalf("Wake() after stop timeout: %v", err)
	}

	if !woken.Awake || woken.Record.State != task.StateActive || !processAlive(pid) || readArgs(t, f) != "" {
		t.Fatalf("Wake() = %+v, want the original running harness reconciled", woken)
	}

	assertStoredState(t, f, task.StateActive)
}

func TestWakeRelaunchesTheAgentThroughItsResumeCommand(t *testing.T) {
	f := newSleepFixture(t)
	sleepTask(t, f)

	woken, err := f.service.Wake(t.Context(), f.record.ID)
	if err != nil {
		t.Fatalf("Wake() error = %v", err)
	}

	if woken.Awake || woken.Record.State != task.StateActive {
		t.Fatalf("Wake() = %+v, want a relaunch that leaves the task active", woken)
	}

	if got := waitForArgs(t, f, "--session "+f.agents.session); got != "--session "+f.agents.session {
		t.Fatalf("relaunched harness arguments = %q", got)
	}

	assertStoredState(t, f, task.StateActive)
}

func TestWakeResumesATaskWhoseEarlierWakeWasInterrupted(t *testing.T) {
	f := newSleepFixture(t)
	sleepTask(t, f)
	setStoredState(t, f, task.StateWaking)

	if _, err := f.service.Wake(t.Context(), f.record.ID); err != nil {
		t.Fatalf("Wake() error = %v", err)
	}

	waitForArgs(t, f, "--session "+f.agents.session)
	assertStoredState(t, f, task.StateActive)
}

func TestWakeOnlyMarksAwakeATaskWhoseHarnessAlreadyRuns(t *testing.T) {
	f := newSleepFixture(t)
	sleepTask(t, f)

	pane, err := f.server.Pane(t.Context(), f.pane)
	if err != nil {
		t.Fatal(err)
	}

	if err := pane.Respawn(t.Context(), gotmux.RespawnOptions{
		Program: gotmux.Exec(f.harness, "--again"), Dir: f.record.Directory, Env: nil, TmuxEnv: nil,
		KillRunning: true, PreserveEnvironment: false,
	}); err != nil {
		t.Fatal(err)
	}

	waitForArgs(t, f, "--again")
	setStoredState(t, f, task.StateSleeping)

	woken, err := f.service.Wake(t.Context(), f.record.ID)
	if err != nil {
		t.Fatalf("Wake() error = %v", err)
	}

	if !woken.Awake || readArgs(t, f) != "--again" {
		t.Fatalf("Wake() = %+v with harness arguments %q, want the running harness kept", woken, readArgs(t, f))
	}

	assertStoredState(t, f, task.StateActive)
}

func TestWakeRefusesATaskThatIsNotAsleep(t *testing.T) {
	f := newSleepFixture(t)

	if _, err := f.service.Wake(t.Context(), f.record.ID); !errors.Is(err, task.ErrNotAsleep) {
		t.Fatalf("Wake() error = %v, want %v", err, task.ErrNotAsleep)
	}
}

func (agents *paneAgent) AgentsIn(ctx context.Context, pane gotmux.PaneID) ([]task.Agent, error) {
	if agents.missing {
		return nil, nil
	}

	info, err := agents.server.Pane(ctx, pane)
	if err != nil {
		return nil, fmt.Errorf("find pane: %w", err)
	}

	details, err := info.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect pane: %w", err)
	}

	return []task.Agent{{
		Harness: agents.harness, Activity: agents.activity, SessionID: "", SessionPath: agents.session,
		ResumeCommand: []string{fakeHarnessName, "--session", agents.session}, Executable: agents.executable,
		PanePID: details.PID,
	}}, nil
}

func newSleepFixture(t *testing.T) sleepFixture {
	t.Helper()

	return newSleepFixtureWith(t, func(string) {})
}

// newSleepFixtureWith starts a project task whose agent is the fake harness. prepare runs in the
// worktree before the harness starts.
func newSleepFixtureWith(t *testing.T, prepare func(dir string)) sleepFixture {
	t.Helper()

	f := newFixture(t, true)
	harness := filepath.Join(f.base, "bin", fakeHarnessName)
	mkdir(t, filepath.Dir(harness))

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(executable, harness); err != nil {
		t.Fatal(err)
	}

	agents := &paneAgent{
		server: f.server, activity: "idle", harness: fakeHarnessName, executable: harness,
		session: filepath.Join(f.base, "sessions", "one.jsonl"), missing: false,
	}

	catalog, err := project.NewCatalog(config.ProjectsConfig{Roots: []string{filepath.Join(f.base, "code")}, Extra: nil, Overrides: nil})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultTasksConfig()
	cfg.Panes = []config.TaskPaneConfig{{Name: "editor", Command: []string{"sleep", "300"}, Dir: "", Restart: false}}
	cfg.Harness = []config.HarnessConfig{{Name: fakeHarnessName, Command: []string{"sh", "-c", `while [ ! -e ready ]; do sleep 0.05; done; exec "$0"`, harness}}}

	full := config.DefaultConfig()
	full.Tasks = cfg

	f.service = task.NewService(task.Options{
		Server: f.server, Store: f.store, Catalog: catalog, Config: cfg, Windows: full.TaskWindowFor, WorktreeDir: f.worktreeDir,
		LookPath: exec.LookPath, Now: time.Now, Agents: agents,
		Placeholder: func(string) []string {
			return []string{"sh", "-c", "echo shown > " + placeholderMarker + "; exec sleep 300"}
		},
	})

	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))

	record, err := f.service.Start(t.Context(), projectPlace(t, f, repo), inNewWorktree(fakeHarnessName))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	prepare(record.Directory)

	if err := os.WriteFile(filepath.Join(record.Directory, "ready"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	pane := agentPane(t, f, record.ID)

	sleeping := sleepFixture{fixture: f, agents: agents, harness: harness, record: record, pane: pane}
	waitForForeground(t, sleeping, func(command string) bool { return command == fakeHarnessName })

	return sleeping
}

func assertSleepRefused(t *testing.T, f sleepFixture, want error) {
	t.Helper()

	_, err := f.service.Sleep(t.Context(), f.record.ID)
	if !errors.Is(err, want) {
		t.Fatalf("Sleep() error = %v, want %v", err, want)
	}

	assertStoredState(t, f, task.StateActive)
}

func sleepTask(t *testing.T, f sleepFixture) {
	t.Helper()

	if _, err := f.service.Sleep(t.Context(), f.record.ID); err != nil {
		t.Fatalf("Sleep() error = %v", err)
	}

	waitForFile(t, filepath.Join(f.record.Directory, placeholderMarker))

	if err := os.Remove(filepath.Join(f.record.Directory, fakeHarnessArgsFile)); err != nil {
		t.Fatal(err)
	}
}

func assertStoredState(t *testing.T, f sleepFixture, want task.State) {
	t.Helper()

	ledger, err := f.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if record, _ := ledger.Find(f.record.ID); record.State != want {
		t.Fatalf("stored state = %s, want %s", record.State, want)
	}
}

func setStoredState(t *testing.T, f sleepFixture, state task.State) {
	t.Helper()

	if _, err := f.store.Update(t.Context(), func(ledger *task.Ledger) error {
		record, _ := ledger.Find(f.record.ID)
		record.State = state
		ledger.Put(record)

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func agentPane(t *testing.T, f fixture, id string) gotmux.PaneID {
	t.Helper()

	panes, err := f.server.PanesWith(t.Context(), gotmux.QueryOptions{Filter: "", ExtraFields: []string{task.OptionTask, task.OptionRole}})
	if err != nil {
		t.Fatal(err)
	}

	for _, pane := range panes {
		taskID, _ := pane.Raw(task.OptionTask)
		role, _ := pane.Raw(task.OptionRole)

		if string(taskID) == id && string(role) == task.RoleAgent {
			return pane.ID
		}
	}

	t.Fatalf("task %s has no agent pane", id)

	return ""
}

func harnessPID(t *testing.T, f sleepFixture) int {
	t.Helper()

	pid := 0

	waitForForeground(t, f, func(string) bool {
		pid = foregroundPID(t, f)

		return pid > 0
	})

	return pid
}

func foregroundPID(t *testing.T, f sleepFixture) int {
	t.Helper()

	output, err := exec.CommandContext(t.Context(), "pgrep", "-f", "-x", f.harness+".*").Output()
	if err != nil {
		return 0
	}

	pid, _ := strconv.Atoi(strings.Fields(string(output))[0])

	return pid
}

func waitForForeground(t *testing.T, f sleepFixture, ready func(command string) bool) {
	t.Helper()

	deadline := time.Now().Add(envWaitLimit)

	for time.Now().Before(deadline) {
		pane, err := f.server.Pane(t.Context(), f.pane)
		if err != nil {
			t.Fatal(err)
		}

		info, err := pane.Info(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		if ready(info.CurrentCommand) {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("agent pane did not reach the expected foreground command")
}

func waitForArgs(t *testing.T, f sleepFixture, want string) string {
	t.Helper()

	deadline := time.Now().Add(envWaitLimit)

	for time.Now().Before(deadline) {
		if got := readArgs(t, f); got == want {
			return got
		}

		time.Sleep(20 * time.Millisecond)
	}

	return readArgs(t, f)
}

func readArgs(t *testing.T, f sleepFixture) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(f.record.Directory, fakeHarnessArgsFile))
	if err != nil {
		return ""
	}

	return string(data)
}

func processAlive(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}

	fields := strings.Fields(string(data))

	return len(fields) > 2 && fields[2] != "Z"
}
