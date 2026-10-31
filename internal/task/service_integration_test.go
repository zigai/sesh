//go:build integration

package task_test

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/git"
	"github.com/zigai/sesh/internal/project"
	"github.com/zigai/sesh/internal/task"
)

const envWaitLimit = 10 * time.Second

const (
	envHarness      = "fake"
	sleepHarness    = "other"
	promptedHarness = "prompted"
	functionHarness = "function"
)

type fixture struct {
	cfg         *config.Config
	server      *gotmux.Server
	service     *task.Service
	store       task.Store
	base        string
	worktreeDir string
	envFile     string
	promptFile  string
}

func TestStartCreatesAProjectTaskInItsOwnWorktreeAndWindow(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	place := projectPlace(t, f, repo)

	record, err := f.service.Start(t.Context(), place, inNewWorktree(envHarness))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	worktree := filepath.Join(f.worktreeDir, "shop", "main-1")
	want := task.EmptyRecord()
	want.ID, want.Kind, want.State, want.Project, want.Number = "t1", task.KindProject, task.StateActive, repo, 1
	want.Harness, want.Directory, want.Branch, want.Base, want.Title = envHarness, worktree, "wt/main/1", "main", "main-1"
	want.WindowName = "main-1"
	want.OwnsWorktree, want.OwnsBranch = true, true
	want.CreatedAt = record.CreatedAt
	want.Owner = task.Process{PID: os.Getpid(), Birth: record.Owner.Birth}

	if !reflect.DeepEqual(record, want) {
		t.Fatalf("Start() = %+v, want %+v", record, want)
	}

	if got := gitOutput(t, worktree, "rev-parse", "--abbrev-ref", "HEAD"); got != "wt/main/1" {
		t.Fatalf("worktree branch = %q, want wt/main/1", got)
	}

	window := taskWindow(t, f.server, place.Session, "t1")
	if window.Name != "main-1" || window.PaneCount != 2 {
		t.Fatalf("task window = %+v, want name main-1 with agent and editor panes", window)
	}

	panes := windowPanes(t, f.server, window.ID)
	if panes[0].Left >= panes[1].Left {
		t.Fatalf("agent pane is not beside the editor pane in a landscape window: %+v", panes)
	}

	for _, pane := range panes {
		if pane.CurrentPath != worktree {
			t.Fatalf("pane runs in %q, want the worktree", pane.CurrentPath)
		}
	}

	if got := waitForFile(t, f.envFile); got != strings.Join([]string{"t1", repo, worktree, "main"}, "|") {
		t.Fatalf("agent environment = %q", got)
	}

	ledger, err := f.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if stored, _ := ledger.Find("t1"); !reflect.DeepEqual(stored, record) {
		t.Fatalf("ledger record = %+v, want %+v", stored, record)
	}
}

func TestReopenRebuildsALostTaskWindow(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	place := projectPlace(t, f, repo)

	record, err := f.service.Start(t.Context(), place, inNewWorktree(envHarness))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	closeTaskWindow(t, f, place.Session, record.ID)

	if _, err := f.service.Reopen(t.Context(), place, record.ID); err != nil {
		t.Fatalf("Reopen() error = %v", err)
	}

	panes := rolePanes(t, f, place.Session, record.ID)
	if _, ok := panes["agent"]; !ok || len(panes) != 2 || panes["editor"].CurrentPath != record.Directory {
		t.Fatalf("reopened window panes = %+v, want the agent and the editor in the worktree", panes)
	}

	if _, err := f.service.Reopen(t.Context(), place, record.ID); err != nil || len(sessionWindows(t, f.server, place.Session)) != 2 {
		t.Fatalf("second Reopen() error = %v; want the window kept and no other opened", err)
	}
}

func TestReopenResumesASleepingTasksAgent(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	resumed := filepath.Join(f.base, "resumed")
	place := scratchPlace(t, f, dir)

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: envHarness})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	closeTaskWindow(t, f, place.Session, record.ID)

	if _, err := f.store.Update(t.Context(), func(ledger *task.Ledger) error {
		record.State = task.StateSleeping
		record.Resume = task.Resume{Harness: "pi", Session: "s1", Command: []string{"sh", "-c", "echo resumed > " + resumed + "; exec sleep 300"}}
		ledger.Put(record)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := f.service.Reopen(t.Context(), place, record.ID)
	if err != nil || reopened.State != task.StateActive {
		t.Fatalf("Reopen() = %s, %v; want the task active", reopened.State, err)
	}

	waitForFile(t, resumed)
}

func TestReopenRefusesATaskWhoseDirectoryIsGone(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	place := scratchPlace(t, f, dir)

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: envHarness})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	closeTaskWindow(t, f, place.Session, record.ID)

	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Reopen(t.Context(), place, record.ID); !errors.Is(err, task.ErrTaskDirectoryMissing) {
		t.Fatalf("Reopen() error = %v, want ErrTaskDirectoryMissing", err)
	}
}

