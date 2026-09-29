//go:build linux || darwin

package engine

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/store"
)

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

func SaveObservation(ctx context.Context, repository *store.Repository, server *gotmux.Server, selected []gotmux.SessionID, cfg SaveConfig) (SaveResult, error) {
	if cfg.LeaseRoot == "" {
		return SaveResult{Attempt: "", Generation: "", Status: "failed"}, saveFailure(ctx, "admission", ErrLeaseRootRequired)
	}

	if server == nil || server.Endpoint().SocketPath == "" {
		return SaveResult{Attempt: "", Generation: "", Status: "failed"}, saveFailure(ctx, "admission", integration.ErrEndpoint)
	}

	if repository == nil {
		return SaveResult{Attempt: "", Generation: "", Status: "failed"}, saveFailure(ctx, "admission", ErrRepositoryRequired)
	}

	observe := func(ctx context.Context) (capture.Observation, error) {
		if cfg.ProcessEnrichment || len(cfg.ProcessEnvironmentKeys) > 0 {
			return capture.ObserveWithApplicationRecovery(ctx, server, selected, cfg.ProcessEnvironmentKeys, cfg.AgentSource)
		}

		return capture.Observe(ctx, server, selected)
	}

	return saveWithProfile(ctx, repository, cfg.LeaseRoot, server.Endpoint().SocketPath, cfg.Profile, observe)
}

func save(ctx context.Context, repository *store.Repository, leaseRoot string, socketPath string, observe func(context.Context) (capture.Observation, error)) (SaveResult, error) {
	return saveWithProfile(ctx, repository, leaseRoot, socketPath, RecoveryExact, observe)
}

func saveWithProfile(ctx context.Context, repository *store.Repository, leaseRoot string, socketPath string, profile RecoveryProfile, observe func(context.Context) (capture.Observation, error)) (SaveResult, error) {
	zerolog.Ctx(ctx).Info().Str("component", "snapshot").Str("operation", "save").Str("status", "started").Msg("snapshot save started")

	result := SaveResult{Attempt: "", Generation: "", Status: "failed"}

	if leaseRoot == "" {
		return result, saveFailure(ctx, "admission", ErrLeaseRootRequired)
	}

	if socketPath == "" {
		return result, saveFailure(ctx, "admission", integration.ErrEndpoint)
	}

	lease, err := integration.AcquireLease(ctx, leaseRoot, socketPath)
	if err != nil {
		return result, saveFailure(ctx, "lease", err)
	}

	attempt, err := repository.Begin(ctx)
	result.Attempt = attempt.ID

	if err != nil {
		closeErr := lease.Close()
		return result, saveFailure(ctx, "register", errors.Join(err, closeErr))
	}

	observation, err := observe(ctx)
	if err != nil {
		failErr := repository.Fail(ctx, attempt.ID)
		closeErr := lease.Close()

		return result, saveFailure(ctx, "capture", errors.Join(err, failErr, closeErr))
	}

	artifacts := make(map[string]io.Reader, len(observation.Artifacts))
	for id, data := range observation.Artifacts {
		artifacts[id] = bytes.NewReader(data)
	}

	generation, err := repository.Commit(ctx, attempt.ID, observation.Manifest, artifacts)
	result.Generation = generation.ID

	if err != nil {
		closeErr := lease.Close()
		return result, saveFailure(ctx, "commit", errors.Join(err, closeErr))
	}

	if closeErr := lease.Close(); closeErr != nil {
		return result, saveFailure(ctx, "finalize", closeErr)
	}

	status, reason := "partial", "parity_incomplete"
	if profile == RecoveryPractical && observation.Manifest.PracticalRecoveryReady() {
		status, reason = "best_effort", "fidelity_limited"
	}

	result.Status = status

	zerolog.Ctx(ctx).Warn().
		Str("component", "snapshot").
		Str("operation", "save").
		Str("status", status).
		Str("reason", reason).
		Int("sessions", len(observation.Manifest.Sessions)).
		Int("panes", len(observation.Manifest.Panes)).
		Int("terminal_text_omitted", observation.OmittedTerminalText()).
		Msg("snapshot observation saved")

	return result, nil
}

func saveFailure(ctx context.Context, phase string, err error) error {
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
	}

	zerolog.Ctx(ctx).Error().
		Str("component", "snapshot").
		Str("operation", "save").
		Str("status", "failed").
		Str("phase", phase).
		Str("reason", reason).
		Msg("snapshot save failed")

	return oops.In("snapshot").Code(reason).
		With("operation", "save", "phase", phase).
		Wrapf(err, "save snapshot observation")
}
