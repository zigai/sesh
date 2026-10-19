//go:build linux || darwin

package app

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var (
	ErrRepositoryRequired = errors.New("snapshot repository is not configured")
	ErrServerRequired     = errors.New("tmux server is not configured")
)

type LocalOptions struct {
	Config             *config.Config
	Server             *gotmux.Server
	Repository         *Repository
	LeaseRoot          string
	HistoryPath        string
	AgentSource        agents.SnapshotSource
	Streams            gotmux.Streams
	Source             string
	DeferSwitchHistory bool
	TaskLedger         string
	WorkState          string
	WorktreeDir        string
}

type Local struct {
	cfg         *config.Config
	server      *gotmux.Server
	repository  *Repository
	leaseRoot   string
	historyPath string
	agentSource agents.SnapshotSource
	taskLedger  string
	workState   string
	worktreeDir string
	workflow    *session.Workflow
	attachRepo  sync.Once
	handlers    map[action.ID]handler
}

// Repository opens the snapshot repository on first use and shares it between actions.
type Repository struct {
	path string
	mu   sync.Mutex
	repo *store.Repository
}

type handler func(ctx context.Context, caller action.Caller, input any) (any, error)

// NewLocal panics when a registered action other than an interactive or picker one has no handler.
func NewLocal(opts LocalOptions) *Local {
	cfg := opts.Config
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	local := &Local{
		cfg:         cfg,
		server:      opts.Server,
		repository:  opts.Repository,
		leaseRoot:   opts.LeaseRoot,
		historyPath: opts.HistoryPath,
		agentSource: opts.AgentSource,
		taskLedger:  opts.TaskLedger,
		workState:   opts.WorkState,
		worktreeDir: opts.WorktreeDir,
		workflow: session.NewWorkflowWithOptions(session.WorkflowOptions{
			Server:             opts.Server,
			Repository:         nil,
			LeaseRoot:          opts.LeaseRoot,
			HistoryPath:        opts.HistoryPath,
			Hooks:              cfg.Hooks,
			HookRunner:         nil,
			Getenv:             nil,
			Streams:            opts.Streams,
			Source:             opts.Source,
			DeferSwitchHistory: opts.DeferSwitchHistory,
		}),
		attachRepo: sync.Once{},
		handlers:   make(map[action.ID]handler),
	}

	local.registerSessionHandlers()
	local.registerSnapshotHandlers()
	local.registerAgentHandlers()
	local.registerTaskHandlers()
	local.registerProjectHandlers()

	for _, entry := range action.All() {
		descriptor := entry.Describe()
		if _, ok := local.handlers[descriptor.ID]; !ok && !descriptor.Interactive && !descriptor.InPicker {
			panic("app: no local handler for action " + string(entry.Describe().ID))
		}
	}

	return local
}

// NewRepository treats an empty path as no repository.
func NewRepository(path string) *Repository {
	return &Repository{path: path, mu: sync.Mutex{}, repo: nil}
}

func (local *Local) Accepts(at action.Location, id action.ID) bool {
	_, ok := local.handlers[id]

	return ok && at.Kind == action.LocationLocal
}

func (local *Local) Dispatch(ctx context.Context, invocation action.Invocation) (any, error) {
	run, ok := local.handlers[invocation.Action]
	if !ok || invocation.At.Kind != action.LocationLocal {
		return nil, action.Wrap(action.CodeUnsupported, action.ErrLocationRejected)
	}

	output, err := run(ctx, invocation.Caller, invocation.Input)
	if err != nil {
		return output, action.Wrap(classify(err), err)
	}

	return output, nil
}

func (local *Local) Workflow() *session.Workflow {
	return local.workflow
}

func (local *Local) Server() *gotmux.Server {
	return local.server
}

func (repository *Repository) Existing(ctx context.Context) (*store.Repository, bool, error) {
	return repository.open(ctx, false)
}

func (repository *Repository) Ensure(ctx context.Context) (*store.Repository, error) {
	repo, _, err := repository.open(ctx, true)

	return repo, err
}

func (repository *Repository) Path() string {
	if repository == nil {
		return ""
	}

	return repository.path
}

func (repository *Repository) Close() error {
	if repository == nil {
		return nil
	}

	repository.mu.Lock()
	defer repository.mu.Unlock()

	if repository.repo == nil {
		return nil
	}

	err := repository.repo.Close()
	repository.repo = nil

	if err != nil {
		return oops.In("app").With("operation", "close_repository").Wrap(err)
	}

	return nil
}

func (repository *Repository) open(ctx context.Context, create bool) (*store.Repository, bool, error) {
	if repository == nil || repository.path == "" {
		if create {
			return nil, false, action.Wrap(action.CodeUnavailable, ErrRepositoryRequired)
		}

		return nil, false, nil
	}

	repository.mu.Lock()
	defer repository.mu.Unlock()

	if repository.repo != nil {
		return repository.repo, true, nil
	}

	var (
		repo *store.Repository
		err  error
	)

	if create {
		repo, err = store.Open(ctx, repository.path)
	} else {
		repo, err = store.OpenExisting(ctx, repository.path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
	}

	if err != nil {
		return nil, false, oops.In("app").With("operation", "open_repository").Wrapf(err, "open repository")
	}

	repository.repo = repo

	return repo, true, nil
}

func (local *Local) useRepository(ctx context.Context, create bool) (*store.Repository, bool, error) {
	repo, found, err := local.repository.open(ctx, create)
	if err != nil || !found {
		return nil, false, err
	}

	// Actions may run concurrently, so the shared workflow is given the repository once.
	local.attachRepo.Do(func() { local.workflow.SetRepository(repo) })

	return repo, true, nil
}

func (local *Local) ensureLeaseRoot() error {
	if local.leaseRoot == "" {
		return nil
	}

	dir, err := privatefs.Ensure(local.leaseRoot)
	if err != nil {
		return oops.In("app").With("operation", "ensure_lease_root").Wrap(err)
	}

	if err := dir.Close(); err != nil {
		return oops.In("app").With("operation", "close_lease_root").Wrap(err)
	}

	return nil
}

func handle[In, Out any](
	local *Local,
	definition *action.Definition[In, Out],
	run func(ctx context.Context, caller action.Caller, input In) (Out, error),
) {
	local.handlers[definition.ID] = func(ctx context.Context, caller action.Caller, input any) (any, error) {
		typed, err := definition.Input(input)
		if err != nil {
			return nil, oops.In("app").With("action", string(definition.ID)).Wrap(err)
		}

		return run(ctx, caller, typed)
	}
}