func closeTaskWindow(t *testing.T, f fixture, session gotmux.SessionID, id string) {
	t.Helper()

	window, err := f.server.Window(t.Context(), taskWindow(t, f.server, session, id).ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := window.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStartSkipsTaskNumbersWhoseBranchOrDirectoryExists(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	runGit(t, repo, "branch", "wt/main/1")
	mkdir(t, filepath.Join(f.worktreeDir, "shop", "main-2"))
	place := projectPlace(t, f, repo)

	first, err := f.service.Start(t.Context(), place, inNewWorktree(sleepHarness))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	second, err := f.service.Start(t.Context(), place, inNewWorktree(sleepHarness))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if first.Number != 3 || second.Number != 4 {
		t.Fatalf("task numbers = %d, %d; want 3, 4", first.Number, second.Number)
	}
}

func TestStartPicksABranchThatDoesNotNestWithAnExistingBranch(t *testing.T) {
	titled := task.Request{Harness: sleepHarness, Title: "main", Location: task.Location{Where: task.WhereNew}}

	for _, test := range []struct {
		name     string
		existing string
		request  task.Request
		want     string
	}{
		{name: "untitled under a titled branch", existing: "wt/main", request: inNewWorktree(sleepHarness), want: "wt/main-2/1"},
		{name: "titled above an untitled branch", existing: "wt/main/1", request: titled, want: "wt/main-2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
			runGit(t, repo, "branch", test.existing)

			record, err := f.service.Start(t.Context(), projectPlace(t, f, repo), test.request)
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			if record.Branch != test.want {
				t.Fatalf("branch = %q, want %q", record.Branch, test.want)
			}
		})
	}
}

func TestStartRejectsAHarnessThatIsNotOffered(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	place := projectPlace(t, f, repo)

	_, err := f.service.Start(t.Context(), place, inNewWorktree("missing"))
	if !errors.Is(err, task.ErrUnknownHarness) {
		t.Fatalf("Start() error = %v, want ErrUnknownHarness", err)
	}

	assertNoTasks(t, f, place)
}

func TestStartRecordsAFailedTaskWhenTheWorktreeCannotBeCreated(t *testing.T) {
	f := newFixture(t)
	repo := mkdir(t, filepath.Join(f.base, "code", "empty"))
	runGit(t, repo, "init", "-q", "-b", "main")
	place := projectPlace(t, f, repo)

	record, err := f.service.Start(t.Context(), place, inNewWorktree(envHarness))
	if err == nil {
		t.Fatal("Start() succeeded in a repository without commits")
	}

	ledger, loadErr := f.store.Load(t.Context())
	if loadErr != nil {
		t.Fatal(loadErr)
	}

	if stored, _ := ledger.Find(record.ID); stored.State != task.StateFailed || stored.Branch != "wt/main/1" {
		t.Fatalf("ledger record = %+v, want a failed record naming its branch", stored)
	}

	if count := len(sessionWindows(t, f.server, place.Session)); count != 1 {
		t.Fatalf("session has %d windows, want only main", count)
	}
}

func TestStartOutsideAProjectCreatesALooseTaskNextToTheCurrentWindow(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	session := newSession(t, f.server, "scratch", dir)

	for range 2 {
		if _, err := session.NewWindow(t.Context(), plainWindow(dir)); err != nil {
			t.Fatal(err)
		}
	}

	first := sessionWindows(t, f.server, session.ID())[0]
	if err := first.Window().Select(t.Context()); err != nil {
		t.Fatal(err)
	}

	place := locate(t, f, first.Window())

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: envHarness})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if record.Kind != task.KindLoose || record.Directory != dir || record.Branch != "" || record.Project != "" {
		t.Fatalf("Start() = %+v, want a loose task in the pane directory", record)
	}

	windows := sessionWindows(t, f.server, session.ID())

	second, err := windows[1].Window().Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if second.ID != taskWindow(t, f.server, session.ID(), record.ID).ID {
		t.Fatal("loose task window is not right after the current window")
	}

	if _, err := os.Stat(f.worktreeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("loose task created a worktree")
	}
}

