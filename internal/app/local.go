//go:build linux || darwin

// Package app runs actions on this machine. It is the composition root that connects action
// definitions to the services that own each operation.
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
}

// Local runs actions against this machine's tmux server and snapshot repository.
type Local struct {
	cfg         *config.Config
	server      *gotmux.Server
	repository  *Repository
	leaseRoot   string
	historyPath string
	agentSource agents.SnapshotSource
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

// NewLocal wires every registered action to its handler. A registered action without a
// handler is a programming error, so construction panics instead of failing at dispatch.
// Interactive actions belong to the front end that owns the invoking client.
func NewLocal(options LocalOptions) *Local {
	cfg := options.Config
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	local := &Local{
		cfg:         cfg,
		server:      options.Server,
		repository:  options.Repository,
		leaseRoot:   options.LeaseRoot,
		historyPath: options.HistoryPath,
		agentSource: options.AgentSource,
		workflow: session.NewWorkflowWithOptions(session.WorkflowOptions{
			Server:             options.Server,
			Repository:         nil,
			LeaseRoot:          options.LeaseRoot,
			HistoryPath:        options.HistoryPath,
			Hooks:              cfg.Hooks,
			HookRunner:         nil,
			Getenv:             nil,
			Streams:            options.Streams,
			Source:             options.Source,
			DeferSwitchHistory: options.DeferSwitchHistory,
		}),
		attachRepo: sync.Once{},
		handlers:   make(map[action.ID]handler),
	}

	local.registerSessionHandlers()
	local.registerSnapshotHandlers()
	local.registerAgentHandlers()

	for _, entry := range action.All() {
		descriptor := entry.Describe()
		if _, ok := local.handlers[descriptor.ID]; !ok && !descriptor.Interactive {
			panic("app: no local handler for action " + string(entry.Describe().ID))
		}
	}

	return local
}

// NewRepository returns a lazily opened repository handle; an empty path means no repository.
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

// Workflow exposes the session workflow for flows that are not actions, such as entering a
// session from the picker.
func (local *Local) Workflow() *session.Workflow {
	return local.workflow
}

func (local *Local) Server() *gotmux.Server {
	return local.server
}

// Existing opens the repository when it exists and reports whether it does.
func (repository *Repository) Existing(ctx context.Context) (*store.Repository, bool, error) {
	return repository.open(ctx, false)
}

// Ensure opens the repository, creating it when missing.
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

// useRepository attaches the repository to the workflow for actions that touch saved state.
// The handle keeps the repository it opened, and actions may run concurrently, so the shared
// workflow is given it once.
func (local *Local) useRepository(ctx context.Context, create bool) (*store.Repository, bool, error) {
	repo, found, err := local.repository.open(ctx, create)
	if err != nil || !found {
		return nil, false, err
	}

	local.attachRepo.Do(func() { local.workflow.SetRepository(repo) })

	return repo, true, nil
}

// ensureLeaseRoot creates the lease root before an action takes a lease, so actions that never
// lease leave the filesystem untouched. An unset root is reported by the lease holder.
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
