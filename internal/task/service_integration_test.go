//go:build integration

package task_test

import (
	"errors"
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
	"github.com/zigai/sesh/internal/project"
	"github.com/zigai/sesh/internal/task"
)

const envWaitLimit = 10 * time.Second

type fixture struct {
	server      *gotmux.Server
	service     *task.Service
	store       task.Store
	base        string
	worktreeDir string
	envFile     string
	promptFile  string
}

func TestStartCreatesAProjectTaskInItsOwnWorktreeAndWindow(t *testing.T) {
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	place := projectPlace(t, f, repo)

	record, err := f.service.Start(t.Context(), place, inNewWorktree("fake"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	worktree := filepath.Join(f.worktreeDir, "shop", "main-1")
	want := task.EmptyRecord()
	want.ID, want.Kind, want.State, want.Project, want.Number = "t1", task.KindProject, task.StateActive, repo, 1
	want.Harness, want.Directory, want.Branch, want.Base, want.Title = "fake", worktree, "wt/main/1", "main", "1"
	want.OwnsWorktree, want.OwnsBranch = true, true
	want.CreatedAt = record.CreatedAt

	if !reflect.DeepEqual(record, want) {
		t.Fatalf("Start() = %+v, want %+v", record, want)
	}

	if got := gitOutput(t, worktree, "rev-parse", "--abbrev-ref", "HEAD"); got != "wt/main/1" {
		t.Fatalf("worktree branch = %q, want wt/main/1", got)
	}

	window := taskWindow(t, f.server, place.Session, "t1")
	if window.Name != "1" || window.PaneCount != 2 {
		t.Fatalf("task window = %+v, want name 1 with agent and editor panes", window)
	}

	panes := windowPanes(t, f.server, window.ID)
	if panes[0].Top >= panes[1].Top {
		t.Fatalf("agent pane is not above the editor pane: %+v", panes)
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

func TestStartSkipsTaskNumbersWhoseBranchOrDirectoryExists(t *testing.T) {
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	runGit(t, repo, "branch", "wt/main/1")
	mkdir(t, filepath.Join(f.worktreeDir, "shop", "main-2"))
	place := projectPlace(t, f, repo)

	first, err := f.service.Start(t.Context(), place, inNewWorktree(config.HarnessNone))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	second, err := f.service.Start(t.Context(), place, inNewWorktree(config.HarnessNone))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if first.Number != 3 || second.Number != 4 {
		t.Fatalf("task numbers = %d, %d; want 3, 4", first.Number, second.Number)
	}
}

func TestStartWithoutWorktreeDirCreatesNothing(t *testing.T) {
	f := newFixture(t, false)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	place := projectPlace(t, f, repo)

	_, err := f.service.Start(t.Context(), place, inNewWorktree("fake"))
	if !errors.Is(err, task.ErrWorktreeDirUnset) {
		t.Fatalf("Start() error = %v, want ErrWorktreeDirUnset", err)
	}

	assertNoTasks(t, f, place)
}

func TestStartRejectsAHarnessThatIsNotOffered(t *testing.T) {
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	place := projectPlace(t, f, repo)

	_, err := f.service.Start(t.Context(), place, inNewWorktree("missing"))
	if !errors.Is(err, task.ErrUnknownHarness) {
		t.Fatalf("Start() error = %v, want ErrUnknownHarness", err)
	}

	assertNoTasks(t, f, place)
}

func TestStartRecordsAFailedTaskWhenTheWorktreeCannotBeCreated(t *testing.T) {
	f := newFixture(t, true)
	repo := mkdir(t, filepath.Join(f.base, "code", "empty"))
	runGit(t, repo, "init", "-q", "-b", "main")
	place := projectPlace(t, f, repo)

	record, err := f.service.Start(t.Context(), place, inNewWorktree("fake"))
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
	f := newFixture(t, true)
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

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: "fake"})
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

func TestOfferListsEveryCheckoutAndBranchAndMarksTheOnesTasksUse(t *testing.T) {
	f := newFixture(t, true)
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

	started, err := f.service.Start(t.Context(), place, inNewWorktree(config.HarnessNone))
	if err != nil {
		t.Fatal(err)
	}

	offer, err := f.service.Offer(t.Context(), place)
	if err != nil {
		t.Fatal(err)
	}

	want := []task.Location{
		{Where: task.WhereNew, Branch: "main"},
		{Where: task.WhereMain, Branch: "main", Directory: repo},
		{Where: task.WhereWorktree, Branch: "hand", Directory: hand},
		{Where: task.WhereWorktree, Branch: "wt/main/1", Directory: started.Directory, Task: started.ID, TaskTitle: "1"},
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
		Harness: config.HarnessNone, Location: task.Location{Where: task.WhereWorktree, Directory: started.Directory},
	}); !errors.Is(err, task.ErrLocationUnavailable) {
		t.Fatalf("Start() in the task's worktree error = %v, want ErrLocationUnavailable", err)
	}
}

func TestOfferStartsANewWorktreeFromTheBranchThePaneIsOn(t *testing.T) {
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	hand := filepath.Join(f.base, "hand")
	runGit(t, repo, "worktree", "add", "-q", "-b", "hand", hand)
	place := projectPlace(t, f, repo)
	place.Dir = mkdir(t, filepath.Join(hand, "sub"))

	offer, err := f.service.Offer(t.Context(), place)
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

func TestOutsideARepositoryOfferHasNoProject(t *testing.T) {
	f := newFixture(t, true)
	initRepository(t, filepath.Join(f.base, "code", "shop"))
	dir := mkdir(t, filepath.Join(f.base, "scratch"))
	session := newSession(t, f.server, "scratch", dir)

	offer, err := f.service.Offer(t.Context(), locate(t, f, sessionWindows(t, f.server, session.ID())[0].Window()))
	if err != nil {
		t.Fatal(err)
	}

	if offer.Project != "" || len(offer.Locations) != 0 {
		t.Fatalf("Offer() = %+v, want no project and no locations", offer)
	}
}

func TestInARepositoryOutsideAProjectSessionOfferIsTheRepositorys(t *testing.T) {
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	session := newSession(t, f.server, "work", repo)

	offer, err := f.service.Offer(t.Context(), locate(t, f, sessionWindows(t, f.server, session.ID())[0].Window()))
	if err != nil {
		t.Fatal(err)
	}

	if offer.Project != repo || len(offer.Locations) == 0 || offer.Locations[0] != (task.Location{Where: task.WhereNew, Branch: "main"}) {
		t.Fatalf("Offer() = %+v, want the shop repository with a new worktree from main first", offer)
	}
}

func TestInARepositoryOutsideAProjectSessionATaskGetsAWorktreeNextToTheCurrentWindow(t *testing.T) {
	f := newFixture(t, true)
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

	record, err := f.service.Start(t.Context(), locate(t, f, first.Window()), inNewWorktree("fake"))
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
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	place := projectPlace(t, f, repo)
	request := task.Request{Harness: "prompted", Title: "Fix login: redirect!", Location: task.Location{Where: task.WhereNew}}

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

func TestStartRunsTheHarnessInTheUserShellWithItsFunctions(t *testing.T) {
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}

	home, err := f.server.Environment().Get(t.Context(), "HOME")
	if err != nil {
		t.Fatal(err)
	}

	homeDir, _ := home.Value.Get()
	rc := "shellagent() { printf '%s|' --yolo \"$@\" > " + f.promptFile + "; exec sleep 300; }\n"

	if err := os.WriteFile(filepath.Join(homeDir, ".bashrc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := f.server.GlobalSessionOptions().SetDefaultShell(t.Context(), bash); err != nil {
		t.Fatal(err)
	}

	title := `it's "quoted" $HOME`
	if _, err := f.service.Start(t.Context(), projectPlace(t, f, repo), task.Request{
		Harness: "function", Title: title, Location: task.Location{Where: task.WhereNew},
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if got := waitForFile(t, f.promptFile); got != "--yolo|"+title+"|" {
		t.Fatalf("shell function ran with %q, want its own option and the title unchanged", got)
	}
}

func TestStartWithoutATitlePassesNoPrompt(t *testing.T) {
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))

	if _, err := f.service.Start(t.Context(), projectPlace(t, f, repo), inNewWorktree("prompted")); err != nil {
		t.Fatal(err)
	}

	if got := waitForFile(t, f.promptFile); got != "0:" {
		t.Fatalf("agent arguments = %q, want none", got)
	}
}

func TestStartANewWorktreeFromAnotherBranch(t *testing.T) {
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	runGit(t, repo, "switch", "-q", "-c", "feature")
	runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "feature work")
	runGit(t, repo, "switch", "-q", "main")
	place := projectPlace(t, f, repo)

	record, err := f.service.Start(t.Context(), place, task.Request{
		Harness: config.HarnessNone, Location: task.Location{Where: task.WhereNew, Branch: "feature"},
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
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	addRemoteBranch(t, f.base, repo, "review")
	place := projectPlace(t, f, repo)

	offer, err := f.service.Offer(t.Context(), place)
	if err != nil {
		t.Fatal(err)
	}

	remote := task.Location{Where: task.WhereBranch, Branch: "review", Remote: "origin"}
	if !slices.Contains(offer.Locations, remote) || slices.Contains(offer.Locations, task.Location{Where: task.WhereBranch, Branch: "main", Remote: "origin"}) {
		t.Fatalf("Offer() locations = %+v, want origin/review and not origin/main, which main shares", offer.Locations)
	}

	record, err := f.service.Start(t.Context(), place, task.Request{Harness: config.HarnessNone, Location: remote})
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
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	hand := filepath.Join(f.base, "hand")
	runGit(t, repo, "worktree", "add", "-q", "-b", "hand", hand)
	place := projectPlace(t, f, repo)

	for _, location := range []task.Location{{Where: task.WhereWorktree, Directory: hand}, {Where: task.WhereMain}} {
		record, err := f.service.Start(t.Context(), place, task.Request{Harness: config.HarnessNone, Location: location})
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

		if _, err := f.service.Start(t.Context(), place, task.Request{Harness: config.HarnessNone, Location: location}); !errors.Is(err, task.ErrLocationUnavailable) {
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
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	runGit(t, repo, "branch", "feature")
	place := projectPlace(t, f, repo)

	record, err := f.service.Start(t.Context(), place, task.Request{
		Harness: config.HarnessNone, Location: task.Location{Where: task.WhereBranch, Branch: "feature"},
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
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))
	place := projectPlace(t, f, repo)

	for _, location := range []task.Location{
		{Where: task.WhereWorktree, Directory: mkdir(t, filepath.Join(f.base, "elsewhere"))},
		{Where: task.WhereBranch, Branch: "missing"},
		{Where: task.WhereBranch, Branch: "main"},
		{Where: ""},
	} {
		if _, err := f.service.Start(t.Context(), place, task.Request{Harness: config.HarnessNone, Location: location}); !errors.Is(err, task.ErrLocationUnavailable) {
			t.Fatalf("Start(%+v) error = %v, want ErrLocationUnavailable", location, err)
		}
	}

	assertNoTasks(t, f, place)
}

func TestOfferKeepsTheHarnessOrder(t *testing.T) {
	f := newFixture(t, true)
	repo := initRepository(t, filepath.Join(f.base, "code", "shop"))

	offer, err := f.service.Offer(t.Context(), projectPlace(t, f, repo))
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(offer.Harnesses))
	for _, harness := range offer.Harnesses {
		names = append(names, harness.Name)
	}

	if !slices.Equal(names, []string{"fake", "other", "prompted", "function"}) {
		t.Fatalf("Offer() harnesses = %v, want the configured order", names)
	}
}

func TestOpenTabOpensAPlainWindowInThePaneDirectory(t *testing.T) {
	f := newFixture(t, true)
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

func newFixture(t *testing.T, withWorktreeDir bool) fixture {
	t.Helper()

	base := realTempDir(t)
	server := tmuxtest.NewServer(t)
	store := task.NewStore(filepath.Join(base, "state", "tasks.json"))
	envFile := filepath.Join(base, "agent-env")
	promptFile := filepath.Join(base, "agent-prompt")

	worktreeDir := ""
	if withWorktreeDir {
		worktreeDir = filepath.Join(base, "worktrees")
	}

	catalog, err := project.NewCatalog(config.ProjectsConfig{Roots: []string{filepath.Join(base, "code")}, Extra: nil, Overrides: nil})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultTasksConfig()
	cfg.Editor = []string{"sleep", "300"}
	cfg.Harness = []config.HarnessConfig{
		{Name: "fake", Command: []string{"sh", "-c", `printf '%s|%s|%s|%s' "$SESH_TASK" "$SESH_PROJECT" "$SESH_WORKTREE" "$SESH_BASE" > "$0"; exec sleep 300`, envFile}},
		{Name: "other", Command: []string{"sleep", "300"}},
		{Name: "prompted", Command: []string{"sh", "-c", `printf '%s:%s' "$#" "$*" > "$0"; exec sleep 300`, promptFile}, PromptArgs: []string{"prompt for {title}"}},
		{Name: "function", Command: []string{"shellagent"}, PromptArgs: []string{"{title}"}},
	}

	service := task.NewService(task.Options{
		Server: server, Store: store, Catalog: catalog, Config: cfg, WorktreeDir: worktreeDir,
		LookPath: exec.LookPath, Now: time.Now,
	})

	return fixture{server: server, service: service, store: store, base: base, worktreeDir: worktreeDir, envFile: envFile, promptFile: promptFile}
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

// addRemoteBranch makes another repository with branch and fetches it into repo as origin.
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
