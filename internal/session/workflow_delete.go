//go:build linux || darwin

package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	rewriteIDBytes = 16

	catalogOperationActive = "active"
	catalogRewritePending  = "pending"
	catalogAttemptPending  = "pending"

	barrierOperationPin   = "operation_pin"
	barrierPendingRewrite = "pending_rewrite"
)

type deleteStep struct {
	Generation string
	SessionIDs []string
}

type deleteLiveTarget struct {
	session   gotmux.Session
	serverPID int
}

func (w *Workflow) DeleteSession(ctx context.Context, name string, force bool) (bool, bool, error) {
	out, err := w.DeleteSessionEverywhere(ctx, name, force)

	return out.LiveKilled, out.SavedDeleted, err
}

func (w *Workflow) DeleteSavedSessionTarget(ctx context.Context, generationID, sessionID string) (string, error) {
	if w.server == nil {
		return "", oops.In("session").Code("endpoint_required").Wrap(errServerRequired)
	}

	if w.repo == nil {
		return "", oops.In("session").Code("repository_unavailable").Wrap(errRepositoryRequired)
	}

	lease, err := w.acquireDeleteLease(ctx)
	if err != nil {
		return "", w.fail(ctx, "delete_saved", err)
	}

	rewriteID, deleteErr := w.deleteSavedSessionTarget(ctx, generationID, sessionID)

	closeErr := closeLease(lease)
	if err := errors.Join(deleteErr, closeErr); err != nil {
		return rewriteID, w.fail(ctx, "delete_saved", err)
	}

	zerolog.Ctx(ctx).Info().
		Str("component", "session").
		Str("operation", "delete_saved").
		Str("status", "ok").
		Msg("saved session deleted")

	return rewriteID, nil
}

func manifestHasSessionID(manifest model.Manifest, sessionID string) bool {
	if sessionID == "" {
		return false
	}

	for _, saved := range manifest.Sessions {
		if saved.ID == sessionID {
			return true
		}
	}

	return false
}

func savedRewriteError(rewriteID string, err error) error {
	switch {
	case errors.Is(err, store.ErrUncertain):
		return oops.In("session").Code("publication_uncertain").With("operation", "remove_sessions").With("rewrite_id", rewriteID).Wrap(err)
	case errors.Is(err, store.ErrRecoveryRequired):
		return oops.In("session").Code("recovery_required").With("operation", "remove_sessions").With("rewrite_id", rewriteID).Wrap(err)
	default:
		return oops.In("session").Code("rewrite_failed").With("operation", "remove_sessions").Wrap(err)
	}
}

func initialDeleteOutcome() DeleteOutcome {
	return DeleteOutcome{
		LivePresent:          false,
		LiveKilled:           false,
		LiveSessionID:        "",
		SavedFound:           false,
		SavedDeleted:         false,
		SavedGenerations:     0,
		SavedSessions:        0,
		PreservedGenerations: nil,
		PendingRewrites:      nil,
		HistoryUpdated:       false,
	}
}

func logDeleteSuccess(ctx context.Context, out DeleteOutcome) {
	zerolog.Ctx(ctx).Info().
		Str("component", "session").
		Str("operation", "delete").
		Str("status", "ok").
		Bool("live_killed", out.LiveKilled).
		Bool("saved_deleted", out.SavedDeleted).
		Int("saved_generations", out.SavedGenerations).
		Int("saved_sessions", out.SavedSessions).
		Msg("session deleted")
}

func (w *Workflow) DeleteSessionEverywhere(ctx context.Context, name string, force bool) (DeleteOutcome, error) {
	return w.DeleteSessionTargetEverywhere(ctx, name, "", force)
}

