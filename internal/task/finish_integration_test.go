//go:build integration

package task_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/project"
	"github.com/zigai/sesh/internal/task"
)

// pullRequests reports a fixed pull request for every branch, like gh for a repository that
// has one.
type pullRequests struct {
	pr    task.PullRequest
	head  string
	found bool
	err   error
}

type finishFixture struct {
	fixture

	prs    *pullRequests
	closed []string
	record task.Record
	repo   string
}

func TestFinishRefusesToLoseUnmergedCommitsAndChanges(t *testing.T) {
	f := newFinishFixture(t)
	commit(t, f.record.Directory)

	if err := os.WriteFile(filepath.Join(f.record.Directory, "notes"), []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := f.service.Finish(t.Context(), f.record.ID, task.FinishOptions{Discard: false, SkipRun: false})

	var loss *task.LossError
	if !errors.As(err, &loss) || loss.UnmergedCommits != 1 || loss.ChangedFiles != 1 || loss.ClosedUnmerged {
		t.Fatalf("Finish() error = %v, want 1 unmerged commit and 1 changed file", err)
	}

	assertTaskKept(t, f)
}

func TestFinishRefusesATaskWhosePullRequestWasClosedUnmerged(t *testing.T) {
	f := newFinishFixture(t)
	f.prs.pr, f.prs.found = task.PullRequest{Number: 7, State: task.PRClosed, URL: ""}, true

	_, err := f.service.Finish(t.Context(), f.record.ID, task.FinishOptions{Discard: false, SkipRun: false})

	var loss *task.LossError
	if !errors.As(err, &loss) || !loss.ClosedUnmerged {
		t.Fatalf("Finish() error = %v, want a closed pull request reported", err)
	}

	assertTaskKept(t, f)
}

func TestFinishRemovesAMergedTaskWithoutDiscard(t *testing.T) {
	for _, squash := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "squash"}[squash], func(t *testing.T) {
			f := newFinishFixture(t)
			mergeTask(t, f, squash)

			if _, _, err := f.service.RefreshPR(t.Context(), f.record.ID); err != nil {
				t.Fatal(err)
			}

			if _, err := f.service.Finish(t.Context(), f.record.ID, task.FinishOptions{Discard: false, SkipRun: false}); err != nil {
				t.Fatalf("Finish() error = %v", err)
			}

			assertTaskRemoved(t, f)
		})
	}
}

func TestFinishRefusesCommitsAddedAfterPullRequestMerge(t *testing.T) {
	for _, squash := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "squash"}[squash], func(t *testing.T) {
			f := newFinishFixture(t)
			mergeTask(t, f, squash)
			commit(t, f.record.Directory)
			tip := gitOutput(t, f.record.Directory, "rev-parse", "HEAD")

			_, err := f.service.Finish(t.Context(), f.record.ID, task.FinishOptions{Discard: false, SkipRun: false})

			var loss *task.LossError
			if !errors.As(err, &loss) || loss.UnmergedCommits != 1 || loss.ChangedFiles != 0 || loss.ClosedUnmerged {
				t.Fatalf("Finish() error = %v, want the post-merge commit reported", err)
			}

			assertTaskKept(t, f)

			if got := gitOutput(t, f.repo, "rev-parse", f.record.Branch); got != tip {
				t.Fatalf("task branch tip = %q, want the post-merge commit %q kept", got, tip)
			}
		})
	}
}

func TestFinishRefusesCachedMergedStateWithoutFreshHeadEvidence(t *testing.T) {
	tests := []struct {
		name  string
		found bool
		err   error
	}{
		{name: "missing head", found: true, err: nil},
		{name: "not found", found: false, err: nil},
		{name: "lookup failed", found: true, err: task.ErrGitHubFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFinishFixture(t)
			mergeTask(t, f, true)

			if _, _, err := f.service.RefreshPR(t.Context(), f.record.ID); err != nil {
				t.Fatal(err)
			}

			f.prs.head, f.prs.found, f.prs.err = "", tt.found, tt.err

			_, err := f.service.Finish(t.Context(), f.record.ID, task.FinishOptions{Discard: false, SkipRun: false})

			var loss *task.LossError
			if !errors.As(err, &loss) || loss.UnmergedCommits != 1 || loss.ChangedFiles != 0 || loss.ClosedUnmerged {
				t.Fatalf("Finish() error = %v, want cached merged state insufficient to discard commits", err)
			}

			assertTaskKept(t, f)
		})
	}
}

