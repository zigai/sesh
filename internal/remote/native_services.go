//go:build linux || darwin

package remote

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/samber/oops"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var (
	errNativeOperationPartial   = errors.New("native operation is incomplete")
	errNativeOperationUncertain = errors.New("native operation outcome is uncertain")
)

type NativeSavedConfig struct {
	Repository         string
	LeaseRoot          string
	Server             *gotmux.Server
	ProcessEnvironment []string
	AgentSource        agents.SessionSource
}

type NativeSavedSessions struct{ config NativeSavedConfig }

func NewNativeSavedSessions(cfg NativeSavedConfig) (*NativeSavedSessions, error) {
	if cfg.Repository == "" || cfg.LeaseRoot == "" || cfg.Server == nil {
		return nil, oops.In("remote").Code("remote_native_failed").With("operation", "new_native_saved_sessions").Wrap(ErrIncompleteDependencies)
	}

	if cfg.ProcessEnvironment == nil {
		cfg.ProcessEnvironment = slices.Clone(config.DefaultProcessEnvironment)
	}

	if cfg.AgentSource == nil {
		cfg.AgentSource = agents.NewAHTStore("")
	}

	if err := process.ValidateEnvironmentKeys(cfg.ProcessEnvironment); err != nil {
		return nil, oops.In("remote").Code("remote_native_failed").With("operation", "new_native_saved_sessions").Wrap(err)
	}

	cfg.ProcessEnvironment = config.FilterProcessEnvironmentKeys(cfg.ProcessEnvironment)

	return &NativeSavedSessions{config: cfg}, nil
}

func (service *NativeSavedSessions) Delete(ctx context.Context, generation, sessionID string) (string, error) {
	if service == nil || service.config.Server == nil {
		return "", oops.In("remote").Code("remote_native_failed").With("operation", "delete").Wrap(ErrIncompleteDependencies)
	}

	repo, err := store.OpenExisting(ctx, service.config.Repository)
	if err != nil {
		return "", oops.In("remote").Code("remote_native_failed").With("operation", "delete").Wrap(err)
	}

	rewriteID, deleteErr := service.workflow(repo).DeleteSavedSessionTarget(ctx, generation, sessionID)
	if deleteErr != nil {
		deleteErr = oops.In("remote").Code("remote_native_failed").With("operation", "delete").Wrap(deleteErr)
	}

	return rewriteID, errors.Join(deleteErr, closeNativeRepository(repo))
}

func (service *NativeSavedSessions) Snapshot(ctx context.Context, socket string) (string, error) {
	if service == nil || service.config.Server == nil || socket != service.config.Server.Endpoint().SocketPath {
		return "", oops.In("remote").Code("remote_native_failed").With("operation", "snapshot").Wrap(ErrTargetChanged)
	}

	repo, err := store.Open(ctx, service.config.Repository)
	if err != nil {
		return "", oops.In("remote").Code("remote_native_failed").With("operation", "snapshot").Wrap(err)
	}

	live, err := session.ListLive(ctx, service.config.Server, "")
	if err != nil {
		return "", errors.Join(oops.In("remote").Code("remote_native_failed").With("operation", "snapshot").Wrap(err), repo.Close())
	}

	selected := make([]gotmux.SessionID, 0, len(live))
	for _, row := range live {
		selected = append(selected, gotmux.SessionID(row.ID))
	}

	result, err := engine.SaveObservation(ctx, repo, service.config.Server, selected, engine.SaveConfig{
		LeaseRoot:              service.config.LeaseRoot,
		ProcessEnrichment:      true,
		ProcessEnvironmentKeys: service.config.ProcessEnvironment,
		AgentSource:            service.config.AgentSource,
		Profile:                engine.RecoveryPractical,
	})
	if err == nil && result.Status != "complete" && result.Status != "best_effort" {
		err = errNativeOperationPartial
	}

	if err != nil {
		err = oops.In("remote").Code("remote_native_failed").With("operation", "snapshot").Wrap(err)
	}

	return result.Attempt, errors.Join(err, repo.Close())
}