func (w *Workflow) DeleteSessionTargetEverywhere(
	ctx context.Context,
	name string,
	expectedSessionID string,
	force bool,
) (DeleteOutcome, error) {
	out := initialDeleteOutcome()

	if w.server == nil {
		return out, oops.In("session").Code("endpoint_required").Wrap(errServerRequired)
	}

	lease, leaseErr := w.acquireDeleteLease(ctx)
	if leaseErr != nil {
		return out, w.fail(ctx, "delete", leaseErr)
	}

	endpointScope, scopeErr := model.EndpointScope(w.server.Endpoint().SocketPath)
	if scopeErr != nil {
		err := oops.In("session").Code("invalid_endpoint").With("operation", "scope_endpoint").Wrap(scopeErr)

		return out, w.fail(ctx, "delete", errors.Join(err, closeLease(lease)))
	}

	live, plan, prepErr := w.prepareDeleteTargetAndPlan(ctx, name, expectedSessionID, endpointScope, force, &out)
	if prepErr != nil {
		return out, w.fail(ctx, "delete", errors.Join(prepErr, closeLease(lease)))
	}

	if applyErr := w.applySavedDelete(ctx, plan, &out); applyErr != nil {
		return out, w.fail(ctx, "delete", errors.Join(applyErr, closeLease(lease)))
	}

	if killErr := w.revalidateAndDeleteLive(ctx, live, name, endpointScope, force, &out); killErr != nil {
		return out, w.fail(ctx, "delete", errors.Join(killErr, closeLease(lease)))
	}

	if histErr := w.deleteHistory(ctx, name); histErr != nil {
		return out, w.fail(ctx, "delete", errors.Join(oops.In("session").Code("history_failed").With("operation", "delete_history").Wrap(histErr), closeLease(lease)))
	}

	if closeErr := closeLease(lease); closeErr != nil {
		return out, w.fail(ctx, "delete", closeErr)
	}

	out.HistoryUpdated = true

	logDeleteSuccess(ctx, out)

	return out, nil
}

func (w *Workflow) deleteSavedSessionTarget(ctx context.Context, generationID, sessionID string) (string, error) {
	endpointScope, err := model.EndpointScope(w.server.Endpoint().SocketPath)
	if err != nil {
		return "", oops.In("session").Code("invalid_endpoint").With("operation", "scope_endpoint").Wrap(err)
	}

	manifest, err := w.repo.Read(ctx, generationID)
	if err != nil {
		return "", oops.In("session").Code("repository_unavailable").With("operation", "read_generation").Wrap(err)
	}

	if manifest.Source.EndpointScope == "" {
		return "", savedSessionScopeConflict("", "delete_saved")
	}

	if manifest.Source.EndpointScope != endpointScope {
		return "", oops.In("session").Code("saved_session_scope_conflict").With("operation", "delete_saved").Wrap(ErrSavedSessionScopeConflict)
	}

	if !manifestHasSessionID(manifest, sessionID) {
		return "", oops.In("session").Code("not_found").With("operation", "delete_saved").Wrap(ErrSessionNotFound)
	}

	rewriteID, err := newRewriteID()
	if err != nil {
		return "", err
	}

	result, err := w.repo.RemoveSessions(ctx, rewriteID, generationID, []string{sessionID})
	if err == nil {
		return result.ID, nil
	}

	if errors.Is(err, store.ErrRecoveryRequired) || errors.Is(err, store.ErrUncertain) {
		return rewriteID, savedRewriteError(rewriteID, err)
	}

	return "", savedRewriteError("", err)
}

func (w *Workflow) prepareDeleteTargetAndPlan(
	ctx context.Context,
	name string,
	expectedSessionID string,
	endpointScope string,
	force bool,
	out *DeleteOutcome,
) (deleteLiveTarget, []deleteStep, error) {
	live, liveErr := w.prepareLiveDelete(ctx, name, force, out)
	if liveErr != nil {
		return deleteLiveTarget{}, nil, liveErr
	}

	if expectedSessionID != "" && (!out.LivePresent || out.LiveSessionID != expectedSessionID) {
		return deleteLiveTarget{}, nil, oops.In("session").Code("target_replaced").With("operation", "preflight_session").Wrap(ErrSessionReplaced)
	}

	plan, planErr := w.planSavedDelete(ctx, name, endpointScope, out)
	if planErr != nil {
		return deleteLiveTarget{}, nil, planErr
	}

	if !out.LivePresent && !out.SavedFound {
		return deleteLiveTarget{}, nil, oops.In("session").Code("not_found").Wrapf(ErrSessionNotFound, "session %q not found", name)
	}

	return live, plan, nil
}