func mergeTask(t *testing.T, f *finishFixture, squash bool) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(f.record.Directory, "merged"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}

	runGit(t, f.record.Directory, "add", "merged")
	commit(t, f.record.Directory)
	f.prs.head = gitOutput(t, f.record.Directory, "rev-parse", "HEAD")

	if squash {
		runGit(t, f.repo, "merge", "-q", "--squash", f.record.Branch)
		runGit(t, f.repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "squash task")
	} else {
		runGit(t, f.repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "merge", "-q", "--no-ff", "-m", "merge task", f.record.Branch)
	}

	f.prs.pr, f.prs.found = task.PullRequest{Number: 7, State: task.PRMerged, URL: ""}, true
}

func TestFinishRemovesATaskWithNothingToLose(t *testing.T) {
	f := newFinishFixture(t)

	if _, err := f.service.Finish(t.Context(), f.record.ID, task.FinishOptions{Discard: false, SkipRun: false}); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	assertTaskRemoved(t, f)
}

func TestFinishWithDiscardRemovesUnmergedWork(t *testing.T) {
	f := newFinishFixture(t)
	commit(t, f.record.Directory)

	if _, err := f.service.Finish(t.Context(), f.record.ID, task.FinishOptions{Discard: true, SkipRun: false}); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	assertTaskRemoved(t, f)
}

