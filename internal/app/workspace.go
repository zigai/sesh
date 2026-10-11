package app

import (
	"context"
	"os"
	"path/filepath"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/snapshot/store"
)

// worktreeDirEnvironment names the directory that holds task worktrees, one folder per project.
const worktreeDirEnvironment = "WORKTREE_DIR"

type Locations struct {
	Socket          string
	Repository      string
	LeaseRoot       string
	History         string
	FinalSaveStatus string
	AgentStore      string
}

type Workspace struct {
	Config          *config.Config
	Socket          string
	Repository      string
	LeaseRoot       string
	History         string
	FinalSaveStatus string
	AgentStore      string
	Tasks           string
	WorktreeDir     string
}

func Resolve(cfg *config.Config, locations Locations) (Workspace, error) {
	if cfg == nil {
		return Workspace{}, oops.In("app").With("operation", "resolve_workspace").Wrap(config.ErrNilConfig)
	}

	dataDir, err := config.GetDataDir(cfg)
	if err != nil {
		return Workspace{}, oops.In("app").With("operation", "resolve_data_dir").Wrap(err)
	}

	stateDir := filepath.Join(config.GetXDGPaths().StateHome, "sesh")

	workspace := Workspace{
		Config:          cfg,
		Socket:          locations.Socket,
		Repository:      "",
		LeaseRoot:       "",
		History:         "",
		FinalSaveStatus: "",
		AgentStore:      "",
		Tasks:           filepath.Join(stateDir, "tasks.json"),
		WorktreeDir:     os.Getenv(worktreeDirEnvironment),
	}

	for _, path := range []struct {
		target   *string
		value    string
		fallback string
	}{
		{&workspace.Repository, locations.Repository, filepath.Join(dataDir, "snapshots")},
		{&workspace.LeaseRoot, locations.LeaseRoot, filepath.Join(dataDir, "leases")},
		{&workspace.History, locations.History, filepath.Join(stateDir, "history.json")},
		{&workspace.FinalSaveStatus, locations.FinalSaveStatus, filepath.Join(stateDir, automation.FinalSaveStatusName)},
		{&workspace.AgentStore, locations.AgentStore, cfg.ResolvedAgentStatus().StorePath},
	} {
		value := path.value
		if value == "" {
			value = path.fallback
		}

		if value == "" {
			continue
		}

		absolute, err := filepath.Abs(value)
		if err != nil {
			return Workspace{}, oops.In("app").With("operation", "resolve_workspace_path", "path", value).Wrap(err)
		}

		*path.target = absolute
	}

	return workspace, nil
}

func (w Workspace) OpenTmux() (*gotmux.Server, error) {
	server, err := tmuxserver.Open(w.Socket)
	if err != nil {
		return nil, oops.In("app").With("operation", "open_tmux").Wrap(err)
	}

	return server, nil
}

func (w Workspace) OpenRepository(ctx context.Context) (*store.Repository, error) {
	repository, err := store.Open(ctx, w.Repository)
	if err != nil {
		return nil, oops.In("app").With("operation", "open_repository").Wrapf(err, "open snapshot repository")
	}

	return repository, nil
}

func (w Workspace) OpenExistingRepository(ctx context.Context) (*store.Repository, error) {
	repository, err := store.OpenExisting(ctx, w.Repository)
	if err != nil {
		return nil, oops.In("app").With("operation", "open_repository").Wrapf(err, "open snapshot repository")
	}

	return repository, nil
}

func (w Workspace) EnsureLeaseRoot() error {
	dir, err := privatefs.Ensure(w.LeaseRoot)
	if err != nil {
		return oops.In("app").With("operation", "ensure_lease_root").Wrap(err)
	}

	if err := dir.Close(); err != nil {
		return oops.In("app").With("operation", "close_lease_root").Wrap(err)
	}

	return nil
}

func (w Workspace) AgentSource() *agents.AHTStore {
	return agents.NewAHTStore(w.AgentStore)
}

func (w Workspace) ProcessEnvironment() ([]string, error) {
	keys := w.Config.Snapshot.ProcessEnvironment
	if err := process.ValidateEnvironmentKeys(keys); err != nil {
		return nil, oops.In("app").With("operation", "validate_process_environment").Wrap(err)
	}

	return config.FilterProcessEnvironmentKeys(keys), nil
}
