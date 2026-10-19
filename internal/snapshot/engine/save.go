//go:build linux || darwin

package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const failRecordTimeout = 2 * time.Second

var (
	ErrLeaseRootRequired  = errors.New("explicit shared lease root required")
	ErrRepositoryRequired = errors.New("repository required")
)

type SaveConfig struct {
	LeaseRoot              string
	ProcessEnrichment      bool
	ProcessEnvironmentKeys []string
	AgentSource            agents.SessionSource
	Profile                RecoveryProfile
}

type SaveResult struct {
	Attempt    string
	Generation string
	Status     string
}

type saveOutcomeError struct{ error }

type paneProcess struct {
	pane  string
	birth string
}

func SaveObservation(ctx context.Context, repository *store.Repository, server *gotmux.Server, selected []gotmux.SessionID, cfg SaveConfig) (SaveResult, error) {
	started := time.Now()

	if cfg.LeaseRoot == "" {
		return SaveResult{Attempt: "", Generation: "", Status: "failed"}, saveFailure(ctx, "admission", started, ErrLeaseRootRequired)
	}

	if server == nil || server.Endpoint().SocketPath == "" {
		return SaveResult{Attempt: "", Generation: "", Status: "failed"}, saveFailure(ctx, "admission", started, tmuxserver.ErrEndpoint)
	}

	if repository == nil {
		return SaveResult{Attempt: "", Generation: "", Status: "failed"}, saveFailure(ctx, "admission", started, ErrRepositoryRequired)
	}

	observe := func(ctx context.Context) (capture.Observation, error) {
		if cfg.ProcessEnrichment || len(cfg.ProcessEnvironmentKeys) > 0 {
			return capture.ObserveWithApplicationRecovery(ctx, server, selected, cfg.ProcessEnvironmentKeys, cfg.AgentSource)
		}

		return capture.Observe(ctx, server, selected)
	}

	return saveWithProfile(ctx, repository, cfg.LeaseRoot, server.Endpoint().SocketPath, cfg.Profile, observe)
}

func saveWithProfile(ctx context.Context, repository *store.Repository, leaseRoot string, socketPath string, profile RecoveryProfile, observe func(context.Context) (capture.Observation, error)) (SaveResult, error) {
	started := time.Now()

	zerolog.Ctx(ctx).Info().Str("component", "snapshot").Str("operation", "save").Str("status", "started").Msg("snapshot save started")

	result := SaveResult{Attempt: "", Generation: "", Status: "failed"}

	if leaseRoot == "" {
		return result, saveFailure(ctx, "admission", started, ErrLeaseRootRequired)
	}

	if socketPath == "" {
		return result, saveFailure(ctx, "admission", started, tmuxserver.ErrEndpoint)
	}

	lease, err := tmuxserver.AcquireLease(ctx, leaseRoot, socketPath)
	if err != nil {
		return result, saveFailure(ctx, "lease", started, err)
	}

	attempt, err := repository.Begin(ctx)
	result.Attempt = attempt.ID

	if err != nil {
		closeErr := lease.Close()
		return result, saveFailure(ctx, "register", started, errors.Join(err, closeErr))
	}

	observation, carried, err := observeCarryingResumeEvidence(ctx, repository, observe)
	if err != nil {
		failErr := recordFailedAttempt(ctx, repository, attempt.ID)
		closeErr := lease.Close()

		return result, saveFailure(ctx, "capture", started, errors.Join(err, failErr, closeErr))
	}

	artifacts := make(map[string]io.Reader, len(observation.Artifacts))
	for id, data := range observation.Artifacts {
		artifacts[id] = bytes.NewReader(data)
	}

	generation, err := repository.Commit(ctx, attempt.ID, observation.Manifest, artifacts)
	result.Generation = generation.ID

	if err != nil {
		var failErr error
		if !errors.Is(err, store.ErrUncertain) {
			failErr = recordFailedAttempt(ctx, repository, attempt.ID)
		}

		closeErr := lease.Close()

		return result, saveFailure(ctx, "commit", started, errors.Join(err, failErr, closeErr))
	}

	if closeErr := lease.Close(); closeErr != nil {
		return result, saveFailure(ctx, "finalize", started, closeErr)
	}

	status, reason := "partial", "parity_incomplete"
	if profile == RecoveryPractical && observation.Manifest.PracticalRecoveryReady() {
		status, reason = "best_effort", "fidelity_limited"
	}

	result.Status = status

	logSaveSuccess(ctx, observation, carried, status, reason, started)

	return result, nil
}

