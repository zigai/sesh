package task

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/git"
	"github.com/zigai/sesh/internal/project"
)

const (
	// OptionTask is the window user option that ties a tmux window to its task record.
	OptionTask = "@sesh_task"
	// OptionRole names the panes of a task window: the agent pane and the configured panes.
	OptionRole = "@sesh_role"
	RoleAgent  = config.TaskPaneAgent

	EnvTask     = "SESH_TASK"
	EnvProject  = "SESH_PROJECT"
	EnvWorktree = "SESH_WORKTREE"
	EnvBase     = "SESH_BASE"

	branchPrefix  = "wt/"
	maxNumberScan = 1000
	launchShell   = "/bin/sh"

	worktreeParentMode = 0o755
	launchScript       = `trap ':' INT QUIT; "$0" -i -c "$1"; trap - INT QUIT; exec "$0" -i`
)

var (
	ErrUnknownHarness = errors.New("harness is not offered for tasks")
	ErrNoBaseBranch   = errors.New("project has no default branch to start a task from")
	ErrNoFreeNumber   = errors.New("no free task number for project")
	ErrPaneNotFound   = errors.New("pane not found")
	ErrDefaultShell   = errors.New("tmux default-shell is not set")
)

// Place is where a task command was invoked: the pane, its session and directory, the project of
// a project session, and the task of a task window.
type Place struct {
	Pane    gotmux.PaneID
	Session gotmux.SessionID
	Dir     string
	Project string
	Task    string
}

type Options struct {
	Server  *gotmux.Server
	Store   Store
	Catalog project.Catalog
	Config  config.TasksConfig
	// Windows returns the task window configuration of the project at a root, or of a loose task
	// for an empty root.
	Windows     func(project string) config.TaskWindow
	WorktreeDir string
	LookPath    func(string) (string, error)
	Now         func() time.Time
	Agents      AgentFinder
	// Placeholder is the command line a sleeping task's agent pane runs.
	Placeholder func(id string) []string
	PRs         PRFinder
	// ClosePopup closes a task's popup session, if it has one.
	ClosePopup func(ctx context.Context, id string) error
}

// Service creates tasks and their windows on one tmux server.
type Service struct {
	server      *gotmux.Server
	store       Store
	catalog     project.Catalog
	cfg         config.TasksConfig
	windows     func(project string) config.TaskWindow
	worktreeDir string
	lookPath    func(string) (string, error)
	now         func() time.Time
	agents      AgentFinder
	placeholder func(id string) []string
	prs         PRFinder
	closePopup  func(ctx context.Context, id string) error
}

func NewService(opts Options) *Service {
	return &Service{
		server: opts.Server, store: opts.Store, catalog: opts.Catalog, cfg: opts.Config, windows: opts.Windows,
		worktreeDir: opts.WorktreeDir, lookPath: opts.LookPath, now: opts.Now, agents: opts.Agents,
		placeholder: opts.Placeholder, prs: opts.PRs, closePopup: opts.ClosePopup,
	}
}

// Locate describes the pane a command was invoked from.
func (s *Service) Locate(ctx context.Context, pane gotmux.PaneID) (Place, error) {
	panes, err := s.server.PanesWith(ctx, gotmux.QueryOptions{
		Filter:      gotmux.Format("#{==:#{pane_id}," + string(pane) + "}"),
		ExtraFields: []string{project.OptionProject, OptionTask},
	})
	if err != nil {
		return Place{}, taskError("locate", err)
	}

	if len(panes) != 1 {
		return Place{}, taskError("locate", ErrPaneNotFound)
	}

	info := panes[0]

	session, ok := info.SessionID.Get()
	if !ok {
		return Place{}, taskError("locate", ErrPaneNotFound)
	}

	projectRoot, _ := info.Raw(project.OptionProject)
	taskID, _ := info.Raw(OptionTask)

	return Place{Pane: info.ID, Session: session, Dir: info.CurrentPath, Project: string(projectRoot), Task: string(taskID)}, nil
}