func (w *Workflow) revalidateAndDeleteLive(ctx context.Context, live deleteLiveTarget, name string, endpointScope string, force bool, out *DeleteOutcome) error {
	if revalErr := w.revalidateSavedDeletions(ctx, name, endpointScope); revalErr != nil {
		return revalErr
	}

	if targetErr := w.revalidateLiveTarget(ctx, live, name, out.LivePresent); targetErr != nil {
		return targetErr
	}

	if attachErr := revalidateLiveNotAttached(ctx, live.session, force, out.LivePresent); attachErr != nil {
		return attachErr
	}

	if out.LivePresent {
		if killErr := live.session.Kill(ctx); killErr != nil {
			return oops.In("session").Code("tmux_failed").With("operation", "kill_session").Wrap(killErr)
		}

		out.LiveKilled = true
	}

	return nil
}

func (w *Workflow) prepareLiveDelete(ctx context.Context, name string, force bool, out *DeleteOutcome) (deleteLiveTarget, error) {
	sess, err := w.server.FindSession(ctx, name)
	if err != nil {
		if isAbsentSession(err) {
			return deleteLiveTarget{session: gotmux.Session{}, serverPID: 0}, nil
		}

		return deleteLiveTarget{}, lookupSessionError(err)
	}

	probe, err := w.server.Probe(ctx)
	if err != nil || probe.Identity.PID <= 0 {
		return deleteLiveTarget{}, oops.In("session").Code("tmux_failed").With("operation", "identify_server").Wrap(errors.Join(err, ErrSessionReplaced))
	}

	target := deleteLiveTarget{session: sess, serverPID: probe.Identity.PID}

	out.LivePresent = true
	out.LiveSessionID = string(sess.ID())

	if force {
		return target, nil
	}

	info, err := sess.Info(ctx)
	if err != nil {
		return deleteLiveTarget{}, oops.In("session").Code("tmux_failed").With("operation", "inspect_session").Wrap(err)
	}

	if info.Attached > 0 {
		return deleteLiveTarget{}, oops.In("session").Code("session_attached").Wrapf(ErrSessionAttached, "session %q is attached", name)
	}

	return target, nil
}

func (w *Workflow) prepareRequiredLiveTarget(ctx context.Context, name, expectedSessionID string) (deleteLiveTarget, error) {
	sess, err := w.server.FindSession(ctx, name)
	if err != nil {
		return deleteLiveTarget{}, lookupSessionError(err)
	}

	probe, err := w.server.Probe(ctx)
	if err != nil || probe.Identity.PID <= 0 {
		return deleteLiveTarget{}, oops.In("session").Code("tmux_failed").With("operation", "identify_server").Wrap(errors.Join(err, ErrSessionReplaced))
	}

	if expectedSessionID != "" && string(sess.ID()) != expectedSessionID {
		return deleteLiveTarget{}, oops.In("session").Code("target_replaced").With("operation", "preflight_session").Wrap(ErrSessionReplaced)
	}

	return deleteLiveTarget{session: sess, serverPID: probe.Identity.PID}, nil
}

func (w *Workflow) planSavedDelete(ctx context.Context, name, endpointScope string, out *DeleteOutcome) ([]deleteStep, error) {
	if w.repo == nil {
		return nil, nil
	}

	catalog, err := w.repo.Inspect(ctx)
	if err != nil {
		return nil, oops.In("session").Code("repository_unavailable").With("operation", "inspect_repository").Wrap(err)
	}

	if _, pending := pendingAttempt(catalog); pending {
		return nil, oops.In("session").
			Code("recovery_required").
			With("operation", "preflight_repository").
			Wrapf(store.ErrRecoveryRequired, "a save attempt is still pending")
	}

	steps := make([]deleteStep, 0, len(catalog.Generations))

	for _, generation := range catalog.Generations {
		manifest, err := w.repo.Read(ctx, generation.ID)
		if err != nil {
			return nil, oops.In("session").Code("repository_unavailable").With("operation", "read_generation").Wrap(err)
		}

		sessionIDs := manifestSessionIDs(manifest, name)
		if len(sessionIDs) == 0 {
			continue
		}

		if manifest.Source.EndpointScope == "" {
			out.SavedFound = true

			return nil, savedSessionScopeConflict(name, "preflight_saved_delete")
		}

		if manifest.Source.EndpointScope != endpointScope {
			continue
		}

		out.SavedFound = true

		if barrier := generationBarrier(catalog, generation.ID); barrier != "" {
			out.PreservedGenerations = append(out.PreservedGenerations, generation.ID)

			return nil, oops.In("session").
				Code("saved_generations_protected").
				With("operation", "preflight_saved_delete").
				With("barrier", barrier).
				Wrapf(ErrSavedGenerationsProtected, "saved generation containing session %q is protected", name)
		}

		steps = append(steps, deleteStep{Generation: generation.ID, SessionIDs: sessionIDs})
	}

	return steps, nil
}

