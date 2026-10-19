//go:build linux || darwin

package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	saveStatusComplete   = "complete"
	saveStatusBestEffort = "best_effort"
)

var (
	ErrPartialSnapshot   = errors.New("parity-incomplete save")
	ErrPartialRestore    = errors.New("restore incomplete: sessions were skipped or not verified")
	ErrRepositoryMissing = errors.New("snapshot repository does not exist")
	errMixedServers      = errors.New("selected sessions belong to different tmux servers")
)

// engineLoggedError marks an outcome the snapshot engine already logged, so the caller's boundary
// does not log it again.
type engineLoggedError struct{ error }

type paneLabel struct {
	Location string
	Program  string
}

func (e engineLoggedError) Unwrap() error { return e.error }

func (engineLoggedError) OutcomeLogged() bool { return true }

func (local *Local) registerSnapshotHandlers() {
	handle(local, action.SnapshotSave, local.saveSnapshot)
	handle(local, action.SnapshotRestore, local.restoreSnapshot)
}

func (local *Local) saveSnapshot(ctx context.Context, _ action.Caller, input action.SaveSnapshot) (action.SavedSnapshot, error) {
	result := action.SavedSnapshot{Attempt: "", Generation: "", Status: "failed"}

	selected, err := local.saveSelection(ctx, input.Sessions)
	if err != nil {
		return result, err
	}

	environment, err := local.saveEnvironment(input)
	if err != nil {
		return result, err
	}

	if err := local.ensureLeaseRoot(); err != nil {
		return result, err
	}

	repo, err := local.repository.Ensure(ctx)
	if err != nil {
		return result, err
	}

	saved, err := engine.SaveObservation(ctx, repo, local.server, selected, engine.SaveConfig{
		LeaseRoot:              local.leaseRoot,
		ProcessEnrichment:      input.ProcessEnrichment,
		ProcessEnvironmentKeys: environment,
		AgentSource:            local.agentSource,
		Profile:                engine.RecoveryPractical,
	})
	result = action.SavedSnapshot{Attempt: saved.Attempt, Generation: saved.Generation, Status: saved.Status}

	if err != nil {
		return result, oops.In("app").With("operation", "save_snapshot").Wrap(engineLoggedError{err})
	}

	if saved.Status != saveStatusComplete && saved.Status != saveStatusBestEffort {
		return result, action.Wrap(action.CodePartial, engineLoggedError{ErrPartialSnapshot})
	}

	return result, nil
}

func (local *Local) saveSelection(ctx context.Context, refs []action.SessionRef) ([]gotmux.SessionID, error) {
	if len(refs) == 0 {
		return nil, action.Wrap(action.CodeInvalidArgument, capture.ErrSelection)
	}

	selected := make([]gotmux.SessionID, 0, len(refs))

	for _, ref := range refs {
		if ref.Socket != refs[0].Socket {
			return nil, action.Wrap(action.CodeInvalidArgument, errMixedServers)
		}

		if _, err := local.liveTarget(ctx, ref); err != nil {
			return nil, err
		}

		if !slices.Contains(selected, gotmux.SessionID(ref.ID)) {
			selected = append(selected, gotmux.SessionID(ref.ID))
		}
	}

	return selected, nil
}

func (local *Local) saveEnvironment(input action.SaveSnapshot) ([]string, error) {
	keys := local.cfg.Snapshot.ProcessEnvironment
	if input.EnvironmentSet {
		keys = input.Environment
	}

	if err := process.ValidateEnvironmentKeys(keys); err != nil {
		return nil, action.Wrap(action.CodeInvalidArgument, err)
	}

	if !input.ProcessEnrichment && !input.EnvironmentSet {
		return nil, nil
	}

	return config.FilterProcessEnvironmentKeys(keys), nil
}

func (local *Local) restoreSnapshot(ctx context.Context, _ action.Caller, input action.RestoreSnapshot) (action.RestoredSnapshot, error) {
	var result engine.RestoreResult

	repo, found, err := local.useRepository(ctx, false)
	if err != nil {
		return restoredSnapshot(result, nil), err
	}

	if !found {
		return restoredSnapshot(result, nil), action.Wrap(action.CodeNotFound, ErrRepositoryMissing)
	}

	generation, err := restoreGeneration(ctx, repo, input.Generation)
	if err != nil {
		return restoredSnapshot(result, nil), err
	}

	if err := local.ensureLeaseRoot(); err != nil {
		return restoredSnapshot(result, nil), err
	}

	launches, err := TaskRestoreLaunches(local.taskLedger, local.cfg.Tasks)
	if err != nil {
		return restoredSnapshot(result, nil), err
	}

	result, err = engine.Restore(ctx, repo, local.server, engine.RestoreConfig{
		LeaseRoot:         local.leaseRoot,
		Generation:        generation,
		Sessions:          slices.Clone(input.Sessions),
		OperationID:       input.Operation,
		LaunchProcesses:   input.LaunchProcesses,
		MissingWorkingDir: missingWorkingDir(input.AllowMissingDir),
		OnCollision:       engine.CollisionPolicy(input.OnCollision),
		Profile:           engine.RecoveryPractical,
		Launches:          launches,
	})

	output := restoredSnapshot(result, readManifest(ctx, repo, generation))
	output.Sessions = local.restoredRefs(ctx, result.SessionsRestored)

	switch {
	case err != nil:
		return output, oops.In("app").With("operation", "restore_snapshot").Wrap(engineLoggedError{err})
	case result.Uncertain:
		return output, action.Wrap(action.CodeUncertain, engineLoggedError{restore.ErrExecutionUncertain})
	case !result.Verified || (result.Status != saveStatusComplete && result.Status != saveStatusBestEffort):
		return output, action.Wrap(action.CodePartial, engineLoggedError{ErrPartialRestore})
	default:
		return output, nil
	}
}

