//go:build linux || darwin

package restore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/snapshot/model"
)

// Durable journal states for one plan step, mirroring the repository action states.
// The only safe dispatch sequence is planned (or not-sent) -> issued -> effect ->
// confirmed. An effect is attempted only after its intent is durable, so "planned"
// proves the effect was never attempted and "issued" or "unknown" never authorize a
// redispatch.
const (
	StatePlanned   = model.ActionStatePlanned
	StateIssued    = model.ActionStateIssued
	StateNotSent   = model.ActionStateNotSent
	StateUnknown   = model.ActionStateUnknown
	StateConfirmed = model.ActionStateConfirmed
	StateVerified  = model.ActionStateVerified
)

const (
	KindCreateSession = model.ActionKindCreateSession
	KindCreateWindow  = model.ActionKindCreateWindow
	KindCreatePane    = model.ActionKindCreatePane
	KindLink          = model.ActionKindLink
	KindConfigure     = model.ActionKindConfigure
	KindLaunch        = model.ActionKindLaunch
	KindVerify        = model.ActionKindVerify
)

const (
	ImportStateActionID = "import-scoped-state"
	VerifyActionID      = "verify"
	terminalActionCount = 2
)

var (
	ErrJournalRequired            = errors.New("a durable step journal is required")
	ErrJournalIncomplete          = errors.New("step journal does not describe the plan")
	ErrPriorUncertainty           = errors.New("previous attempt left an uncertain effect")
	ErrPriorConflict              = errors.New("previous attempt conflicts with scoped state")
	ErrExecutionUncertain         = errors.New("effect outcome uncertain")
	ErrRuntimeIdentityUnavailable = errors.New("confirmed effect identity unavailable")
)

type ActionSpec struct {
	ID   string           `json:"id"`
	Kind model.ActionKind `json:"kind"`
}

type StepState struct {
	State     model.ActionState `json:"state"`
	RuntimeID string            `json:"runtimeId"`
}

type Journal interface {
	Steps(ctx context.Context, steps []Step) (map[string]StepState, error)
	Intent(ctx context.Context, step Step) error
	Confirmed(ctx context.Context, step Step, runtimeID string) error
	NotSent(ctx context.Context, step Step) error
	Unknown(ctx context.Context, step Step) error
}

func StepKindAction(step Step) model.ActionKind {
	switch step.(type) {
	case *CreateSessionStep:
		return KindCreateSession
	case *PlaceInitialWindowStep, *CreateWindowStep:
		return KindCreateWindow
	case *SplitPaneStep:
		return KindCreatePane
	case *LinkWindowStep:
		return KindLink
	case *CloseBootstrapWindowStep, *ApplyLayoutStep, *SelectPaneStep, *ApplyZoomStep, *SelectWindowStep, *SetPaneTitleStep:
		return KindConfigure
	}

	panic("unknown step type")
}

func ActionID(stepID string) string {
	sanitized := strings.ReplaceAll(stepID, ":", "-")
	if model.ValidID(sanitized) {
		return sanitized
	}

	digest := sha256.Sum256([]byte(stepID))

	return "a-" + hex.EncodeToString(digest[:8])
}

func PlanActions(plan Plan) ([]ActionSpec, error) {
	if len(plan.Steps) > model.MaxEntities-terminalActionCount {
		return nil, oops.In("snapshot.restore").
			Code("restore_unsupported").
			With("operation", "build_actions", "reason", "plan_exceeds_journal_bounds").
			Wrap(ErrInvalid)
	}

	actions := make([]ActionSpec, 0, len(plan.Steps)+terminalActionCount)
	seen := make(map[string]bool, len(plan.Steps)+terminalActionCount)

	for _, step := range plan.Steps {
		id := ActionID(step.ID())
		if !model.ValidID(id) || seen[id] {
			return nil, oops.In("snapshot.restore").
				Code("restore_unsupported").
				With("operation", "build_actions", "reason", "action_identifier_not_journal_safe").
				Wrap(ErrInvalid)
		}

		seen[id] = true
		actions = append(actions, ActionSpec{ID: id, Kind: StepKindAction(step)})
	}

	for _, terminal := range []ActionSpec{
		{ID: ImportStateActionID, Kind: KindConfigure},
		{ID: VerifyActionID, Kind: KindVerify},
	} {
		if seen[terminal.ID] {
			return nil, oops.In("snapshot.restore").
				Code("restore_unsupported").
				With("operation", "build_actions", "reason", "action_identifier_not_journal_safe").
				Wrap(ErrInvalid)
		}

		seen[terminal.ID] = true
		actions = append(actions, terminal)
	}

	return actions, nil
}