func TestFinishCompletesARemovalThatStoppedPartWay(t *testing.T) {
	f := newFinishFixture(t)
	commit(t, f.record.Directory)
	runGit(t, f.repo, "worktree", "remove", "--force", f.record.Directory)

	if _, err := f.store.Update(t.Context(), func(ledger *task.Ledger) error {
		record, _ := ledger.Find(f.record.ID)
		record.State = task.StateFinishing
		ledger.Put(record)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Finish(t.Context(), f.record.ID, task.FinishOptions{Discard: false, SkipRun: false}); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	assertTaskRemoved(t, f)
}

func TestFinishLeavesACheckoutSeshDidNotCreateWithItsWork(t *testing.T) {
	f := newFinishFixture(t)
	hand := filepath.Join(f.base, "hand")
	runGit(t, f.repo, "worktree", "add", "-q", "-b", "hand", hand)
	commit(t, hand)

	if err := os.WriteFile(filepath.Join(hand, "notes"), []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}

	record, err := f.service.Start(t.Context(), projectSessionPlace(t, f), task.Request{
		Harness: envHarness, Location: task.Location{Where: task.WhereWorktree, Directory: hand},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Finish(t.Context(), record.ID, task.FinishOptions{Discard: false, SkipRun: false}); err != nil {
		t.Fatalf("Finish() error = %v, want a checkout sesh does not own finished without loss checks", err)
	}

	if _, err := os.Stat(filepath.Join(hand, "notes")); err != nil {
		t.Fatalf("uncommitted work in the checkout is gone: %v", err)
	}

	if branches := gitOutput(t, f.repo, "branch", "--list", "hand"); branches == "" {
		t.Fatal("the checkout's branch was deleted")
	}
}

func TestFinishOfABranchWorktreeRemovesTheWorktreeAndKeepsTheBranch(t *testing.T) {
	f := newFinishFixture(t)
	runGit(t, f.repo, "branch", "feature")
	runGit(t, f.repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "main moves on")

	record, err := f.service.Start(t.Context(), projectSessionPlace(t, f), task.Request{
		Harness: envHarness, Location: task.Location{Where: task.WhereBranch, Branch: "feature"},
	})
	if err != nil {
		t.Fatal(err)
	}

	commit(t, record.Directory)

	if err := os.WriteFile(filepath.Join(record.Directory, "notes"), []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = f.service.Finish(t.Context(), record.ID, task.FinishOptions{Discard: false, SkipRun: false})

	var loss *task.LossError
	if !errors.As(err, &loss) || loss.ChangedFiles != 1 || loss.UnmergedCommits != 0 {
		t.Fatalf("Finish() error = %v, want only the worktree's uncommitted change reported", err)
	}

	if err := os.Remove(filepath.Join(record.Directory, "notes")); err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Finish(t.Context(), record.ID, task.FinishOptions{Discard: false, SkipRun: false}); err != nil {
		t.Fatalf("Finish() error = %v, want the branch's commits kept, not counted as loss", err)
	}

	if _, err := os.Stat(record.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still exists: %v", err)
	}

	if branches := gitOutput(t, f.repo, "branch", "--list", "feature"); branches == "" {
		t.Fatal("the existing branch was deleted")
	}
}

func TestFinishOfARemoteBranchTaskKeepsUnpushedWorkAndTheRemoteBranch(t *testing.T) {
	f := newFinishFixture(t)
	addRemoteBranch(t, f.base, f.repo, "review")

	record, err := f.service.Start(t.Context(), projectSessionPlace(t, f), task.Request{
		Harness: envHarness, Location: task.Location{Where: task.WhereBranch, Branch: "review", Remote: "origin"},
	})
	if err != nil {
		t.Fatal(err)
	}

	commit(t, record.Directory)

	_, err = f.service.Finish(t.Context(), record.ID, task.FinishOptions{Discard: false, SkipRun: false})

	var loss *task.LossError
	if !errors.As(err, &loss) || loss.UnmergedCommits != 1 {
		t.Fatalf("Finish() error = %v, want the unpushed commit reported", err)
	}

	runGit(t, record.Directory, "push", "-q", "origin", "review")

	if _, err := f.service.Finish(t.Context(), record.ID, task.FinishOptions{Discard: false, SkipRun: false}); err != nil {
		t.Fatalf("Finish() error = %v, want a pushed branch removed without loss", err)
	}

	if branches := gitOutput(t, f.repo, "branch", "--list", "review"); branches != "" {
		t.Fatal("the local branch sesh created was kept")
	}

	if remote := gitOutput(t, f.repo, "branch", "-r", "--list", "origin/review"); remote == "" {
		t.Fatal("the remote branch was removed")
	}
}

func TestRefreshPRRecordsTheBranchPullRequest(t *testing.T) {
	f := newFinishFixture(t)
	want := task.PullRequest{Number: 12, State: task.PROpen, URL: "https://example.test/pr/12"}
	f.prs.pr, f.prs.found = want, true

	record, changed, err := f.service.RefreshPR(t.Context(), f.record.ID)
	if err != nil || !changed || record.PR != want {
		t.Fatalf("RefreshPR() = %+v, %v, %v; want %+v recorded", record.PR, changed, err, want)
	}

	if _, changed, err := f.service.RefreshPR(t.Context(), f.record.ID); err != nil || changed {
		t.Fatalf("second RefreshPR() changed = %v, %v; want no change", changed, err)
	}
}

func TestRetitleRenamesTheWindowUntilTheUserRenamesIt(t *testing.T) {
	f := newFinishFixture(t)

	renamed, err := f.service.Retitle(t.Context(), f.record.ID, "fix login\nflow")
	if err != nil || !renamed {
		t.Fatalf("Retitle() = %v, %v; want renamed", renamed, err)
	}

	window := taskWindowOf(t, f)
	if window.Name != "fix login flow" {
		t.Fatalf("window name = %q, want the agent's title", window.Name)
	}

	handle, err := f.server.Window(t.Context(), window.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := handle.Rename(t.Context(), "mine"); err != nil {
		t.Fatal(err)
	}

	if renamed, err := f.service.Retitle(t.Context(), f.record.ID, "another title"); err != nil || renamed {
		t.Fatalf("Retitle() after a manual rename = %v, %v; want the user's name kept", renamed, err)
	}

	if name := taskWindowOf(t, f).Name; name != "mine" {
		t.Fatalf("window name = %q, want the user's name", name)
	}
}

func (prs *pullRequests) FindPR(context.Context, string, string) (task.PullRequest, string, bool, error) {
	return prs.pr, prs.head, prs.found, prs.err
}

func newFinishFixture(t *testing.T) *finishFixture {
	t.Helper()

	f := &finishFixture{
		fixture: newFixture(t), prs: &pullRequests{pr: task.PullRequest{Number: 0, State: "", URL: ""}, head: "", found: false, err: nil},
		closed: nil, record: task.EmptyRecord(), repo: "",
	}

	catalog, err := project.NewCatalog(config.ProjectsConfig{Roots: []string{filepath.Join(f.base, "code")}, Extra: nil, Overrides: nil})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultTasksConfig()
	cfg.Panes = []config.TaskPaneConfig{{Name: "editor", Command: []string{"sleep", "300"}, Dir: "", Restart: false}}
	cfg.Harness = []config.HarnessConfig{{Name: envHarness, Command: []string{"sleep", "300"}, PromptArgs: nil}}

	full := config.DefaultConfig()
	full.Tasks = cfg

	f.service = task.NewService(task.Options{
		Server: f.server, Store: f.store, Catalog: catalog, Config: cfg, Windows: full.TaskWindowFor, WorktreeDir: f.worktreeDir,
		LookPath: exec.LookPath, Now: time.Now, Agents: nil, Placeholder: nil, PRs: f.prs,
		ClosePopup: func(_ context.Context, id string) error {
			f.closed = append(f.closed, id)

			return nil
		},
	})

	f.repo = initRepository(t, filepath.Join(f.base, "code", "shop"))

	f.record, err = f.service.Start(t.Context(), projectPlace(t, f.fixture, f.repo), inNewWorktree(envHarness))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return f
}

func commit(t *testing.T, dir string) {
	t.Helper()

	runGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "work")
}

func assertTaskKept(t *testing.T, f *finishFixture) {
	t.Helper()

	assertStoredTaskState(t, f, task.StateActive)

	if _, err := os.Stat(f.record.Directory); err != nil {
		t.Fatalf("worktree was removed: %v", err)
	}

	if len(f.closed) != 0 {
		t.Fatal("popup was closed for a task that was kept")
	}

	taskWindowOf(t, f)
}

func assertTaskRemoved(t *testing.T, f *finishFixture) {
	t.Helper()

	ledger, err := f.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if _, found := ledger.Find(f.record.ID); found {
		t.Fatal("task record kept after finish")
	}

	if _, err := os.Stat(f.record.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still exists: %v", err)
	}

	if branches := gitOutput(t, f.repo, "branch", "--list", f.record.Branch); branches != "" {
		t.Fatalf("branch %s still exists", f.record.Branch)
	}

	if !slices.Equal(f.closed, []string{f.record.ID}) {
		t.Fatalf("closed popups = %v, want the task's", f.closed)
	}

	panes, err := f.server.PanesWith(t.Context(), gotmux.QueryOptions{Filter: "", ExtraFields: []string{task.OptionTask}})
	if err != nil {
		t.Fatal(err)
	}

	for _, pane := range panes {
		if id, _ := pane.Raw(task.OptionTask); string(id) == f.record.ID {
			t.Fatal("task window still open after finish")
		}
	}
}

func assertStoredTaskState(t *testing.T, f *finishFixture, want task.State) {
	t.Helper()

	ledger, err := f.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if record, _ := ledger.Find(f.record.ID); record.State != want {
		t.Fatalf("stored state = %s, want %s", record.State, want)
	}
}

func taskWindowOf(t *testing.T, f *finishFixture) gotmux.WindowInfo {
	t.Helper()

	place := projectPlaceSession(t, f)

	return taskWindow(t, f.server, place, f.record.ID)
}

func projectSessionPlace(t *testing.T, f *finishFixture) task.Place {
	t.Helper()

	return locate(t, f.fixture, sessionWindows(t, f.server, projectPlaceSession(t, f))[0].Window())
}

func projectPlaceSession(t *testing.T, f *finishFixture) gotmux.SessionID {
	t.Helper()

	sessions, err := project.LiveSessions(t.Context(), f.server)
	if err != nil {
		t.Fatal(err)
	}

	for _, live := range sessions {
		if live.Root == f.repo {
			found, err := f.server.FindSession(t.Context(), live.Name)
			if err != nil {
				t.Fatal(err)
			}

			return found.ID()
		}
	}

	t.Fatal("project session not found")

	return ""
}

func TestATaskStartedFromOriginsNewerBaseFinishesWithNothingToLose(t *testing.T) {
	f := newFinishFixture(t)
	addRemoteBranch(t, f.base, f.repo, "shared")

	upstream := filepath.Join(f.base, "upstream")
	commitAt(t, upstream, "upstream moves on", "2026-02-01T00:00:00Z")
	runGit(t, f.repo, "fetch", "-q", "origin")

	record, err := f.service.Start(t.Context(), projectPlace(t, f.fixture, f.repo), inNewWorktree(envHarness))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if got := gitOutput(t, record.Directory, "log", "-1", "--format=%s"); got != "upstream moves on" {
		t.Fatalf("new branch starts at %q, want origin's newer main", got)
	}

	if _, err := f.service.Finish(t.Context(), record.ID, task.FinishOptions{Discard: false, SkipRun: false}); err != nil {
		t.Fatalf("Finish() error = %v, want nothing to lose", err)
	}
}

func TestATaskStartsFromTheLocalBaseWhenItHasCommitsOriginLacks(t *testing.T) {
	f := newFinishFixture(t)
	addRemoteBranch(t, f.base, f.repo, "shared")
	commitAt(t, f.repo, "local work", "2026-02-01T00:00:00Z")

	record, err := f.service.Start(t.Context(), projectPlace(t, f.fixture, f.repo), inNewWorktree(envHarness))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if got := gitOutput(t, record.Directory, "log", "-1", "--format=%s"); got != "local work" {
		t.Fatalf("new branch starts at %q, want the local main", got)
	}
}

func TestFindPRSkipsPullRequestsFromForks(t *testing.T) {
	bin := t.TempDir()
	listing := `[{"number":7,"state":"CLOSED","url":"https://example.test/fork/7","headRefOid":"","isCrossRepository":true},` +
		`{"number":5,"state":"OPEN","url":"https://example.test/pr/5","headRefOid":"","isCrossRepository":false}]`
	writeFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s' '"+listing+"'\n")

	if err := os.Chmod(filepath.Join(bin, "gh"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin)

	pr, _, found, err := task.GitHub{}.FindPR(t.Context(), t.TempDir(), "feature")
	if err != nil || !found || pr.Number != 5 {
		t.Fatalf("FindPR() = %+v, %t, %v; want the repository's own pull request 5", pr, found, err)
	}
}

func TestFinishRemovesATaskWhoseCreationStoppedWithItsProcess(t *testing.T) {
	f := newFinishFixture(t)

	running := task.Process{PID: os.Getpid(), Birth: ""}
	setCreating(t, f, running)

	if _, err := f.service.Finish(t.Context(), f.record.ID, task.FinishOptions{Discard: false, SkipRun: false}); !errors.Is(err, task.ErrTaskStarting) {
		t.Fatalf("Finish() of a task its running process is creating error = %v, want ErrTaskStarting", err)
	}

	gone := exec.CommandContext(t.Context(), "true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}

	setCreating(t, f, task.Process{PID: gone.Process.Pid, Birth: "00000000-0000-0000-0000-000000000000:1"})

	live, err := task.Inventory(t.Context(), f.server, f.store)
	if err != nil {
		t.Fatal(err)
	}

	if index := slices.IndexFunc(live, func(l task.Live) bool { return l.Record.ID == f.record.ID }); index < 0 || !live[index].Stalled {
		t.Fatal("Inventory() does not report the task as stalled")
	}

	if _, err := f.service.Finish(t.Context(), f.record.ID, task.FinishOptions{Discard: false, SkipRun: false}); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	assertTaskRemoved(t, f)
}

func setCreating(t *testing.T, f *finishFixture, owner task.Process) {
	t.Helper()

	if _, err := f.store.Update(t.Context(), func(ledger *task.Ledger) error {
		record, _ := ledger.Find(f.record.ID)
		record.State, record.Owner = task.StateCreating, owner
		ledger.Put(record)

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
