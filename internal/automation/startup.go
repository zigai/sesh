//go:build linux || darwin

package automation

import (
	"context"
	"errors"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	ReasonRestoreDisabled   = "disabled"
	ReasonRepositoryMissing = "repository_missing"
	ReasonPendingOperation  = "pending_operation"
	ReasonRecoveryRequired  = "recovery_required"
	ReasonObservationOnly   = "observation_incomplete"
	ReasonRestoreAdmitted   = "admitted"
)

var ErrStartupRepositoryRequired = errors.New("repository is required to inspect startup state")

type RestoreAdmission struct {
	Allowed    bool
	Reason     string
	Generation string
}

type StartupState struct {
	Enabled           bool
	RepositoryPresent bool
	PendingOperations int
	RecoveryRequired  bool
	ObservationOnly   bool
	Generation        string
	Practical         bool
}

func EvaluateStartupRestore(state StartupState) RestoreAdmission {
	if !state.Enabled {
		return RestoreAdmission{Allowed: false, Reason: ReasonRestoreDisabled, Generation: state.Generation}
	}

	if !state.RepositoryPresent {
		return RestoreAdmission{Allowed: false, Reason: ReasonRepositoryMissing, Generation: ""}
	}

	if state.PendingOperations > 0 {
		return RestoreAdmission{Allowed: false, Reason: ReasonPendingOperation, Generation: state.Generation}
	}

	if state.RecoveryRequired {
		return RestoreAdmission{Allowed: false, Reason: ReasonRecoveryRequired, Generation: state.Generation}
	}

	if (state.ObservationOnly && !state.Practical) || state.Generation == "" {
		return RestoreAdmission{Allowed: false, Reason: ReasonObservationOnly, Generation: state.Generation}
	}

	return RestoreAdmission{Allowed: true, Reason: ReasonRestoreAdmitted, Generation: state.Generation}
}

func InspectStartupState(ctx context.Context, repository *store.Repository) (StartupState, error) {
	if repository == nil {
		return StartupState{}, oops.In("automation").Code("repository_required").With("operation", "inspect_startup_state").Wrap(ErrStartupRepositoryRequired)
	}

	state := new(StartupState)
	state.RepositoryPresent = true

	catalog, err := repository.Inspect(ctx)
	if err != nil {
		return StartupState{}, oops.In("automation").Code("startup_inspect_failed").With("operation", "inspect_startup_state").Wrap(err)
	}

	for _, operation := range catalog.Operations {
		if operation.State == "active" {
			state.PendingOperations++
		}
	}

	generation, _, readErr := repository.ReadCurrent(ctx)

	switch {
	case readErr == nil:
		state.Generation = generation.ID
	case errors.Is(readErr, store.ErrRecoveryRequired):
		if currentGenerationIncomplete(catalog) {
			state.ObservationOnly = true
			state.Generation = catalog.Current
		} else {
			state.RecoveryRequired = true
		}
	case errors.Is(readErr, store.ErrUnavailable):
	default:
		return StartupState{}, oops.In("automation").Code("startup_inspect_failed").With("operation", "inspect_startup_state").Wrap(readErr)
	}

	return *state, nil
}

func currentGenerationIncomplete(catalog store.Catalog) bool {
	if catalog.Current == "" || len(catalog.Attempts) == 0 {
		return false
	}

	latest := catalog.Attempts[0]
	for _, attempt := range catalog.Attempts[1:] {
		if attempt.Sequence > latest.Sequence {
			latest = attempt
		}
	}

	if latest.State != "incomplete" || latest.ID != catalog.Current {
		return false
	}

	for _, rewrite := range catalog.Rewrites {
		if rewrite.State == "pending" && rewrite.Source == catalog.Current {
			return false
		}
	}

	for _, generation := range catalog.Generations {
		if generation.ID == catalog.Current {
			return !generation.Complete
		}
	}

	return false
}