// Start creates a task at place running the requested harness. In a project session or a repository the task works in the requested
// location, one the project currently offers, with a window in the pane's session, next to the
// current window outside a project session; elsewhere it is a loose task in the pane's directory,
// next to the current window. A title names the task and is passed to the agent as its first
// prompt when its harness says how.
func (s *Service) Start(ctx context.Context, place Place, request Request) (Record, error) {
	offered, err := s.harness(request.Harness)
	if err != nil {
		return Record{}, taskError("start_task", err)
	}

	root, err := s.projectOf(ctx, place)
	if err != nil {
		return Record{}, taskError("start_task", err)
	}

	if request.Layout != "" && !layoutNamed(s.windows(root), request.Layout) {
		return Record{}, taskError("start_task", fmt.Errorf("%w: %s", ErrUnknownLayout, request.Layout))
	}

	if root == "" {
		return s.startLoose(ctx, place, request, offered)
	}

	return s.startProject(ctx, place, root, request, offered)
}

// OpenTab opens a plain window in the pane's directory next to the current window.
func (s *Service) OpenTab(ctx context.Context, place Place) error {
	session, err := s.server.Session(ctx, place.Session)
	if err != nil {
		return taskError("open_tab", err)
	}

	_, err = session.NewWindow(ctx, gotmux.NewWindowOptions{
		Name: "", Dir: place.Dir, Program: gotmux.Program{}, Env: nil, TmuxEnv: nil,
		Index: nil, Before: false, After: true, Select: true,
	})

	return taskError("open_tab", err)
}

// projectOf returns the root of the project a task started at place belongs to: the project
// session's, else the repository the pane's directory is in, else none. A registered folder
// outside a project session leaves the task loose, in the pane's directory.
func (s *Service) projectOf(ctx context.Context, place Place) (string, error) {
	if place.Project != "" {
		return place.Project, nil
	}

	p, err := s.catalog.Resolve(ctx, place.Dir)

	switch {
	case errors.Is(err, project.ErrNotProject):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("resolve task project: %w", err)
	case p.Kind != project.KindGit:
		return "", nil
	default:
		return p.Root, nil
	}
}

func (s *Service) startProject(ctx context.Context, place Place, root string, request Request, offered Harness) (Record, error) {
	p, err := s.catalog.Resolve(ctx, root)
	if err != nil {
		return Record{}, taskError("start_task", err)
	}

	location, local, err := s.offered(ctx, p, request.Location)
	if err != nil {
		return Record{}, taskError("start_task", err)
	}

	record, err := s.reserveProjectTask(ctx, p, request, location, local)
	if err != nil {
		return Record{}, taskError("start_task", err)
	}

	if record.OwnsWorktree {
		if err := s.addWorktree(ctx, p.Root, record); err != nil {
			return record, s.fail(ctx, record, err)
		}
	}

	if err := s.openTaskWindow(ctx, place, record, launchOf(offered, request, record), request.Layout, place.Project == ""); err != nil {
		return record, s.fail(ctx, record, err)
	}

	return s.activate(ctx, record)
}

func (s *Service) startLoose(ctx context.Context, place Place, request Request, offered Harness) (Record, error) {
	owner := currentProcess(ctx)

	record, err := s.reserve(ctx, func(ledger *Ledger) (Record, error) {
		record := EmptyRecord()
		record.ID = ledger.NextID()
		record.Kind = KindLoose
		record.State = StateCreating
		record.Owner = owner
		record.Harness = request.Harness
		record.Directory = place.Dir
		record.Title = cmp.Or(request.Title, filepath.Base(place.Dir))
		record.CreatedAt = s.now().Unix()

		return record, nil
	})
	if err != nil {
		return Record{}, taskError("start_task", err)
	}

	if err := s.openTaskWindow(ctx, place, record, launchOf(offered, request, record), request.Layout, true); err != nil {
		return record, s.fail(ctx, record, err)
	}

	return s.activate(ctx, record)
}