func TestStartOutsideAProjectNamesTheWindowAfterTheDirectory(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	place := scratchPlace(t, f, dir)

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: envHarness})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	window := taskWindow(t, f.server, place.Session, record.ID)
	if record.Title != "scratch" || window.Name != "scratch" {
		t.Fatalf("task title = %q, window name = %q; want scratch", record.Title, window.Name)
	}
}

func TestOfferListsEveryCheckoutAndBranchAndMarksTheOnesTasksUse(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	hand := filepath.Join(f.base, "hand")
	gone := filepath.Join(f.base, "gone")

	runGit(t, repo, "worktree", "add", "-q", "-b", "hand", hand)
	runGit(t, repo, "worktree", "add", "-q", "-b", "gone", gone)
	runGit(t, repo, "branch", "feature")
	place := projectPlace(t, f, repo)

	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	started, err := f.service.Start(t.Context(), place, inNewWorktree(sleepHarness))
	if err != nil {
		t.Fatal(err)
	}

	offer, err := f.service.Offer(t.Context(), place, false)
	if err != nil {
		t.Fatal(err)
	}

	want := []task.Location{
		{Where: task.WhereNew, Branch: "main"},
		{Where: task.WhereMain, Branch: "main", Directory: repo},
		{Where: task.WhereWorktree, Branch: "hand", Directory: hand},
		{Where: task.WhereWorktree, Branch: "wt/main/1", Directory: started.Directory, Task: started.ID, TaskTitle: "main-1"},
		{Where: task.WhereBranch, Branch: "feature"},
		{Where: task.WhereNew, Branch: "feature"},
		{Where: task.WhereNew, Branch: "gone"},
		{Where: task.WhereNew, Branch: "hand"},
		{Where: task.WhereNew, Branch: "wt/main/1"},
	}
	if !slices.Equal(offer.Locations, want) {
		t.Fatalf("Offer() locations = %+v, want %+v: a missing worktree and checked-out branches left out, the task's worktree marked", offer.Locations, want)
	}

	if offer.Current != 1 || offer.NextNumber != 2 {
		t.Fatalf("Offer() current = %d, next number = %d; want the main checkout the pane is in and number 2 after task 1", offer.Current, offer.NextNumber)
	}

	if _, err := f.service.Start(t.Context(), place, task.Request{
		Harness: sleepHarness, Location: task.Location{Where: task.WhereWorktree, Directory: started.Directory},
	}); !errors.Is(err, task.ErrLocationUnavailable) {
		t.Fatalf("Start() in the task's worktree error = %v, want ErrLocationUnavailable", err)
	}
}

func TestOfferDescribesEachBranchNewestFirst(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))

	runGit(t, repo, "switch", "-q", "-c", "older")
	commitAt(t, repo, "", "2020-01-02T03:04:05Z")
	runGit(t, repo, "switch", "-q", "main")
	runGit(t, repo, "switch", "-q", "-c", "newer")
	commitAt(t, repo, "first step", "2026-01-01T00:00:00Z")
	commitAt(t, repo, "second step", "2026-01-02T00:00:00Z")
	runGit(t, repo, "switch", "-q", "main")
	commitAt(t, repo, "main moves on", "2025-06-01T00:00:00Z")
	addRemoteBranch(t, f.base, repo, "shared")

	offer, err := f.service.Offer(t.Context(), projectPlace(t, f, repo), true)
	if err != nil {
		t.Fatal(err)
	}

	newer := task.Ref{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).Unix(), Subject: "second step", Ahead: 2, Behind: 1}
	if got := offer.Refs["refs/heads/newer"]; got != newer {
		t.Errorf("newer ref = %+v, want its latest commit, 2 ahead of main and 1 behind: %+v", got, newer)
	}

	older := task.Ref{Time: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC).Unix(), Subject: "", Ahead: 1, Behind: 1}
	if got, ok := offer.Refs["refs/heads/older"]; !ok || got != older {
		t.Errorf("older ref without a subject = %+v, %t; want %+v", got, ok, older)
	}

	if shared, ok := offer.Refs["refs/remotes/origin/shared"]; !ok || shared.Subject != "remote work" || shared.Ahead != 1 {
		t.Errorf("remote ref = %+v, %t; want origin/shared 1 ahead", shared, ok)
	}

	if order := localBranches(offer.Locations); !slices.Equal(order, []string{"newer", "older"}) {
		t.Errorf("branches = %v, want the most recently committed first", order)
	}
}

