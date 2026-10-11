//go:build linux || darwin

package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/errorstep"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	maxRestoreTargetBytes = 4096
	journalCleanupTimeout = 15 * time.Second
	restoreTargetPrefix   = "restore-v1\n"
)

var restoreFailureClasses = []struct {
	code   string
	reason string
	causes []error
}{
	{"endpoint_required", "admission_failed", []error{ErrLeaseRootRequired, tmuxserver.ErrEndpoint, ErrRepositoryRequired}},
	{"operation_uncertain", "effect_outcome_uncertain", []error{restore.ErrExecutionUncertain}},
	{"operation_uncertain", "prior_effect_uncertain", []error{restore.ErrPriorUncertainty}},
	{"operation_uncertain", "prior_effect_conflicted", []error{restore.ErrPriorConflict}},
	{"restore_failed", "journal_unavailable", []error{restore.ErrJournalRequired, restore.ErrJournalIncomplete}},
	{"restore_conflict", "daemon_identity_changed", []error{tmuxserver.ErrDaemonIdentityChanged}},
	{"restore_no_server", "no_server", []error{ErrNoServerRunning, gotmux.ErrNoServer}},
	{"restore_failed", "daemon_identity_unavailable", []error{tmuxserver.ErrDaemonIdentityUnavailable}},
	{"operation_canceled", "canceled", []error{context.Canceled}},
	{"operation_timed_out", "timeout", []error{context.DeadlineExceeded}},
	{"restore_conflict", "collision", []error{restore.ErrSessionCollision, restore.ErrGroupCollision}},
	{"restore_conflict", "working_directory_missing", []error{restore.ErrWorkingDirNotFound}},
	{"snapshot_invalid", "invalid_snapshot", []error{store.ErrCorrupt, model.ErrInvalid}},
	{"snapshot_version_unsupported", "version_unsupported", []error{model.ErrVersion}},
}

type restoreOutcomeError struct{ error }

type actionLedger struct {
	repository *store.Repository
	operation  string
	states     map[string]store.Action
}

type stepJournal struct {
	ledger *actionLedger
}

type verificationError struct {
	report restore.VerificationReport
}

type restoreDaemonIdentity struct {
	Server gotmux.ServerIdentity
	Birth  string
}

type RestoreConfig struct {
	LeaseRoot         string
	Generation        string
	Sessions          []string
	OperationID       string
	LaunchProcesses   bool
	MissingWorkingDir restore.MissingWorkingDir
	OnCollision       CollisionPolicy
	Profile           RecoveryProfile
	Launches          LaunchOverrides
}

type RestoreResult struct {
	OperationID         string                         `json:"operationId"`
	Generation          string                         `json:"generation"`
	Status              string                         `json:"status"`
	Reason              string                         `json:"reason"`
	SessionsRestored    []string                       `json:"sessionsRestored"`
	Verified            bool                           `json:"verified"`
	Uncertain           bool                           `json:"uncertain"`
	MissingRequirements []restore.MissingRequirement   `json:"missingRequirements"`
	Panes               []restore.PaneResult           `json:"panes,omitempty"`
	SessionsSkipped     []SkippedSession               `json:"sessionsSkipped"`
	Advisories          []restore.VerificationMismatch `json:"advisories"`
	Mismatches          []restore.VerificationMismatch `json:"mismatches"`
}

type ActionStatus struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	RuntimeID string `json:"runtimeId"`
}

type OperationStatus struct {
	OperationID string         `json:"operationId"`
	Source      string         `json:"source"`
	Target      string         `json:"target"`
	State       string         `json:"state"`
	Actions     []ActionStatus `json:"actions"`
	Uncertain   []string       `json:"uncertain"`
	Settled     bool           `json:"settled"`
}

type restoredPaneTally struct {
	resumed, restarted, shell, unrecovered, directoryFallback int
}

type restoreStartedKey struct{}

func (i restoreDaemonIdentity) Equal(other restoreDaemonIdentity) bool {
	return i.Server.Equal(other.Server) && i.Birth == other.Birth
}

func (e *verificationError) Error() string {
	codes := make([]string, 0, len(e.report.Mismatches))
	for _, mismatch := range e.report.Mismatches {
		codes = append(codes, mismatch.Domain+"/"+mismatch.Entity+"/"+mismatch.Reason)
	}

	return fmt.Sprintf("independent verification failed with %d mismatches: %s", len(codes), strings.Join(codes, ", "))
}