func observeCarryingResumeEvidence(ctx context.Context, repository *store.Repository, observe func(context.Context) (capture.Observation, error)) (capture.Observation, int, error) {
	baseline, err := resumeBaseline(ctx, repository)
	if err != nil {
		return capture.Observation{}, 0, err
	}

	observation, err := observe(ctx)
	if err != nil {
		return capture.Observation{}, 0, err
	}

	carried := 0

	for i := range observation.Manifest.Panes {
		pane := &observation.Manifest.Panes[i]

		launch, ok := baseline[paneProcess{pane: pane.ID, birth: string(pane.Launch.Birth)}]
		if ok && pane.Launch.Recovery != model.RecoveryResume {
			pane.Launch = launch
			carried++
		}
	}

	return observation, carried, nil
}

func resumeBaseline(ctx context.Context, repository *store.Repository) (map[paneProcess]model.Launch, error) {
	baseline := make(map[paneProcess]model.Launch)

	_, manifest, err := repository.ReadPracticalCurrent(ctx)
	if errors.Is(err, store.ErrUnavailable) || errors.Is(err, store.ErrRecoveryRequired) {
		return baseline, nil
	}

	if err != nil {
		return nil, oops.In("snapshot").With("operation", "read_resume_baseline").Wrapf(err, "read current snapshot")
	}

	for _, pane := range manifest.Panes {
		if pane.Launch.Recovery == model.RecoveryResume && len(pane.Launch.Birth) > 0 {
			baseline[paneProcess{pane: pane.ID, birth: string(pane.Launch.Birth)}] = pane.Launch
		}
	}

	return baseline, nil
}

func logSaveSuccess(ctx context.Context, observation capture.Observation, carried int, status, reason string, started time.Time) {
	recovery := make(map[model.Recovery]int, len(observation.Manifest.Panes))
	for _, pane := range observation.Manifest.Panes {
		recovery[pane.Launch.Recovery]++
	}

	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "save").
		Str("status", status).
		Str("reason", reason).
		Int("sessions", len(observation.Manifest.Sessions)).
		Int("panes", len(observation.Manifest.Panes)).
		Int("terminal_text_omitted", observation.OmittedTerminalText()).
		Int("floating_panes_omitted", observation.FloatingPanesOmitted).
		Int("restart_panes", recovery[model.RecoveryRestart]).
		Int("resume_panes", recovery[model.RecoveryResume]).
		Int("carried_resume_panes", carried).
		Int("unverified_panes", recovery[model.RecoveryUnverified]).
		Int("nonrestartable_panes", recovery[model.RecoveryNonrestartable]).
		Int64("duration_ms", time.Since(started).Milliseconds()).
		Msg("snapshot observation saved")
}

func recordFailedAttempt(ctx context.Context, repository *store.Repository, id string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failRecordTimeout)
	defer cancel()

	if err := repository.Fail(ctx, id); err != nil {
		return oops.In("snapshot").With("operation", "record_failed_attempt").Wrapf(err, "record failed save attempt")
	}

	return nil
}

func saveFailure(ctx context.Context, phase string, started time.Time, err error) error {
	reason := "save_failed"

	switch {
	case errors.Is(err, store.ErrUncertain):
		reason = "publication_uncertain"
	case errors.Is(err, context.Canceled):
		reason = "operation_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "operation_timed_out"
	case errors.Is(err, gotmux.ErrOutputLimit):
		reason = "output_limit_exceeded"
	case errors.Is(err, capture.ErrChanged):
		reason = "capture_changed"
	case errors.Is(err, capture.ErrAgentRegistry):
		reason = "agent_registry_failed"
	case errors.Is(err, capture.ErrUnsupported), errors.Is(err, process.ErrUnsupported):
		reason = "capture_unsupported"
	case errors.Is(err, model.ErrInvalid):
		reason = "snapshot_invalid"
	case processFailureCode(err) != "":
		reason = "process_observation_failed"
	}

	event := zerolog.Ctx(ctx).Error().
		Str("component", "snapshot").
		Str("operation", "save").
		Str("status", "failed").
		Str("phase", phase).
		Str("reason", reason).
		Int64("duration_ms", time.Since(started).Milliseconds())

	if code := processFailureCode(err); code != "" {
		event = event.Str("process_code", code)
	}

	event.Msg("snapshot save failed")

	return &saveOutcomeError{error: oops.In("snapshot").Code(reason).
		With("operation", "save", "phase", phase).
		Wrapf(err, "save snapshot observation")}
}

func processFailureCode(err error) string {
	diagnostic, ok := oops.AsOops(err)
	if !ok || diagnostic.Domain() != process.Domain {
		return ""
	}

	if code, ok := diagnostic.Code().(string); ok && code != "" {
		return code
	}

	return process.CodeFailed
}

func (e *saveOutcomeError) Unwrap() error       { return e.error }
func (e *saveOutcomeError) OutcomeLogged() bool { return true }