func localBranches(locations []task.Location) []string {
	var names []string

	for _, location := range locations {
		if location.Where == task.WhereBranch && location.Remote == "" {
			names = append(names, location.Branch)
		}
	}

	return names
}

func TestChangesCountsTheLinesEachCheckoutsChangesAddAndDelete(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	notes := filepath.Join(repo, "notes.txt")

	writeFile(t, notes, "one\ntwo\nthree\n")
	runGit(t, repo, "add", "notes.txt")
	runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "notes")

	clean := filepath.Join(f.base, "clean")
	runGit(t, repo, "worktree", "add", "-q", "-b", "clean", clean)
	writeFile(t, notes, "one\n2\nthree\nfour\nfive\n")
	writeFile(t, filepath.Join(repo, "untracked.txt"), "new\n")

	missing := filepath.Join(f.base, "missing")

	changes := task.Changes(t.Context(), []string{repo, clean, missing})
	want := map[string]git.Diff{repo: {Added: 3, Deleted: 1}, clean: {Added: 0, Deleted: 0}}

	if !maps.Equal(changes, want) {
		t.Fatalf("Changes() = %v, want %v: tracked lines only, nothing for a missing checkout", changes, want)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOfferStartsANewWorktreeFromTheBranchThePaneIsOn(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	hand := filepath.Join(f.base, "hand")
	runGit(t, repo, "worktree", "add", "-q", "-b", "hand", hand)
	place := projectPlace(t, f, repo)
	place.Dir = mkdir(t, filepath.Join(hand, "sub"))

	offer, err := f.service.Offer(t.Context(), place, false)
	if err != nil {
		t.Fatal(err)
	}

	if len(offer.Locations) < 2 || offer.Locations[1] != (task.Location{Where: task.WhereNew, Branch: "hand"}) {
		t.Fatalf("Offer() locations = %+v, want a new worktree from hand right after the default branch", offer.Locations)
	}

	if current := offer.Current; current < 0 || offer.Locations[current].Directory != hand {
		t.Fatalf("Offer() current = %d in %+v, want the hand worktree the pane is in", current, offer.Locations)
	}
}

func TestOfferInAMainCheckoutInsideALinkedWorktreeStartsFromTheMainCheckout(t *testing.T) {
	f := newFixture(t)
	moved := initRepository(t, filepath.Join(f.base, "code", "shop"))
	outer := filepath.Join(f.base, "outer")
	runGit(t, moved, "worktree", "add", "-q", "-b", "outer", outer)

	repo := filepath.Join(outer, "shop")
	if err := os.Rename(moved, repo); err != nil {
		t.Fatal(err)
	}

	runGit(t, repo, "worktree", "repair", outer)

	offer, err := f.service.Offer(t.Context(), projectPlace(t, f, repo), false)
	if err != nil {
		t.Fatal(err)
	}

	if len(offer.Locations) < 2 || offer.Locations[1].Where != task.WhereMain {
		t.Fatalf("Offer() locations = %+v, want only a new worktree from main before the checkouts", offer.Locations)
	}

	if current := offer.Current; current < 0 || offer.Locations[current].Directory != repo {
		t.Fatalf("Offer() current = %d in %+v, want the main checkout the pane is in", current, offer.Locations)
	}
}

func TestOutsideARepositoryOfferHasNoProject(t *testing.T) {
	f := newFixture(t)
	initRepository(t, filepath.Join(f.base, "code", "shop"))
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	session := newSession(t, f.server, "scratch", dir)

	offer, err := f.service.Offer(t.Context(), locate(t, f, sessionWindows(t, f.server, session.ID())[0].Window()), false)
	if err != nil {
		t.Fatal(err)
	}

	if offer.Project != "" || len(offer.Locations) != 0 {
		t.Fatalf("Offer() = %+v, want no project and no locations", offer)
	}
}

func TestInARepositoryOutsideAProjectSessionOfferIsTheRepositorys(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	session := newSession(t, f.server, "work", repo)

	offer, err := f.service.Offer(t.Context(), locate(t, f, sessionWindows(t, f.server, session.ID())[0].Window()), false)
	if err != nil {
		t.Fatal(err)
	}

	if offer.Project != repo || len(offer.Locations) == 0 || offer.Locations[0] != (task.Location{Where: task.WhereNew, Branch: "main"}) {
		t.Fatalf("Offer() = %+v, want the shop repository with a new worktree from main first", offer)
	}
}

func TestInARepositoryOutsideAProjectSessionATaskGetsAWorktreeNextToTheCurrentWindow(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	session := newSession(t, f.server, "work", repo)

	for range 2 {
		if _, err := session.NewWindow(t.Context(), plainWindow(repo)); err != nil {
			t.Fatal(err)
		}
	}

	first := sessionWindows(t, f.server, session.ID())[0]
	if err := first.Window().Select(t.Context()); err != nil {
		t.Fatal(err)
	}

	record, err := f.service.Start(t.Context(), locate(t, f, first.Window()), inNewWorktree(envHarness))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if record.Kind != task.KindProject || record.Project != repo || record.Branch != "wt/main/1" || !record.OwnsWorktree {
		t.Fatalf("Start() = %+v, want a project task on a new worktree", record)
	}

	if _, err := os.Stat(record.Directory); err != nil {
		t.Fatalf("task worktree: %v", err)
	}

	second, err := sessionWindows(t, f.server, session.ID())[1].Window().Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if second.ID != taskWindow(t, f.server, session.ID(), record.ID).ID {
		t.Fatal("task window is not right after the current window")
	}
}

func TestStartWithATitleNamesTheTaskAndBranchAndPromptsTheAgent(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	place := projectPlace(t, f, repo)
	request := task.Request{Harness: promptedHarness, Title: "Fix login: redirect!", Location: task.Location{Where: task.WhereNew}}

	record, err := f.service.Start(t.Context(), place, request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if record.Branch != "wt/fix-login-redirect" || record.Title != request.Title {
		t.Fatalf("Start() = %+v, want the title and a branch named after it", record)
	}

	if name := taskWindow(t, f.server, place.Session, record.ID).Name; name != request.Title {
		t.Fatalf("window name = %q, want the title", name)
	}

	if got := waitForFile(t, f.promptFile); got != "1:prompt for "+request.Title {
		t.Fatalf("agent prompt = %q, want the filled prompt argument", got)
	}

	again, err := f.service.Start(t.Context(), place, request)
	if err != nil {
		t.Fatal(err)
	}

	if again.Branch != "wt/fix-login-redirect-2" {
		t.Fatalf("second branch = %q, want a suffix after the taken name", again.Branch)
	}
}

func TestStartWithABackslashInTheTitleKeepsTheTitleAndNamesTheWindowWithoutIt(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	place := projectPlace(t, f, repo)
	request := task.Request{Harness: promptedHarness, Title: `Fix C:\Users path`, Location: task.Location{Where: task.WhereNew}}

	record, err := f.service.Start(t.Context(), place, request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if record.Title != request.Title {
		t.Fatalf("title = %q, want %q", record.Title, request.Title)
	}

	if name := taskWindow(t, f.server, place.Session, record.ID).Name; name != "Fix C:/Users path" {
		t.Fatalf("window name = %q, want the title with the backslash replaced", name)
	}
}

func TestStartRunsTheHarnessInTheUserShellWithItsFunctions(t *testing.T) {
	for _, shell := range []struct {
		name, rcFile, rc string
	}{
		{name: "bash", rcFile: ".bashrc", rc: "shellagent() { printf '%%s|' --yolo \"$@\" > '%s'; exec sleep 300; }\n"},
		{name: "fish", rcFile: ".config/fish/config.fish", rc: "function shellagent; printf '%%s|' --yolo $argv > '%s'; exec sleep 300; end\n"},
	} {
		t.Run(shell.name, func(t *testing.T) {
			f := newFixture(t)
			repo := initRepository(t, filepath.Join(f.base, "code", "shop"))

			path, err := exec.LookPath(shell.name)
			if err != nil {
				t.Fatal(err)
			}

			home, err := f.server.Environment().Get(t.Context(), "HOME")
			if err != nil {
				t.Fatal(err)
			}

			homeDir, _ := home.Value.Get()
			rcPath := filepath.Join(homeDir, shell.rcFile)

			if err := os.MkdirAll(filepath.Dir(rcPath), 0o700); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(rcPath, []byte(fmt.Sprintf(shell.rc, f.promptFile)), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := f.server.GlobalSessionOptions().SetDefaultShell(t.Context(), path); err != nil {
				t.Fatal(err)
			}

			title := `it's "quoted" $HOME`
			if _, err := f.service.Start(t.Context(), projectPlace(t, f, repo), task.Request{
				Harness: functionHarness, Title: title, Location: task.Location{Where: task.WhereNew},
			}); err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			if got := waitForFile(t, f.promptFile); got != `--yolo|a\b \'|`+title+"|" {
				t.Fatalf("shell function ran with %q, want its own option and the title unchanged", got)
			}
		})
	}
}

func TestStartWithoutATitlePassesNoPrompt(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))

	if _, err := f.service.Start(t.Context(), projectPlace(t, f, repo), inNewWorktree(promptedHarness)); err != nil {
		t.Fatal(err)
	}

	if got := waitForFile(t, f.promptFile); got != "0:" {
		t.Fatalf("agent arguments = %q, want none", got)
	}
}

func TestStartANewWorktreeFromAnotherBranch(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	runGit(t, repo, "switch", "-q", "-c", "feature")
	runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "feature work")
	runGit(t, repo, "switch", "-q", "main")
	place := projectPlace(t, f, repo)

	record, err := f.service.Start(t.Context(), place, task.Request{
		Harness: sleepHarness, Location: task.Location{Where: task.WhereNew, Branch: "feature"},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if record.Base != "feature" || record.Branch != "wt/feature/1" || !record.OwnsBranch {
		t.Fatalf("Start() = %+v, want an owned branch based on feature", record)
	}

	if got := gitOutput(t, record.Directory, "log", "-1", "--format=%s"); got != "feature work" {
		t.Fatalf("new branch starts at %q, want the feature branch's commit", got)
	}
}

func TestStartFromARemoteBranchCreatesALocalBranchTrackingIt(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	addRemoteBranch(t, f.base, repo, "review")
	place := projectPlace(t, f, repo)

	offer, err := f.service.Offer(t.Context(), place, false)
	if err != nil {
		t.Fatal(err)
	}

	remote := task.Location{Where: task.WhereBranch, Branch: "review", Remote: "origin"}
	if !slices.Contains(offer.Locations, remote) || slices.Contains(offer.Locations, task.Location{Where: task.WhereBranch, Branch: "main", Remote: "origin"}) {
		t.Fatalf("Offer() locations = %+v, want origin/review and not origin/main, which main shares", offer.Locations)
	}

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: sleepHarness, Location: remote})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if record.Branch != "review" || record.Upstream != "origin/review" || !record.OwnsBranch || !record.OwnsWorktree || record.Base != "main" {
		t.Fatalf("Start() = %+v, want an owned local review branch tracking origin/review", record)
	}

	if got := gitOutput(t, record.Directory, "rev-parse", "--abbrev-ref", "@{upstream}"); got != "origin/review" {
		t.Fatalf("upstream = %q, want origin/review", got)
	}
}

func TestStartInAnExistingCheckoutLeavesGitAloneAndTakesTheCheckout(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	hand := filepath.Join(f.base, "hand")
	runGit(t, repo, "worktree", "add", "-q", "-b", "hand", hand)
	place := projectPlace(t, f, repo)

	for _, location := range []task.Location{{Where: task.WhereWorktree, Directory: hand}, {Where: task.WhereMain}} {
		record, err := f.service.Start(t.Context(), place, task.Request{Harness: sleepHarness, Location: location})
		if err != nil {
			t.Fatalf("Start(%s) error = %v", location.Where, err)
		}

		if record.OwnsWorktree || record.OwnsBranch || record.Branch == "" || record.Base != "main" {
			t.Fatalf("Start(%s) = %+v, want a task that owns nothing on the checkout's branch", location.Where, record)
		}

		for _, pane := range windowPanes(t, f.server, taskWindow(t, f.server, place.Session, record.ID).ID) {
			if pane.CurrentPath != record.Directory {
				t.Fatalf("pane runs in %q, want %q", pane.CurrentPath, record.Directory)
			}
		}

		if _, err := f.service.Start(t.Context(), place, task.Request{Harness: sleepHarness, Location: location}); !errors.Is(err, task.ErrLocationUnavailable) {
			t.Fatalf("second Start(%s) error = %v, want the taken checkout refused", location.Where, err)
		}
	}

	if branches := gitOutput(t, repo, "branch", "--list", "wt/*"); branches != "" {
		t.Fatalf("task branches %q were created", branches)
	}

	if _, err := os.Stat(f.worktreeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a worktree was created")
	}
}

func TestStartFromABranchChecksItOutInANewWorktree(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	runGit(t, repo, "branch", "feature")
	place := projectPlace(t, f, repo)

	record, err := f.service.Start(t.Context(), place, task.Request{
		Harness: sleepHarness, Location: task.Location{Where: task.WhereBranch, Branch: "feature"},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	worktree := filepath.Join(f.worktreeDir, "shop", "main-1")
	if record.Directory != worktree || record.Branch != "feature" || !record.OwnsWorktree || record.OwnsBranch {
		t.Fatalf("Start() = %+v, want an owned worktree on the existing branch", record)
	}

	if got := gitOutput(t, worktree, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature" {
		t.Fatalf("worktree branch = %q, want feature", got)
	}

	if branches := gitOutput(t, repo, "branch", "--list", "wt/*"); branches != "" {
		t.Fatalf("task branches %q were created", branches)
	}
}

func TestStartRefusesALocationTheProjectDoesNotOffer(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	place := projectPlace(t, f, repo)

	for _, location := range []task.Location{
		{Where: task.WhereWorktree, Directory: mkdir(t, filepath.Join(f.base, "elsewhere"))},
		{Where: task.WhereBranch, Branch: "missing"},
		{Where: task.WhereBranch, Branch: "main"},
		{Where: ""},
	} {
		if _, err := f.service.Start(t.Context(), place, task.Request{Harness: sleepHarness, Location: location}); !errors.Is(err, task.ErrLocationUnavailable) {
			t.Fatalf("Start(%+v) error = %v, want ErrLocationUnavailable", location, err)
		}
	}

	assertNoTasks(t, f, place)
}

func TestOfferKeepsTheHarnessOrder(t *testing.T) {
	f := newFixture(t)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))

	offer, err := f.service.Offer(t.Context(), projectPlace(t, f, repo), false)
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(offer.Harnesses))
	for _, harness := range offer.Harnesses {
		names = append(names, harness.Name)
	}

	if !slices.Equal(names, []string{envHarness, sleepHarness, promptedHarness, functionHarness}) {
		t.Fatalf("Offer() harnesses = %v, want the configured order", names)
	}
}