func Restore(ctx context.Context, repository *store.Repository, server *gotmux.Server, cfg RestoreConfig) (RestoreResult, error) {
	result := RestoreResult{
		OperationID:         cfg.OperationID,
		Generation:          "",
		Status:              "failed",
		Reason:              "admission_failed",
		SessionsRestored:    nil,
		Verified:            false,
		Uncertain:           false,
		MissingRequirements: nil,
		Panes:               nil,
		SessionsSkipped:     nil,
		Advisories:          nil,
		Mismatches:          nil,
	}

	ctx = context.WithValue(ctx, restoreStartedKey{}, time.Now())

	if cfg.LeaseRoot == "" {
		return result, restoreFailure(ctx, "admission", ErrLeaseRootRequired)
	}

	if server == nil || server.Endpoint().SocketPath == "" {
		return result, restoreFailure(ctx, "admission", tmuxserver.ErrEndpoint)
	}

	if repository == nil {
		return result, restoreFailure(ctx, "admission", ErrRepositoryRequired)
	}

	zerolog.Ctx(ctx).Info().
		Str("component", "snapshot").
		Str("operation", "restore").
		Str("status", "started").
		Msg("snapshot restore started")

	if _, probeErr := server.Probe(ctx); errors.Is(probeErr, gotmux.ErrNoServer) {
		result.Reason = "no_server"

		return result, restoreFailure(ctx, "admission", ErrNoServerRunning)
	}

	lease, err := tmuxserver.AcquireLease(ctx, cfg.LeaseRoot, server.Endpoint().SocketPath)
	if err != nil {
		return result, restoreFailure(ctx, "lease", err)
	}

	return executeRestore(ctx, repository, server, lease, cfg)
}

func ReconcileRestore(ctx context.Context, repository *store.Repository, operationID string) (OperationStatus, error) {
	if repository == nil {
		return OperationStatus{}, oops.In("snapshot").Code("repository_required").With("operation", "reconcile_restore").Wrap(ErrRepositoryRequired)
	}

	op, err := repository.ReconcileOperation(ctx, operationID)
	if err != nil {
		return OperationStatus{}, oops.In("snapshot").Code("reconcile_failed").With("operation", "reconcile_restore").Wrap(err)
	}

	if err := validateRestoreOperation(op); err != nil {
		return OperationStatus{}, err
	}

	return operationStatus(op), nil
}

// AbandonRestore explicitly abandons an active restore operation and releases its recovery
// source pin. Existing tmux objects are retained and nothing is rolled back.
func AbandonRestore(ctx context.Context, repository *store.Repository, operationID string) (OperationStatus, error) {
	return abandonRestore(ctx, repository, operationID, "explicit_abandonment")
}

func abandonRestore(ctx context.Context, repository *store.Repository, operationID, reason string) (OperationStatus, error) {
	if repository == nil {
		return OperationStatus{}, oops.In("snapshot").Code("repository_required").With("operation", "abandon_restore").Wrap(ErrRepositoryRequired)
	}

	op, err := repository.ReconcileOperation(ctx, operationID)
	if err != nil {
		return OperationStatus{}, oops.In("snapshot").Code("reconcile_failed").With("operation", "abandon_restore").Wrap(err)
	}

	if err := validateRestoreOperation(op); err != nil {
		return OperationStatus{}, err
	}

	if op.State != "active" {
		return operationStatus(op), oops.In("snapshot.store").
			Code("repository_conflict").
			With("operation", "abandon_restore", "reason", "operation_not_active").
			Wrap(store.ErrConflict)
	}

	if err := repository.Finish(ctx, operationID, true); err != nil {
		return operationStatus(op), oops.In("snapshot.store").
			Code("repository_conflict").
			With("operation", "abandon_restore").
			Wrap(err)
	}

	status := operationStatus(op)
	status.State = "abandoned"

	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "restore").
		Str("status", "abandoned").
		Str("reason", reason).
		Int("uncertain", len(status.Uncertain)).
		Msg("snapshot restore abandoned")

	return status, nil
}

func validateRestoreOperation(op store.Operation) error {
	if !strings.HasPrefix(string(op.Target), restoreTargetPrefix) {
		return oops.In("snapshot.store").
			Code("repository_conflict").
			With("operation", "validate_restore_operation", "reason", "operation_kind_mismatch").
			Wrap(store.ErrConflict)
	}

	var importAction, verifyAction bool

	for _, action := range op.Actions {
		switch action.ID {
		case restore.ImportStateActionID:
			importAction = action.Kind == restore.KindConfigure
		case restore.VerifyActionID:
			verifyAction = action.Kind == restore.KindVerify
		}
	}

	if importAction && verifyAction {
		return nil
	}

	return oops.In("snapshot.store").
		Code("repository_conflict").
		With("operation", "validate_restore_operation", "reason", "operation_kind_mismatch").
		Wrap(store.ErrConflict)
}

func readGeneration(ctx context.Context, repository *store.Repository, genID string) (string, model.Manifest, error) {
	if genID != "" {
		manifest, err := repository.Read(ctx, genID)
		if err != nil {
			return genID, model.Manifest{}, fmt.Errorf("read generation: %w", err)
		}

		return genID, manifest, nil
	}

	gen, manifest, err := repository.ReadCurrent(ctx)
	if err != nil {
		return "", model.Manifest{}, fmt.Errorf("read current generation: %w", err)
	}

	return gen.ID, manifest, nil
}

func buildRestoreActions(specs []restore.ActionSpec) []store.Action {
	actions := make([]store.Action, 0, len(specs))
	for _, spec := range specs {
		actions = append(actions, store.Action{
			ID:        spec.ID,
			Kind:      spec.Kind,
			State:     "planned",
			RuntimeID: nil,
		})
	}

	return actions
}

