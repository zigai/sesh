//go:build linux || darwin

package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

var (
	ErrStartupRepositoryRequired = errors.New("repository is required to inspect startup state")
	// ErrActiveRestores reports restores still recorded as active, which hold back automatic saves
	// and startup restores until they finish or are abandoned.
	ErrActiveRestores = errors.New("restore still in progress or interrupted")
)

// ActiveRestoresError names the active restores behind ErrActiveRestores and how to clear them.
type ActiveRestoresError struct {
	Operations []string
}

type RestoreAdmission struct {
	Allowed    bool
	Reason     string
	Generation string
}

type StartupState struct {
	Enabled           bool
	RepositoryPresent bool
	PendingOperations int
	ActiveOperations  []string
	// Abandoned counts the restores abandoned before inspection because their tmux server no
	// longer exists.
	Abandoned        int
	RecoveryRequired bool
	ObservationOnly  bool
	Generation       string
	Practical        bool
}

func (e *ActiveRestoresError) Error() string {
	if len(e.Operations) == 0 {
		return ErrActiveRestores.Error()
	}

	commands := make([]string, 0, len(e.Operations))
	for _, id := range e.Operations {
		commands = append(commands, "sesh snapshot recover restore abandon "+id)
	}

	return fmt.Sprintf("%d restore(s) still in progress or interrupted (%s); "+
		"if no restore is running, abandon them with: %s",
		len(e.Operations), strings.Join(e.Operations, ", "), strings.Join(commands, " && "))
}

func (e *ActiveRestoresError) Is(target error) bool { return target == ErrActiveRestores }

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
			state.ActiveOperations = append(state.ActiveOperations, operation.ID)
		}
	}

	generation, _, readErr := repository.ReadCurrent(ctx)

	switch {
	case readErr == nil:
		state.Generation = generation.ID
	case errors.Is(readErr, store.ErrRecoveryRequired):
		if catalog.PracticalCurrentAdmissible() {
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