func (service *NativeSavedSessions) Restore(ctx context.Context, socket, generation, sessionID string) (string, error) {
	if service == nil || service.config.Server == nil || socket != service.config.Server.Endpoint().SocketPath {
		return "", oops.In("remote").Code("remote_native_failed").With("operation", "restore").Wrap(ErrTargetChanged)
	}

	repo, err := store.OpenExisting(ctx, service.config.Repository)
	if err != nil {
		return "", oops.In("remote").Code("remote_native_failed").With("operation", "restore").Wrap(err)
	}

	if err := validateRestoreSource(ctx, repo, generation, socket); err != nil {
		return "", errors.Join(oops.In("remote").Code("remote_native_failed").With("operation", "restore").Wrap(errors.Join(ErrTargetChanged, err)), repo.Close())
	}

	rows, err := session.ListSaved(ctx, repo, service.config.Repository, generation)
	if err != nil {
		return "", errors.Join(oops.In("remote").Code("remote_native_failed").With("operation", "restore").Wrap(err), repo.Close())
	}

	ids := savedSessionIDs(rows, sessionID)
	if len(ids) == 0 {
		return "", errors.Join(oops.In("remote").Code("remote_native_failed").With("operation", "restore").Wrap(session.ErrSessionNotFound), repo.Close())
	}

	result, err := engine.Restore(ctx, repo, service.config.Server, engine.RestoreConfig{
		LeaseRoot: service.config.LeaseRoot, Generation: generation, Sessions: ids, OperationID: "",
		LaunchProcesses: true, AllowMissingWorkingDir: false, CompensateOnFailure: false,
		Profile: engine.RecoveryPractical,
	})
	if err == nil && restoreIncomplete(result) {
		if result.Uncertain {
			err = errNativeOperationUncertain
		} else {
			err = errNativeOperationPartial
		}
	}

	if err != nil {
		err = oops.In("remote").Code("remote_native_failed").With("operation", "restore").Wrap(err)
	}

	return result.OperationID, errors.Join(err, repo.Close())
}

func validateRestoreSource(ctx context.Context, repo *store.Repository, generation, socket string) error {
	manifest, err := repo.Read(ctx, generation)
	if err != nil {
		return oops.In("remote").Code("remote_native_failed").With("operation", "validate_restore_source").Wrap(err)
	}

	endpointScope, err := model.EndpointScope(socket)
	if err != nil {
		return oops.In("remote").Code("remote_native_failed").With("operation", "validate_restore_source").Wrap(err)
	}

	if manifest.Source.EndpointScope != endpointScope {
		return oops.In("remote").Code("remote_native_failed").With("operation", "validate_restore_source").Wrap(ErrTargetChanged)
	}

	return nil
}

func savedSessionIDs(rows []session.Session, id string) []string {
	var ids []string

	for _, row := range rows {
		if row.ID == id {
			ids = append(ids, row.ID)
		}
	}

	return ids
}

func restoreIncomplete(result engine.RestoreResult) bool {
	if !result.Verified || result.Uncertain {
		return true
	}

	switch result.Status {
	case "best_effort":
		return false
	case "complete":
		return len(result.MissingRequirements) != 0
	default:
		return true
	}
}

func (service *NativeSavedSessions) DeleteLiveAndSaved(ctx context.Context, target session.Session, _ LiveServer) (string, error) {
	if service == nil || service.config.Server == nil {
		return "", oops.In("remote").Code("remote_native_failed").With("operation", "delete_live_and_saved").Wrap(ErrIncompleteDependencies)
	}

	repo, err := store.OpenExisting(ctx, service.config.Repository)
	if errors.Is(err, os.ErrNotExist) {
		repo = nil
		err = nil
	}

	if err != nil {
		return "", oops.In("remote").Code("remote_native_failed").With("operation", "delete_live_and_saved").Wrap(err)
	}

	workflow := service.workflow(repo)
	outcome, err := workflow.DeleteSessionTargetEverywhere(ctx, target.Name, target.ID, false)

	operationID := ""
	if len(outcome.PendingRewrites) > 0 {
		operationID = outcome.PendingRewrites[0]
	}

	if err != nil {
		err = oops.In("remote").Code("remote_native_failed").With("operation", "delete_live_and_saved").Wrap(err)
	}

	return operationID, errors.Join(err, closeNativeRepository(repo))
}

func (service *NativeSavedSessions) workflow(repo *store.Repository) *session.Workflow {
	return session.NewWorkflowWithOptions(session.WorkflowOptions{
		Server: service.config.Server, Repository: repo, LeaseRoot: service.config.LeaseRoot,
		HistoryPath: "",
		Hooks: config.HookConfig{
			Timeout: "", SessionSwitchPre: nil, SessionSwitchPost: nil,
			SessionAttachPre: nil, SessionAttachPost: nil, TmuxStartupPost: nil,
		},
		HookRunner: nil, Getenv: nil,
		Streams: gotmux.TerminalStreams{In: nil, Out: nil, Err: nil}, Source: "remote",
	})
}

func closeNativeRepository(repo *store.Repository) error {
	if repo == nil {
		return nil
	}

	if err := repo.Close(); err != nil {
		return oops.In("remote").Code("remote_native_failed").With("operation", "close_native_repository").Wrap(err)
	}

	return nil
}