func executeRestore(
	ctx context.Context,
	repository *store.Repository,
	server *gotmux.Server,
	lease *tmuxserver.Lease,
	cfg RestoreConfig,
) (RestoreResult, error) {
	result := RestoreResult{
		OperationID:         cfg.OperationID,
		Generation:          "",
		Status:              "failed",
		Reason:              "execution_failed",
		SessionsRestored:    nil,
		Verified:            false,
		Uncertain:           false,
		MissingRequirements: nil,
		Panes:               nil,
		SessionsSkipped:     nil,
		Advisories:          nil,
		Mismatches:          nil,
	}

	plan, override, generation, skipped, err := prepareRestorePlan(ctx, repository, server, cfg)
	result.Generation = generation
	result.SessionsSkipped = skipped

	if err != nil {
		result.Reason = planFailureReason(result.Reason, err)

		return result, closeWith(ctx, lease, "plan", err)
	}

	daemon, op, ledger, admissionPhase, err := establishRestoreIntent(ctx, repository, server, cfg, generation, plan, override)
	if op.ID != "" {
		result.OperationID = op.ID
	}

	if err != nil {
		return result, closeWith(ctx, lease, admissionPhase, err)
	}

	if inspected, done, inspectErr := reinspectVerifiedRestore(ctx, repository, server, lease, daemon, op.ID, cfg, plan, ledger, &result); done {
		return inspected, inspectErr
	}

	report, err := executeRestorePlan(ctx, server, daemon, cfg, plan, ledger)
	result.SessionsRestored = report.SessionsCreated
	result.Panes = slices.Clone(report.Panes)

	if err != nil {
		result.Uncertain = isUncertain(err)
		if result.Uncertain {
			result.Status = "uncertain"
			result.Reason = "effect_outcome_uncertain"
		}

		result.MissingRequirements = mergeRequirements(plan.MissingRequirements, report.Missing)

		return result, closeWith(ctx, lease, "execute", err)
	}

	result.MissingRequirements = mergeRequirements(plan.MissingRequirements, report.Missing)

	verification, scopedMissing, err := applyAndVerify(
		ctx, server, daemon, plan, report.Receipts, ledger, len(report.Dispatched) > 0,
	)

	result.MissingRequirements = mergeRequirements(result.MissingRequirements, scopedMissing)
	result.Advisories = verification.Advisories

	if err != nil {
		result.Uncertain = isUncertain(err)
		if result.Uncertain {
			result.Status = "uncertain"
			result.Reason = "effect_outcome_uncertain"
		}

		return result, closeWith(ctx, lease, "import", err)
	}

	if !verification.Verified {
		result.Mismatches = verification.Mismatches

		return result, closeWith(ctx, lease, "verify", reportVerificationFailure(verification))
	}

	if err := markVerified(ctx, ledger, plan, verification); err != nil {
		return result, closeWith(ctx, lease, "finalize", err)
	}

	return finishRestore(ctx, repository, lease, op.ID, cfg.Profile, &result)
}

func establishRestoreIntent(
	ctx context.Context,
	repository *store.Repository,
	server *gotmux.Server,
	cfg RestoreConfig,
	generation string,
	plan restore.Plan,
	override launchOverride,
) (restoreDaemonIdentity, store.Operation, *actionLedger, string, error) {
	daemon, err := observeRestoreDaemonIdentity(ctx, server)
	if err != nil {
		return restoreDaemonIdentity{}, store.Operation{}, nil, "identity", err
	}

	op, ledger, err := admitRestore(ctx, repository, server, daemon, cfg, generation, plan, override.digest)
	if err != nil {
		return daemon, op, nil, "pin", err
	}

	if err := requireRestoreDaemonIdentity(ctx, server, daemon, false); err != nil {
		return daemon, op, ledger, "admission", err
	}

	if cfg.Launches != nil && len(plan.SourceGraph.Panes) > 0 {
		panes := make([]string, len(plan.SourceGraph.Panes))
		for index, pane := range plan.SourceGraph.Panes {
			panes[index] = pane.ID
		}

		slices.Sort(panes)

		if err := cfg.Launches.Commit(ctx, panes); err != nil {
			return daemon, op, ledger, "admission", fmt.Errorf("commit launch overrides: %w", err)
		}
	}

	return daemon, op, ledger, "", nil
}

func executeRestorePlan(
	ctx context.Context,
	server *gotmux.Server,
	daemon restoreDaemonIdentity,
	cfg RestoreConfig,
	plan restore.Plan,
	ledger *actionLedger,
) (restore.ExecutionReport, error) {
	if err := requireRestoreDaemonIdentity(ctx, server, daemon, false); err != nil {
		return restore.ExecutionReport{}, err
	}

	dispatchCandidates := restoreDispatchCandidates(plan, ledger)

	executor := restore.NewExecutor(server, readManifest(plan), restore.ExecuteConfig{
		LaunchProcesses:   cfg.LaunchProcesses,
		MissingWorkingDir: cfg.MissingWorkingDir,
	})

	report, err := executor.Execute(ctx, plan, &stepJournal{ledger: ledger})
	if err != nil {
		executeErr := fmt.Errorf("execute restore plan: %w", err)
		effectsPossible := executionMayHaveDispatched(report, dispatchCandidates) || isUncertain(err)

		return report, postRestoreDaemonIdentity(ctx, server, daemon, executeErr, effectsPossible)
	}

	if err := requireRestoreDaemonIdentity(ctx, server, daemon, true); err != nil {
		return report, err
	}

	return report, nil
}