func restoreGeneration(ctx context.Context, repo *store.Repository, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}

	current, _, err := repo.ReadPracticalCurrent(ctx)
	if err != nil {
		return "", oops.In("app").With("operation", "read_current_generation").Wrap(err)
	}

	return current.ID, nil
}

func readManifest(ctx context.Context, repo *store.Repository, generation string) *model.Manifest {
	manifest, err := repo.Read(ctx, generation)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Str("component", "snapshot").Str("operation", "label_restore").
			Str("status", "degraded").Str("reason", "manifest_read_failed").Msg("restored sessions and panes are reported without names")

		return nil
	}

	return &manifest
}

func (local *Local) restoredRefs(ctx context.Context, ids []string) []action.SessionRef {
	refs := make([]action.SessionRef, 0, len(ids))
	if len(ids) == 0 {
		return refs
	}

	rows, err := local.liveSessions(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Str("component", "snapshot").Str("operation", "list_restored_sessions").
			Str("status", "degraded").Str("reason", "session_list_failed").Msg("restored sessions are reported by tmux ID only")
	}

	for _, id := range ids {
		index := slices.IndexFunc(rows, func(row session.Session) bool { return row.ID == id })
		if index < 0 {
			refs = append(refs, action.SessionRef{
				Kind: session.KindLive, ID: id, Name: "", Socket: "", ServerPID: 0,
				ServerStartedAt: time.Time{}, CreatedAt: time.Time{}, Generation: "",
			})

			continue
		}

		refs = append(refs, action.RefOf(rows[index]))
	}

	return refs
}

func restoredSnapshot(result engine.RestoreResult, manifest *model.Manifest) action.RestoredSnapshot {
	panes := paneLabels(manifest)

	output := action.RestoredSnapshot{
		OperationID:         result.OperationID,
		Generation:          result.Generation,
		Status:              result.Status,
		Reason:              result.Reason,
		Sessions:            []action.SessionRef{},
		Panes:               make([]action.RestoredPane, 0, len(result.Panes)),
		Verified:            result.Verified,
		Uncertain:           result.Uncertain,
		MissingRequirements: make([]action.Requirement, 0, len(result.MissingRequirements)),
		SessionsSkipped:     make([]action.SkippedSession, 0, len(result.SessionsSkipped)),
		Advisories:          mismatches(result.Advisories),
		Mismatches:          mismatches(result.Mismatches),
	}

	for _, pane := range result.Panes {
		label := panes[pane.PaneID]
		if label.Location == "" {
			label.Location = pane.PaneID
		}

		output.Panes = append(output.Panes, action.RestoredPane{
			PaneID: pane.PaneID, RuntimeID: pane.RuntimeID, Outcome: pane.Outcome, Reason: pane.Reason,
			AppVerified: pane.AppVerified, CWDFallback: pane.CWDFallback, VerificationReason: pane.VerificationReason,
			Location: label.Location, Program: label.Program,
		})
	}

	for _, missing := range result.MissingRequirements {
		output.MissingRequirements = append(output.MissingRequirements, action.Requirement{
			Domain: missing.Domain, Status: missing.Status, Reason: string(missing.Reason),
		})
	}

	for _, skipped := range result.SessionsSkipped {
		output.SessionsSkipped = append(output.SessionsSkipped, action.SkippedSession{ID: skipped.ID, Name: skipped.Name, Reason: skipped.Reason})
	}

	return output
}

func paneLabels(manifest *model.Manifest) map[string]paneLabel {
	labels := map[string]paneLabel{}
	if manifest == nil {
		return labels
	}

	names := make(map[string]string, len(manifest.Sessions))
	for _, saved := range manifest.Sessions {
		names[saved.ID] = string(saved.Name)
	}

	links := make(map[string]model.Link, len(manifest.Links))
	for _, link := range manifest.Links {
		if _, seen := links[link.Window]; !seen {
			links[link.Window] = link
		}
	}

	for _, pane := range manifest.Panes {
		label := paneLabel{Location: pane.ID, Program: ""}
		if len(pane.Launch.Executable) > 0 {
			label.Program = filepath.Base(string(pane.Launch.Executable))
		}

		if link, ok := links[pane.Window]; ok {
			label.Location = fmt.Sprintf("%s:%d.%d", names[link.Session], link.Index, pane.Index)
		}

		labels[pane.ID] = label
	}

	return labels
}

func mismatches(values []restore.VerificationMismatch) []action.Mismatch {
	converted := make([]action.Mismatch, 0, len(values))
	for _, value := range values {
		converted = append(converted, action.Mismatch{Domain: value.Domain, Entity: value.Entity, Reason: value.Reason})
	}

	return converted
}

func missingWorkingDir(allowMissing bool) restore.MissingWorkingDir {
	if allowMissing {
		return restore.MissingWorkingDirLaunch
	}

	return restore.MissingWorkingDirFail
}