// reserveProjectTask records the new task before any worktree or window exists, so a crash
// part way through leaves a record of what was being created. A new worktree's number skips any
// branch or directory that already exists, including ones made by hand with the same naming
// scheme, and a new branch skips the names of local branches. A task in an existing checkout is
// refused if another task took it meanwhile. Checking out a branch only a remote has creates a
// local branch tracking it, which sesh then owns.
func (s *Service) reserveProjectTask(ctx context.Context, p project.Project, request Request, location Location, local []string) (Record, error) {
	creates := location.Where == WhereNew || location.Where == WhereBranch
	base := ""

	if p.Kind == project.KindGit {
		branch, err := defaultBranch(ctx, p.Root)
		if err != nil {
			return Record{}, err
		}

		base = branch
	}

	owner := currentProcess(ctx)

	return s.reserve(ctx, func(ledger *Ledger) (Record, error) {
		state := ledger.Project(p.Root)
		number, directory := state.LastNumber+1, location.Directory

		if creates {
			var err error

			number, directory, err = s.freeNumber(ctx, p, base, state.LastNumber)
			if err != nil {
				return Record{}, err
			}
		} else if slices.ContainsFunc(ledger.Tasks, func(record Record) bool { return sameDirectory(record.Directory, directory) }) {
			return Record{}, ErrLocationUnavailable
		}

		ledger.SetProject(ProjectState{Root: p.Root, LastNumber: number})

		record := EmptyRecord()
		record.ID = ledger.NextID()
		record.Kind = KindProject
		record.State = StateCreating
		record.Owner = owner
		record.Project = p.Root
		record.Number = number
		record.Harness = request.Harness
		record.Directory = directory
		record.Branch = location.Branch
		record.Base = base
		record.OwnsWorktree = creates
		record.OwnsBranch = location.Where == WhereNew || location.Remote != ""
		record.Title = cmp.Or(request.Title, filepath.Base(directory))
		record.CreatedAt = s.now().Unix()

		switch {
		case location.Where == WhereNew:
			record.Base = location.Branch
			record.Branch = NewBranch(request.Title, location.Branch, number, local)
		case location.Remote != "":
			record.Upstream = location.Remote + "/" + location.Branch
		}

		return record, nil
	})
}

func (s *Service) freeNumber(ctx context.Context, p project.Project, base string, last int) (int, string, error) {
	if p.Kind != project.KindGit {
		return last + 1, p.Root, nil
	}

	for number := last + 1; number <= last+maxNumberScan; number++ {
		directory := filepath.Join(s.worktreeDir, p.Name, base+"-"+strconv.Itoa(number))

		if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
			continue
		}

		exists, err := branchExists(ctx, p.Root, taskBranch(base, number))
		if err != nil {
			return 0, "", err
		}

		if !exists {
			return number, directory, nil
		}
	}

	return 0, "", ErrNoFreeNumber
}

func (s *Service) reserve(ctx context.Context, build func(*Ledger) (Record, error)) (Record, error) {
	var record Record

	_, err := s.store.Update(ctx, func(ledger *Ledger) error {
		built, err := build(ledger)
		if err != nil {
			return err
		}

		record = built
		ledger.Put(record)

		return nil
	})

	return record, err
}

func (s *Service) addWorktree(ctx context.Context, root string, record Record) error {
	if err := os.MkdirAll(filepath.Dir(record.Directory), worktreeParentMode); err != nil {
		return fmt.Errorf("create worktree parent: %w", err)
	}

	args := []string{"worktree", "add", record.Directory, record.Branch}

	switch {
	case record.Upstream != "":
		args = []string{"worktree", "add", "--track", "-b", record.Branch, record.Directory, record.Upstream}
	case record.OwnsBranch:
		start, err := startPoint(ctx, root, record.Base)
		if err != nil {
			return err
		}

		args = []string{"worktree", "add", "--no-track", "-b", record.Branch, record.Directory, start}
	}

	if _, err := git.Output(ctx, root, args...); err != nil {
		return fmt.Errorf("add task worktree: %w", err)
	}

	return nil
}