func restoreDispatchCandidates(plan restore.Plan, ledger *actionLedger) map[string]bool {
	candidates := make(map[string]bool)

	for _, step := range plan.Steps {
		action, ok := ledger.states[restore.ActionID(step.ID())]
		if ok && (action.State == "planned" || action.State == "not-sent") {
			candidates[step.ID()] = true
		}
	}

	return candidates
}

func executionMayHaveDispatched(report restore.ExecutionReport, candidates map[string]bool) bool {
	if len(report.Dispatched) > 0 {
		return true
	}

	for step, receipt := range report.Receipts {
		if candidates[step] && receipt != "" {
			return true
		}
	}

	return false
}

func reinspectVerifiedRestore(
	ctx context.Context,
	repository *store.Repository,
	server *gotmux.Server,
	lease *tmuxserver.Lease,
	daemon restoreDaemonIdentity,
	operation string,
	cfg RestoreConfig,
	plan restore.Plan,
	ledger *actionLedger,
	result *RestoreResult,
) (RestoreResult, bool, error) {
	receipts, verified, err := verifiedOperationReceipts(plan, ledger)
	if err != nil {
		return *result, true, closeWith(ctx, lease, "reinspect", err)
	}

	if !verified {
		return *result, false, nil
	}

	result.SessionsRestored = restoredSessionReceipts(plan, receipts)
	result.MissingRequirements = mergeRequirements(plan.MissingRequirements, restore.ScopedStateRequirements(plan))

	verification, scopedMissing, err := applyAndVerify(ctx, server, daemon, plan, receipts, ledger, false)

	result.MissingRequirements = mergeRequirements(result.MissingRequirements, scopedMissing)
	if err != nil {
		result.Uncertain = isUncertain(err)
		if result.Uncertain {
			result.Status = "uncertain"
			result.Reason = "effect_outcome_uncertain"
		}

		return *result, true, closeWith(ctx, lease, "reinspect", err)
	}

	if !verification.Verified {
		result.Mismatches = verification.Mismatches

		return *result, true, closeWith(ctx, lease, "reinspect", reportVerificationFailure(verification))
	}

	result.Panes = restore.PanesFromVerifiedRuntime(plan, verification.RuntimePanes, cfg.LaunchProcesses)

	finished, err := finishRestore(ctx, repository, lease, operation, cfg.Profile, result)

	return finished, true, err
}

func verifiedOperationReceipts(plan restore.Plan, ledger *actionLedger) (map[string]string, bool, error) {
	receipts := make(map[string]string, len(plan.Steps))

	for _, step := range plan.Steps {
		action, err := ledger.state(restore.ActionID(step.ID()))
		if err != nil {
			return nil, false, err
		}

		if action.State != restore.StateVerified {
			return nil, false, nil
		}

		if len(action.RuntimeID) > 0 {
			receipts[step.ID()] = string(action.RuntimeID)
		}
	}

	for _, id := range []string{restore.ImportStateActionID, restore.VerifyActionID} {
		action, err := ledger.state(id)
		if err != nil {
			return nil, false, err
		}

		if action.State != restore.StateVerified {
			return nil, false, nil
		}
	}

	return receipts, true, nil
}

func restoredSessionReceipts(plan restore.Plan, receipts map[string]string) []string {
	ids := make([]string, 0, len(plan.ClosureSessions))
	for _, step := range plan.Steps {
		if _, ok := step.(*restore.CreateSessionStep); !ok {
			continue
		}

		if id := receipts[step.ID()]; id != "" {
			ids = append(ids, id)
		}
	}

	return ids
}

func prepareRestorePlan(
	ctx context.Context,
	repository *store.Repository,
	server *gotmux.Server,
	cfg RestoreConfig,
) (restore.Plan, launchOverride, string, []SkippedSession, error) {
	none := launchOverride{digest: ""}

	genID, manifest, err := readGeneration(ctx, repository, cfg.Generation)
	if err != nil {
		return restore.Plan{}, none, genID, nil, err
	}

	if err := manifest.Validate(); err != nil {
		return restore.Plan{}, none, genID, nil, fmt.Errorf("validate manifest: %w", err)
	}

	manifest, override, err := overrideLaunches(ctx, cfg.Launches, manifest)
	if err != nil {
		return restore.Plan{}, none, genID, nil, err
	}

	target, err := restore.ReadTargetOptions(ctx, server)
	if err != nil {
		return restore.Plan{}, none, genID, nil, fmt.Errorf("read target options: %w", err)
	}

	manifest, unsupportedOptions := target.Adapt(manifest)

	selected := cfg.Sessions
	if len(selected) == 0 {
		selected = make([]string, 0, len(manifest.Sessions))
		for _, session := range manifest.Sessions {
			selected = append(selected, session.ID)
		}
	}

	plan, skipped, err := planSelection(ctx, repository, server, manifest, selected, cfg)
	if err != nil {
		return restore.Plan{}, none, genID, skipped, err
	}

	if _, err := restore.PlanActions(plan); err != nil {
		return restore.Plan{}, none, genID, nil, fmt.Errorf("build restore actions: %w", err)
	}

	plan.UnsupportedOptions = unsupportedOptions

	return plan, override, genID, skipped, nil
}

func readManifest(plan restore.Plan) model.Manifest {
	return plan.SourceGraph
}