func (w *Workflow) applySavedDelete(ctx context.Context, plan []deleteStep, out *DeleteOutcome) error {
	for _, step := range plan {
		rewriteID, err := newRewriteID()
		if err != nil {
			return oops.In("session").Code("rewrite_failed").With("operation", "generate_rewrite_id").Wrap(err)
		}

		result, err := w.repo.RemoveSessions(ctx, rewriteID, step.Generation, step.SessionIDs)
		if err != nil {
			if errors.Is(err, store.ErrRecoveryRequired) || errors.Is(err, store.ErrUncertain) {
				out.PendingRewrites = append(out.PendingRewrites, rewriteID)
			}

			if errors.Is(err, store.ErrUncertain) {
				return oops.In("session").Code("publication_uncertain").With("operation", "remove_sessions").With("rewrite_id", rewriteID).Wrap(err)
			}

			if errors.Is(err, store.ErrRecoveryRequired) {
				return oops.In("session").Code("recovery_required").With("operation", "remove_sessions").With("rewrite_id", rewriteID).Wrap(err)
			}

			return oops.In("session").Code("rewrite_failed").With("operation", "remove_sessions").Wrap(err)
		}

		out.SavedDeleted = true
		out.SavedGenerations++
		out.SavedSessions += len(result.Removed)
	}

	return nil
}

func closeLease(lease *integration.Lease) error {
	if lease == nil {
		return nil
	}

	if err := lease.Close(); err != nil {
		return oops.In("session").Code("lease_failed").With("operation", "close_lease").Wrap(err)
	}

	return nil
}

func (w *Workflow) acquireDeleteLease(ctx context.Context) (*integration.Lease, error) {
	if w.server == nil {
		return nil, oops.In("session").Code("endpoint_required").With("operation", "acquire_lease").Wrap(errServerRequired)
	}

	if w.leaseRoot == "" {
		return nil, oops.In("session").Code("lease_failed").With("operation", "acquire_lease").Wrap(integration.ErrLeaseRoot)
	}

	lease, err := integration.AcquireLease(ctx, w.leaseRoot, w.server.Endpoint().SocketPath)
	if err != nil {
		return nil, oops.In("session").Code("lease_failed").With("operation", "acquire_lease").Wrap(err)
	}

	return lease, nil
}

func (w *Workflow) revalidateLiveTarget(ctx context.Context, target deleteLiveTarget, name string, livePresent bool) error {
	if !livePresent {
		return nil
	}

	probe, err := w.server.Probe(ctx)
	if err != nil {
		return oops.In("session").Code("tmux_failed").With("operation", "revalidate_server").Wrap(err)
	}

	if !sameLiveDeleteTarget(target.serverPID, probe.Identity.PID, target.session.ID(), target.session.ID()) {
		return oops.In("session").Code("target_replaced").With("operation", "revalidate_server").Wrap(ErrSessionReplaced)
	}

	current, err := w.server.FindSession(ctx, name)
	if err != nil {
		if isAbsentSession(err) {
			return oops.In("session").Code("target_replaced").With("operation", "revalidate_session").Wrap(ErrSessionReplaced)
		}

		return lookupSessionError(err)
	}

	if !sameLiveDeleteTarget(target.serverPID, probe.Identity.PID, target.session.ID(), current.ID()) {
		return oops.In("session").Code("target_replaced").With("operation", "revalidate_session").Wrap(ErrSessionReplaced)
	}

	return nil
}