// openTaskWindow opens a task's window in place's session running launch, with the panes and
// layout of its project. A session no client shows is laid out for the size of place's window,
// the size the user is likely to see it at.
func (s *Service) openTaskWindow(
	ctx context.Context,
	place Place,
	record Record,
	launch []string,
	layout string,
	nextToCurrent bool,
) error {
	session, err := s.server.Session(ctx, place.Session)
	if err != nil {
		return fmt.Errorf("find task session: %w", err)
	}

	expected, err := s.expectedSize(ctx, session, place.Pane)
	if err != nil {
		return err
	}

	shell, err := defaultShell(ctx, s.server)
	if err != nil {
		return err
	}

	env := Env(record)

	link, err := session.NewWindow(ctx, gotmux.NewWindowOptions{
		Name: record.Title, Dir: record.Directory, Program: shellProgram(shell, launch), Env: nil, TmuxEnv: env,
		Index: nil, Before: false, After: nextToCurrent, Select: true,
	})
	if err != nil {
		return fmt.Errorf("open task window: %w", err)
	}

	window := link.Window()

	if err := window.Options().SetUser(ctx, OptionTask, record.ID); err != nil {
		return fmt.Errorf("tag task window: %w", err)
	}

	agent, err := window.ActivePane(ctx)
	if err != nil {
		return fmt.Errorf("find agent pane: %w", err)
	}

	if err := agent.Options().SetUser(ctx, OptionRole, RoleAgent); err != nil {
		return fmt.Errorf("tag agent pane: %w", err)
	}

	return s.arrange(ctx, window, record, layout, expected, true)
}

// expectedSize is the size of the window of pane when no client shows session, else zero.
func (s *Service) expectedSize(ctx context.Context, session gotmux.Session, pane gotmux.PaneID) (gotmux.Size, error) {
	none := gotmux.Size{Width: 0, Height: 0}

	info, err := session.Info(ctx)
	if err != nil {
		return none, fmt.Errorf("inspect task session: %w", err)
	}

	if info.Attached > 0 || pane == "" {
		return none, nil
	}

	caller, err := s.server.Pane(ctx, pane)
	if err != nil {
		return none, fmt.Errorf("find calling pane: %w", err)
	}

	window, err := caller.Window(ctx)
	if err != nil {
		return none, fmt.Errorf("find calling window: %w", err)
	}

	size, err := window.Info(ctx)
	if err != nil {
		return none, fmt.Errorf("inspect calling window: %w", err)
	}

	return gotmux.Size{Width: size.Width, Height: size.Height}, nil
}

func (s *Service) activate(ctx context.Context, record Record) (Record, error) {
	record.State = StateActive

	_, err := s.store.Update(ctx, func(ledger *Ledger) error {
		ledger.Put(record)

		return nil
	})

	return record, taskError("record_task", err)
}

// fail marks a task whose creation stopped part way. Whatever was already created stays: the
// record names the worktree and branch so they can be inspected or finished by hand.
func (s *Service) fail(ctx context.Context, record Record, cause error) error {
	record.State = StateFailed

	return taskError("start_task", errors.Join(cause, s.put(context.WithoutCancel(ctx), record)))
}

func (s *Service) harness(name string) (Harness, error) {
	offered, found := Find(Harnesses(s.cfg, s.lookPath), name)
	if !found {
		return offered, fmt.Errorf("%w: %s", ErrUnknownHarness, name)
	}

	return offered, nil
}

// launchOf is the command line that starts a task's agent: the harness command, followed by its
// prompt arguments filled in with the task when the task was given a title.
func launchOf(offered Harness, request Request, record Record) []string {
	launch := slices.Clone(offered.Command)
	if len(launch) == 0 || request.Title == "" {
		return launch
	}

	for _, argument := range offered.PromptArgs {
		launch = append(launch, Render(argument, record))
	}

	return launch
}