func admitRestore(
	ctx context.Context,
	repository *store.Repository,
	server *gotmux.Server,
	daemon restoreDaemonIdentity,
	cfg RestoreConfig,
	generation string,
	plan restore.Plan,
	launches string,
) (store.Operation, *actionLedger, error) {
	target, err := restoreTarget(server.Endpoint().SocketPath, daemon, cfg, launches)
	if err != nil {
		return store.Operation{}, nil, err
	}

	specs, err := restore.PlanActions(plan)
	if err != nil {
		return store.Operation{}, nil, fmt.Errorf("build restore actions: %w", err)
	}

	operationID := cfg.OperationID
	if operationID == "" {
		operationID = store.NewOperationID()
	}

	op, err := repository.RegisterOperation(ctx, operationID, generation, target, buildRestoreActions(specs))
	if err != nil {
		return op, nil, fmt.Errorf("register restore operation: %w", err)
	}

	ledger, err := loadActionLedger(ctx, repository, op.ID)
	if err != nil {
		return op, nil, fmt.Errorf("load restore ledger: %w", err)
	}

	return op, ledger, nil
}

func applyAndVerify(
	ctx context.Context,
	server *gotmux.Server,
	daemon restoreDaemonIdentity,
	plan restore.Plan,
	receipts map[string]string,
	ledger *actionLedger,
	priorEffects bool,
) (restore.VerificationReport, []restore.MissingRequirement, error) {
	if err := requireRestoreDaemonIdentity(ctx, server, daemon, priorEffects); err != nil {
		return restore.VerificationReport{}, nil, err
	}

	missing, importDispatched, err := runScopedImport(ctx, server, plan, receipts, ledger)
	if err != nil {
		return restore.VerificationReport{}, missing,
			postRestoreDaemonIdentity(ctx, server, daemon, err, priorEffects || importDispatched)
	}

	effectsPossible := priorEffects || importDispatched
	if err := requireRestoreDaemonIdentity(ctx, server, daemon, effectsPossible); err != nil {
		return restore.VerificationReport{}, missing, err
	}

	verification, err := restore.Verify(ctx, server, plan, receipts)
	if err != nil {
		verifyErr := fmt.Errorf("verify restored target: %w", err)

		return restore.VerificationReport{}, missing,
			postRestoreDaemonIdentity(ctx, server, daemon, verifyErr, effectsPossible)
	}

	if err := requireRestoreDaemonIdentity(ctx, server, daemon, effectsPossible); err != nil {
		return restore.VerificationReport{}, missing, err
	}

	return verification, missing, nil
}

func reportVerificationFailure(verification restore.VerificationReport) error {
	return &verificationError{report: verification}
}

func finishRestore(
	ctx context.Context,
	repository *store.Repository,
	lease *tmuxserver.Lease,
	operation string,
	profile RecoveryProfile,
	result *RestoreResult,
) (RestoreResult, error) {
	result.Verified = true

	if len(result.MissingRequirements) > 0 {
		if profile == RecoveryPractical {
			finishErr := repository.Finish(ctx, operation, false)
			if err := errors.Join(finishErr, lease.Close()); err != nil {
				result.Reason = "finalize_failed"

				return *result, restoreFailure(ctx, "finalize", err)
			}

			result.Status = "best_effort"
			result.Reason = "fidelity_limited"
			markSkippedSessions(result)
			logRestoreSuccess(ctx, *result)

			return *result, nil
		}

		result.Status = "partial"
		result.Reason = "missing_requirements"

		if err := lease.Close(); err != nil {
			result.Reason = "finalize_failed"

			return *result, restoreFailure(ctx, "finalize", err)
		}

		logRestoreSuccess(ctx, *result)

		return *result, nil
	}

	finishErr := repository.Finish(ctx, operation, false)
	if err := errors.Join(finishErr, lease.Close()); err != nil {
		result.Reason = "finalize_failed"

		return *result, restoreFailure(ctx, "finalize", err)
	}

	result.Status = "complete"
	result.Reason = "ok"

	markSkippedSessions(result)
	logRestoreSuccess(ctx, *result)

	return *result, nil
}

// markSkippedSessions downgrades a finished restore that declined some sessions to partial, so
// the exit status and the log both say the saved set was not fully brought back.
func markSkippedSessions(result *RestoreResult) {
	if len(result.SessionsSkipped) == 0 {
		return
	}

	if result.Status == "complete" || result.Status == "best_effort" {
		result.Status = "partial"
		result.Reason = "sessions_skipped"
	}
}

func restoreTarget(endpoint string, daemon restoreDaemonIdentity, cfg RestoreConfig, launches string) ([]byte, error) {
	identity := daemon.Server
	if endpoint == "" || identity.PID <= 0 || identity.Started.IsZero() || identity.ReportedSocket == "" {
		return nil, oops.In("snapshot").Code("endpoint_required").With("operation", "restore_target").Wrap(tmuxserver.ErrEndpoint)
	}

	if daemon.Birth != "" {
		if _, err := process.ParseIdentity(daemon.Birth); err != nil {
			return nil, oops.In("snapshot").Code("daemon_identity_unavailable").With("operation", "restore_target").Wrap(tmuxserver.ErrDaemonIdentityUnavailable)
		}
	}

	target := fmt.Sprintf("%s%s\n%d\n%d\nlaunch=%t\nmissing_working_dir=%s\ncompensate=false\nprofile=%s",
		restoreTargetPrefix,
		endpoint, identity.PID, identity.Started.UnixNano(),
		cfg.LaunchProcesses, cfg.MissingWorkingDir, cfg.Profile)
	target += restoreDaemonTargetAppendix(daemon)

	if launches != "" {
		target += "\nlaunch_overrides=" + launches
	}

	if len(target) > maxRestoreTargetBytes {
		return nil, oops.In("snapshot").Code("endpoint_required").With("operation", "restore_target", "reason", "target_too_long").Wrap(tmuxserver.ErrEndpoint)
	}

	return []byte(target), nil
}