func sameLiveDeleteTarget(initialServerPID, currentServerPID int, initialSessionID, currentSessionID gotmux.SessionID) bool {
	return initialServerPID > 0 && initialServerPID == currentServerPID &&
		initialSessionID != "" && initialSessionID == currentSessionID
}

func (w *Workflow) revalidateSavedDeletions(ctx context.Context, name, endpointScope string) error {
	if w.repo == nil {
		return nil
	}

	catalogAfter, catErr := w.repo.Inspect(ctx)
	if catErr != nil {
		return oops.In("session").Code("repository_unavailable").With("operation", "revalidate_catalog").Wrap(catErr)
	}

	for _, gen := range catalogAfter.Generations {
		manifest, readErr := w.repo.Read(ctx, gen.ID)
		if readErr != nil {
			return oops.In("session").Code("repository_unavailable").With("operation", "revalidate_generation").Wrap(readErr)
		}

		if len(manifestSessionIDs(manifest, name)) == 0 {
			continue
		}

		if manifest.Source.EndpointScope == "" {
			return savedSessionScopeConflict(name, "revalidate_saved_delete")
		}

		if manifest.Source.EndpointScope == endpointScope {
			return oops.In("session").Code("saved_generations_protected").Wrapf(ErrSavedGenerationsProtected, "saved generations still contain session %q after concurrent publication", name)
		}
	}

	return nil
}

func savedSessionScopeConflict(name, operation string) error {
	if name == "" {
		return oops.In("session").
			Code("saved_session_scope_conflict").
			With("operation", operation).
			Wrap(ErrSavedSessionScopeConflict)
	}

	return oops.In("session").
		Code("saved_session_scope_conflict").
		With("operation", operation).
		Wrapf(ErrSavedSessionScopeConflict, "saved session %q has no endpoint scope", name)
}

func revalidateLiveNotAttached(ctx context.Context, live gotmux.Session, force, livePresent bool) error {
	if !livePresent || force {
		return nil
	}

	liveInfo, infoErr := live.Info(ctx)
	if infoErr != nil {
		return oops.In("session").Code("tmux_failed").With("operation", "inspect_session").Wrap(infoErr)
	}

	if liveInfo.Attached > 0 {
		return oops.In("session").Code("session_attached").Wrap(ErrSessionAttached)
	}

	return nil
}

func (w *Workflow) deleteHistory(ctx context.Context, name string) error {
	ident, ok := w.localIdentity(name)
	if !ok {
		return nil
	}

	_, err := UpdateState(ctx, w.historyPath, func(st *State) {
		st.DeleteSession(ident)
	})

	return err
}

func manifestSessionIDs(manifest model.Manifest, name string) []string {
	var ids []string

	for _, stored := range manifest.Sessions {
		if string(stored.Name) == name {
			ids = append(ids, stored.ID)
		}
	}

	return ids
}

func generationBarrier(catalog store.Catalog, generation string) string {
	for _, operation := range catalog.Operations {
		if operation.State == catalogOperationActive && operation.Source == generation {
			return barrierOperationPin
		}
	}

	for _, rewrite := range catalog.Rewrites {
		if rewrite.State == catalogRewritePending && rewrite.Source == generation {
			return barrierPendingRewrite
		}
	}

	return ""
}

func pendingAttempt(catalog store.Catalog) (store.Attempt, bool) {
	for _, attempt := range catalog.Attempts {
		if attempt.State == catalogAttemptPending {
			return attempt, true
		}
	}

	return store.Attempt{ID: "", Sequence: 0, State: "", Reason: "", ManifestSHA256: ""}, false
}

func newRewriteID() (string, error) {
	value := make([]byte, rewriteIDBytes)
	if _, err := rand.Read(value); err != nil {
		return "", oops.In("session").Code("rewrite_failed").With("operation", "generate_rewrite_id").Wrap(err)
	}

	return hex.EncodeToString(value), nil
}