// record returns the task with id from the ledger.
func (s *Service) record(ctx context.Context, id string) (Record, error) {
	ledger, err := s.store.Load(ctx)
	if err != nil {
		return EmptyRecord(), err
	}

	record, found := ledger.Find(id)
	if !found {
		return EmptyRecord(), ErrNotFound
	}

	return record, nil
}

func (s *Service) put(ctx context.Context, record Record) error {
	_, err := s.store.Update(ctx, func(ledger *Ledger) error {
		ledger.Put(record)

		return nil
	})

	return err
}

func defaultShell(ctx context.Context, server *gotmux.Server) (string, error) {
	value, err := server.GlobalSessionOptions().DefaultShell(ctx)
	if err != nil {
		return "", fmt.Errorf("read default shell: %w", err)
	}

	shell, ok := value.Effective.Get()
	if !ok || shell == "" {
		return "", ErrDefaultShell
	}

	return shell, nil
}

// shellProgram runs argv in the user's interactive shell, so its functions and aliases apply as
// when the user types the command, and then that shell in the same pane, so an agent or editor
// that exits leaves a usable shell where it ran. Interrupts reach argv, not the wrapper.
func shellProgram(shell string, argv []string) gotmux.Program {
	if len(argv) == 0 {
		return gotmux.Program{}
	}

	command := shellArgv(shell, argv)

	return gotmux.Exec(command[0], command[1:]...)
}

func shellArgv(shell string, argv []string) []string {
	words := make([]string, 0, len(argv))
	for _, argument := range argv {
		words = append(words, shellQuote(argument))
	}

	return []string{launchShell, "-m", "-c", launchScript, shell, strings.Join(words, " ")}
}

// shellQuote quotes an argument for sh, bash, zsh and fish: inside single quotes nothing is
// special, and a single quote ends the quoting, is escaped, and reopens it.
func shellQuote(argument string) string {
	return "'" + strings.ReplaceAll(argument, "'", `'\''`) + "'"
}

func taskBranch(base string, number int) string {
	return branchPrefix + base + "/" + strconv.Itoa(number)
}

func defaultBranch(ctx context.Context, root string) (string, error) {
	if remote, err := git.Output(ctx, root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if branch, found := strings.CutPrefix(remote, "origin/"); found && branch != "" {
			return branch, nil
		}
	}

	branch, err := git.Output(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch == "" {
		return "", errors.Join(ErrNoBaseBranch, err)
	}

	return branch, nil
}

func branchExists(ctx context.Context, root, branch string) (bool, error) {
	return refExists(ctx, root, "refs/heads/"+branch)
}

// startPoint is where a new branch from base starts: origin's base when the local base is
// missing or behind it, and otherwise the local base. Nothing is fetched.
func startPoint(ctx context.Context, root, base string) (string, error) {
	local, remote := "refs/heads/"+base, "refs/remotes/origin/"+base

	onRemote, err := refExists(ctx, root, remote)
	if err != nil || !onRemote {
		return local, err
	}

	isLocal, err := refExists(ctx, root, local)
	if err != nil || !isLocal {
		return remote, err
	}

	_, err = git.Output(ctx, root, "merge-base", "--is-ancestor", local, remote)
	if _, diverged := errors.AsType[*git.CommandError](err); diverged {
		return local, nil
	}

	if err != nil {
		return "", fmt.Errorf("compare base with origin: %w", err)
	}

	return remote, nil
}

func refExists(ctx context.Context, root, ref string) (bool, error) {
	_, err := git.Output(ctx, root, "rev-parse", "--verify", "--quiet", ref)
	if _, missing := errors.AsType[*git.CommandError](err); missing {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("check git ref: %w", err)
	}

	return true, nil
}

func taskError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return oops.In("task").Code("task_failed").With("operation", operation).Wrap(err)
}