func requireRestoreDaemonIdentity(ctx context.Context, server *gotmux.Server, expected restoreDaemonIdentity, effectsPossible bool) error {
	observed, err := observeRestoreDaemonIdentity(ctx, server)
	if err == nil && !expected.Equal(observed) {
		err = tmuxserver.ErrDaemonIdentityChanged
	}

	if err == nil {
		return nil
	}

	if effectsPossible {
		return errors.Join(restore.ErrExecutionUncertain, err)
	}

	return err
}

func postRestoreDaemonIdentity(
	ctx context.Context,
	server *gotmux.Server,
	expected restoreDaemonIdentity,
	phaseErr error,
	effectsPossible bool,
) error {
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), journalCleanupTimeout)
	defer cancel()

	identityErr := requireRestoreDaemonIdentity(checkCtx, server, expected, false)

	return joinPhaseIdentityFailure(phaseErr, identityErr, effectsPossible)
}

func joinPhaseIdentityFailure(phaseErr, identityErr error, effectsPossible bool) error {
	if identityErr != nil && effectsPossible {
		identityErr = errors.Join(restore.ErrExecutionUncertain, identityErr)
	}

	return errors.Join(phaseErr, identityErr)
}

func closeWith(ctx context.Context, lease *tmuxserver.Lease, phase string, err error) error {
	return restoreFailure(ctx, phase, errors.Join(err, lease.Close()))
}

func isUncertain(err error) bool {
	return errors.Is(err, restore.ErrExecutionUncertain) ||
		errors.Is(err, restore.ErrPriorUncertainty) ||
		errors.Is(err, store.ErrUncertain)
}

func loadActionLedger(ctx context.Context, repository *store.Repository, operation string) (*actionLedger, error) {
	op, err := repository.ReconcileOperation(ctx, operation)
	if err != nil {
		return nil, fmt.Errorf("read operation journal: %w", err)
	}

	states := make(map[string]store.Action, len(op.Actions))
	for _, action := range op.Actions {
		states[action.ID] = action
	}

	return &actionLedger{repository: repository, operation: operation, states: states}, nil
}

func (l *actionLedger) state(id string) (store.Action, error) {
	action, ok := l.states[id]
	if !ok {
		return store.Action{}, oops.In("snapshot.store").
			Code("repository_conflict").
			With("operation", "read_action", "reason", "action_missing").
			Wrapf(store.ErrUnavailable, "%s", id)
	}

	return action, nil
}

func (l *actionLedger) transition(ctx context.Context, id string, expected, next model.ActionState, runtimeID model.Bytes) error {
	if err := l.repository.Transition(ctx, l.operation, id, expected, next, runtimeID); err != nil {
		return fmt.Errorf("record action transition: %w", err)
	}

	action := l.states[id]
	action.State = next
	action.RuntimeID = runtimeID
	l.states[id] = action

	return nil
}

func (l *actionLedger) advance(ctx context.Context, id string) error {
	action, err := l.state(id)
	if err != nil {
		return err
	}

	switch action.State {
	case model.ActionStateIssued:
		return nil
	case model.ActionStatePlanned:
		return l.transition(ctx, id, "planned", "issued", nil)
	case model.ActionStateNotSent:
		if err := l.transition(ctx, id, "not-sent", "planned", nil); err != nil {
			return err
		}

		return l.transition(ctx, id, "planned", "issued", nil)
	case model.ActionStateUnknown, model.ActionStateConfirmed, model.ActionStateVerified:
	}

	return oops.In("snapshot.store").
		Code("operation_uncertain").
		With("operation", "advance_action", "reason", "effect_outcome_uncertain").
		Wrapf(restore.ErrPriorUncertainty, "%s", id)
}

func (j *stepJournal) Steps(ctx context.Context, steps []restore.Step) (map[string]restore.StepState, error) {
	states := make(map[string]restore.StepState, len(steps))

	for _, step := range steps {
		action, err := j.ledger.state(j.actionID(step))
		if err != nil {
			return nil, err
		}

		if action.Kind != restore.StepKindAction(step) {
			return nil, oops.In("snapshot.store").
				Code("repository_conflict").
				With("operation", "read_action", "reason", "action_kind_mismatch").
				Wrapf(store.ErrConflict, "%s", action.ID)
		}

		states[step.ID()] = restore.StepState{State: action.State, RuntimeID: string(action.RuntimeID)}
	}

	return states, nil
}

func (j *stepJournal) Intent(ctx context.Context, step restore.Step) error {
	return j.ledger.advance(ctx, j.actionID(step))
}