func TestOpenTabOpensAPlainWindowInThePaneDirectory(t *testing.T) {
	f := newFixture(t)
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	session := newSession(t, f.server, "scratch", dir)
	place := locate(t, f, sessionWindows(t, f.server, session.ID())[0].Window())

	if err := f.service.OpenTab(t.Context(), place); err != nil {
		t.Fatalf("OpenTab() error = %v", err)
	}

	windows := sessionWindows(t, f.server, session.ID())
	if len(windows) != 2 {
		t.Fatalf("session has %d windows, want 2", len(windows))
	}

	panes := windowPanes(t, f.server, windows[1].Window().ID())
	if len(panes) != 1 || panes[0].CurrentPath != dir {
		t.Fatalf("tab panes = %+v, want one pane in %s", panes, dir)
	}
}

func inNewWorktree(harness string) task.Request {
	return task.Request{Harness: harness, Location: task.Location{Where: task.WhereNew}}
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	base := realTempDir(t)
	server := tmuxtest.NewServer(t)
	store := task.NewStore(filepath.Join(base, "state", "tasks.json"))
	envFile := filepath.Join(base, "agent-env")
	promptFile := filepath.Join(base, "agent-prompt")

	worktreeDir := filepath.Join(base, "worktrees")

	catalog, err := project.NewCatalog(config.ProjectsConfig{Roots: []string{filepath.Join(base, "code")}, Extra: nil, Overrides: nil})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultTasksConfig()
	cfg.Panes = []config.TaskPaneConfig{{Name: "editor", Command: []string{"sleep", "300"}, Dir: "", Restart: false}}
	cfg.Harness = []config.HarnessConfig{
		{Name: envHarness, Command: []string{"sh", "-c", `printf '%s|%s|%s|%s' "$SESH_TASK" "$SESH_PROJECT" "$SESH_WORKTREE" "$SESH_BASE" > "$0"; exec sleep 300`, envFile}},
		{Name: sleepHarness, Command: []string{"sleep", "300"}},
		{Name: promptedHarness, Command: []string{"sh", "-c", `printf '%s:%s' "$#" "$*" > "$0"; exec sleep 300`, promptFile}, PromptArgs: []string{"prompt for {title}"}},
		{Name: functionHarness, Command: []string{"shellagent", `a\b \'`}, PromptArgs: []string{"{title}"}},
	}

	full := config.DefaultConfig()
	full.Tasks = cfg

	service := task.NewService(task.Options{
		Server: server, Store: store, Catalog: catalog, Config: cfg, Windows: full.TaskWindowFor, WorktreeDir: worktreeDir,
		LookPath: exec.LookPath, Now: time.Now,
	})

	return fixture{cfg: full, server: server, service: service, store: store, base: base, worktreeDir: worktreeDir, envFile: envFile, promptFile: promptFile}
}