func (j *stepJournal) Confirmed(ctx context.Context, step restore.Step, runtimeID string) error {
	return j.ledger.transition(ctx, j.actionID(step), "issued", "confirmed", model.Bytes(runtimeID))
}

func (j *stepJournal) NotSent(ctx context.Context, step restore.Step) error {
	return j.ledger.transition(ctx, j.actionID(step), "issued", "not-sent", nil)
}

func (j *stepJournal) Unknown(ctx context.Context, step restore.Step) error {
	action, err := j.ledger.state(j.actionID(step))
	if err != nil {
		return err
	}

	if action.State != "issued" {
		return nil
	}

	return j.ledger.transition(ctx, j.actionID(step), "issued", "unknown", nil)
}

func (j *stepJournal) actionID(step restore.Step) string {
	return restore.ActionID(step.ID())
}

func runScopedImport(
	ctx context.Context,
	server *gotmux.Server,
	plan restore.Plan,
	receipts map[string]string,
	ledger *actionLedger,
) ([]restore.MissingRequirement, bool, error) {
	staticMissing := restore.ScopedStateRequirements(plan)

	action, err := ledger.state(restore.ImportStateActionID)
	if err != nil {
		return staticMissing, false, err
	}

	switch action.State {
	case model.ActionStateConfirmed, model.ActionStateVerified:
		mismatches, observeErr := restore.ObserveScopedStateConflicts(ctx, server, plan, receipts)
		if observeErr != nil {
			return staticMissing, false, fmt.Errorf("observe scoped state conflicts: %w", observeErr)
		}

		if len(mismatches) > 0 {
			return staticMissing, false, oops.In("snapshot.restore").
				Code("restore_conflict").
				With("operation", "import_scoped_state", "reason", "resumed_scoped_state_conflict").
				Wrapf(restore.ErrPriorConflict, "scoped state conflict on resume: %d mismatches", len(mismatches))
		}

		return staticMissing, false, nil
	case model.ActionStateIssued, model.ActionStateUnknown:
		return staticMissing, false, oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "import_scoped_state", "reason", "prior_effect_uncertain").
			Wrap(restore.ErrPriorUncertainty)
	case model.ActionStatePlanned, model.ActionStateNotSent:
	}

	if err := ledger.advance(ctx, restore.ImportStateActionID); err != nil {
		return staticMissing, false, err
	}

	importReport, importErr := restore.ImportScopedState(ctx, server, plan, receipts)
	missing := mergeRequirements(staticMissing, importReport.Missing)

	if importErr != nil {
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), journalCleanupTimeout)
		defer cancel()

		journalErr := ledger.transition(recordCtx, restore.ImportStateActionID, "issued", "unknown", nil)

		return missing, true, scopedImportFailure(importErr, journalErr)
	}

	return missing, true, ledger.transition(ctx, restore.ImportStateActionID, "issued", "confirmed", nil)
}

func scopedImportFailure(importErr, journalErr error) error {
	return oops.In("snapshot.restore").
		Code("operation_uncertain").
		With("operation", "import_scoped_state", "reason", "import_failed_uncertain").
		Wrap(errors.Join(restore.ErrExecutionUncertain, importErr, journalErr))
}

func markVerified(ctx context.Context, ledger *actionLedger, plan restore.Plan, verification restore.VerificationReport) error {
	verified := make(map[string]bool, len(verification.VerifiedSteps))
	for _, stepID := range verification.VerifiedSteps {
		verified[restore.ActionID(stepID)] = true
	}

	for _, step := range plan.Steps {
		id := restore.ActionID(step.ID())

		action, err := ledger.state(id)
		if err != nil {
			return err
		}

		if action.State == "verified" {
			continue
		}

		if action.State != "confirmed" {
			return oops.In("snapshot.restore").
				Code("operation_uncertain").
				With("operation", "mark_verified", "reason", "action_not_confirmed").
				Wrapf(restore.ErrPriorUncertainty, "%s", action.ID)
		}

		if !verified[id] {
			return oops.In("snapshot.restore").
				Code("restore_failed").
				With("operation", "mark_verified", "reason", "step_not_verified").
				Wrapf(restore.ErrExecutionFailed, "%s", action.ID)
		}

		if err := ledger.transition(ctx, id, "confirmed", "verified", action.RuntimeID); err != nil {
			return err
		}
	}

	if err := verifyAction(ctx, ledger, restore.ImportStateActionID); err != nil {
		return err
	}

	return verifyAction(ctx, ledger, restore.VerifyActionID)
}

func verifyAction(ctx context.Context, ledger *actionLedger, id string) error {
	action, err := ledger.state(id)
	if err != nil {
		return err
	}

	switch action.State {
	case model.ActionStateVerified:
		return nil
	case model.ActionStatePlanned, model.ActionStateNotSent:
		if err := ledger.advance(ctx, id); err != nil {
			return err
		}
	case model.ActionStateIssued:
	case model.ActionStateConfirmed:
		return ledger.transition(ctx, id, "confirmed", "verified", action.RuntimeID)
	case model.ActionStateUnknown:
		return oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "verify_action", "reason", "effect_outcome_uncertain").
			Wrapf(restore.ErrPriorUncertainty, "%s", id)
	}

	if err := ledger.transition(ctx, id, "issued", "confirmed", nil); err != nil {
		return err
	}

	return ledger.transition(ctx, id, "confirmed", "verified", nil)
}

func mergeRequirements(planned, runtime []restore.MissingRequirement) []restore.MissingRequirement {
	merged := make([]restore.MissingRequirement, 0, len(planned)+len(runtime))

	merged = append(merged, runtime...)

	for _, entry := range planned {
		replaced := false

		for _, observed := range runtime {
			if observed.Domain == entry.Domain && observed.Status == entry.Status {
				replaced = true

				break
			}
		}

		if !replaced {
			merged = append(merged, entry)
		}
	}

	return merged
}

func operationStatus(op store.Operation) OperationStatus {
	status := OperationStatus{
		OperationID: op.ID,
		Source:      op.Source,
		Target:      string(op.Target),
		State:       op.State,
		Actions:     make([]ActionStatus, 0, len(op.Actions)),
		Uncertain:   nil,
		Settled:     true,
	}

	for _, action := range op.Actions {
		status.Actions = append(status.Actions, ActionStatus{
			ID:        action.ID,
			Kind:      string(action.Kind),
			State:     string(action.State),
			RuntimeID: string(action.RuntimeID),
		})

		switch action.State {
		case model.ActionStateIssued, model.ActionStateUnknown:
			status.Uncertain = append(status.Uncertain, action.ID)
			status.Settled = false
		case model.ActionStatePlanned, model.ActionStateNotSent:
			status.Settled = false
		case model.ActionStateConfirmed, model.ActionStateVerified:
		}
	}

	if op.State == "active" {
		status.Settled = false
	}

	return status
}

func logRestoreSuccess(ctx context.Context, result RestoreResult) {
	level := zerolog.InfoLevel
	if result.Status != "complete" {
		level = zerolog.WarnLevel
	}

	outcomes := tallyRestoredPanes(result.Panes)

	zerolog.Ctx(ctx).WithLevel(level).
		Str("component", "snapshot").
		Str("operation", "restore").
		Str("status", result.Status).
		Str("reason", result.Reason).
		Int("sessions", len(result.SessionsRestored)).
		Int("skipped", len(result.SessionsSkipped)).
		Bool("verified", result.Verified).
		Int("resumed_panes", outcomes.resumed).
		Int("restarted_panes", outcomes.restarted).
		Int("shell_panes", outcomes.shell).
		Int("unrecovered_panes", outcomes.unrecovered).
		Int("directory_fallback_panes", outcomes.directoryFallback).
		Int64("duration_ms", restoreDuration(ctx)).
		Msg("snapshot restored")
}

func tallyRestoredPanes(panes []restore.PaneResult) restoredPaneTally {
	var tally restoredPaneTally

	for _, pane := range panes {
		switch pane.Outcome {
		case restore.PaneOutcomeNativeResumeDispatch:
			tally.resumed++
		case restore.PaneOutcomeRestartDispatch:
			tally.restarted++
		case restore.PaneOutcomeDefaultShell:
			tally.shell++
		default:
			tally.unrecovered++
		}

		if pane.CWDFallback {
			tally.directoryFallback++
		}
	}

	return tally
}

func restoreDuration(ctx context.Context) int64 {
	started, ok := ctx.Value(restoreStartedKey{}).(time.Time)
	if !ok {
		return 0
	}

	return time.Since(started).Milliseconds()
}

func restoreFailure(ctx context.Context, phase string, err error) error {
	code, reason := classifyRestoreFailure(err)

	event := zerolog.Ctx(ctx).Error().
		Str("component", "snapshot").
		Str("operation", "restore").
		Str("status", "failed").
		Str("phase", phase).
		Str("reason", reason).
		Int64("duration_ms", restoreDuration(ctx))

	if verification, ok := errors.AsType[*verificationError](err); ok {
		event = event.Int("mismatches", len(verification.report.Mismatches)).
			Str("mismatch_reasons", mismatchReasons(verification.report.Mismatches))
	}

	if step, ok := errorstep.Owned(err, "snapshot.restore"); ok {
		event = event.Str("failed_step", step.Operation)
		if step.Reason != "" {
			event = event.Str("step_reason", step.Reason)
		}
	}

	event.Msg("snapshot restore failed")

	return &restoreOutcomeError{error: oops.In("snapshot").
		Code(code).
		With("operation", "restore", "phase", phase, "reason", reason).
		Wrap(err)}
}

func (e *restoreOutcomeError) Unwrap() error       { return e.error }
func (e *restoreOutcomeError) OutcomeLogged() bool { return true }

// mismatchReasons lists the distinct fixed mismatch reasons, without the entities they name.
func mismatchReasons(mismatches []restore.VerificationMismatch) string {
	reasons := make([]string, 0, len(mismatches))
	for _, mismatch := range mismatches {
		reasons = append(reasons, mismatch.Domain+"/"+mismatch.Reason)
	}

	slices.Sort(reasons)

	return strings.Join(slices.Compact(reasons), ",")
}

func classifyRestoreFailure(err error) (string, string) {
	for _, class := range restoreFailureClasses {
		for _, cause := range class.causes {
			if errors.Is(err, cause) {
				return class.code, class.reason
			}
		}
	}

	return "restore_failed", "restore_error"
}