func projectPlace(t *testing.T, f fixture, root string) task.Place {
	t.Helper()

	session, err := project.CreateSession(t.Context(), f.server, project.Project{Root: root, Name: filepath.Base(root), Kind: project.KindGit})
	if err != nil {
		t.Fatal(err)
	}

	place := locate(t, f, sessionWindows(t, f.server, session.ID())[0].Window())
	if place.Project != root {
		t.Fatalf("Locate() project = %q, want %q", place.Project, root)
	}

	return place
}

func locate(t *testing.T, f fixture, window gotmux.Window) task.Place {
	t.Helper()

	pane, err := window.ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	place, err := f.service.Locate(t.Context(), pane.ID())
	if err != nil {
		t.Fatalf("Locate() error = %v", err)
	}

	return place
}

func assertNoTasks(t *testing.T, f fixture, place task.Place) {
	t.Helper()

	ledger, err := f.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(ledger.Tasks) != 0 {
		t.Fatalf("ledger has tasks %+v, want none", ledger.Tasks)
	}

	if count := len(sessionWindows(t, f.server, place.Session)); count != 1 {
		t.Fatalf("session has %d windows, want only main", count)
	}
}

func newSession(t *testing.T, server *gotmux.Server, name, dir string) gotmux.Session {
	t.Helper()

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: name, Dir: dir, Window: "", Program: gotmux.Program{}, Env: nil, TmuxEnv: nil,
		Size: gotmux.Size{Width: 0, Height: 0}, Start: gotmux.StartPolicyExistingOnly, Group: "",
	})
	if err != nil {
		t.Fatal(err)
	}

	return session
}

func plainWindow(dir string) gotmux.NewWindowOptions {
	return gotmux.NewWindowOptions{
		Name: "", Dir: dir, Program: gotmux.Program{}, Env: nil, TmuxEnv: nil,
		Index: nil, Before: false, After: false, Select: false,
	}
}

func sessionWindows(t *testing.T, server *gotmux.Server, id gotmux.SessionID) []gotmux.WindowLink {
	t.Helper()

	session, err := server.Session(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	windows, err := session.Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return windows
}

func taskWindow(t *testing.T, server *gotmux.Server, id gotmux.SessionID, taskID string) gotmux.WindowInfo {
	t.Helper()

	for _, link := range sessionWindows(t, server, id) {
		info, err := link.Window().InfoWith(t.Context(), gotmux.QueryOptions{Filter: "", ExtraFields: []string{task.OptionTask}})
		if err != nil {
			t.Fatal(err)
		}

		if value, _ := info.Raw(task.OptionTask); string(value) == taskID {
			return info
		}
	}

	t.Fatalf("no window is tagged with task %s", taskID)

	return gotmux.WindowInfo{}
}

func windowPanes(t *testing.T, server *gotmux.Server, id gotmux.WindowID) []gotmux.PaneInfo {
	t.Helper()

	window, err := server.Window(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	panes, err := window.Panes(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return panes
}

func waitForFile(t *testing.T, path string) string {
	t.Helper()

	deadline := time.Now().Add(envWaitLimit)

	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return string(data)
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("%s was not written", path)

	return ""
}

func realTempDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return dir
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	return dir
}

func initRepository(t *testing.T, dir string) string {
	t.Helper()

	mkdir(t, dir)
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")

	return dir
}

func addRemoteBranch(t *testing.T, base, repo, branch string) {
	t.Helper()

	upstream := filepath.Join(base, "upstream")
	runGit(t, base, "clone", "-q", repo, upstream)
	runGit(t, upstream, "switch", "-q", "-c", branch)
	runGit(t, upstream, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "remote work")
	runGit(t, upstream, "switch", "-q", "main")
	runGit(t, repo, "remote", "add", "origin", upstream)
	runGit(t, repo, "fetch", "-q", "origin")
}

func commitAt(t *testing.T, dir, message, date string) {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "--allow-empty-message", "-m", message)
	command.Dir = dir

	command.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+date, "GIT_AUTHOR_DATE="+date)

	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	gitOutput(t, dir, args...)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", args...)
	command.Dir = dir

	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}

	return strings.TrimSpace(string(output))
}
