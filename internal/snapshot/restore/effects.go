//go:build linux || darwin

package restore

import (
	"context"
	"errors"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
)

const (
	dispatchTimeout = 15 * time.Second
)

type mutationOutcome struct {
	Effect    gotmux.Effect
	RuntimeID string
}

type mutationError struct {
	step string
	err  error
}

type appliedError struct {
	runtimeID string
	err       error
}

type uncertainError struct {
	step string
	err  error
}

type collectedEvidence struct {
	hasNotSent   bool
	hasUnknown   bool
	hasConfirmed bool
	hasRejected  bool
	unrecognized bool
	runtimeID    string
}

func (e *mutationError) Error() string {
	return "step " + e.step + " refused before application: " + e.err.Error()
}

func (e *mutationError) Unwrap() error { return e.err }

func (e *appliedError) Error() string {
	return "applied identity " + e.runtimeID + ": " + e.err.Error()
}

func (e *appliedError) Unwrap() error { return e.err }

func (e *uncertainError) Error() string {
	return "step " + e.step + " outcome uncertain: " + e.err.Error()
}

func (e *uncertainError) Unwrap() error {
	return errors.Join(ErrExecutionUncertain, e.err)
}

// classifyMutation reports the effect knowledge carried by a failed gotmux mutation.
// It traverses all branches of joined errors. Uncertainty is strictly preserved:
// if any branch is Unknown or unrecognized, the overall outcome stays Unknown.
// Exit status and stderr diagnostics are never used to downgrade library Unknown.
func classifyMutation(err error) mutationOutcome {
	if err == nil {
		return mutationOutcome{Effect: gotmux.EffectConfirmed, RuntimeID: ""}
	}

	evidence := collectEvidence(err)

	if evidence.hasUnknown || evidence.unrecognized {
		return mutationOutcome{Effect: gotmux.EffectUnknown, RuntimeID: evidence.runtimeID}
	}

	if evidence.runtimeID != "" || evidence.hasConfirmed {
		return mutationOutcome{Effect: gotmux.EffectConfirmed, RuntimeID: evidence.runtimeID}
	}

	if evidence.hasRejected {
		return mutationOutcome{Effect: gotmux.EffectRejected, RuntimeID: ""}
	}

	if evidence.hasNotSent && !evidence.hasUnknown && !evidence.unrecognized {
		return mutationOutcome{Effect: gotmux.EffectNotSent, RuntimeID: ""}
	}

	return mutationOutcome{Effect: gotmux.EffectUnknown, RuntimeID: evidence.runtimeID}
}

func collectEvidence(err error) collectedEvidence {
	var ev collectedEvidence

	queue := []error{err}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		if curr == nil {
			continue
		}

		if multi, ok := curr.(interface{ Unwrap() []error }); ok {
			queue = append(queue, multi.Unwrap()...)

			continue
		}

		if single, ok := curr.(interface{ Unwrap() error }); ok {
			queue = append(queue, single.Unwrap())
		}

		inspectErrorNode(curr, &ev)
	}

	return ev
}

func inspectErrorNode(curr error, ev *collectedEvidence) {
	if applied, ok := errors.AsType[*appliedError](curr); ok {
		if ev.runtimeID == "" {
			ev.runtimeID = applied.runtimeID
		}

		ev.hasUnknown = true

		return
	}

	outcome, found := extractOutcome(curr)

	switch {
	case found:
		if ev.runtimeID == "" {
			ev.runtimeID = firstCreatedID(outcome.Created)
		}

		switch outcome.Effect {
		case gotmux.EffectNotSent:
			ev.hasNotSent = true
		case gotmux.EffectUnspecified, gotmux.EffectUnknown:
			ev.hasUnknown = true
		case gotmux.EffectConfirmed:
			ev.hasConfirmed = true
		case gotmux.EffectRejected:
			ev.hasRejected = true
		default:
			ev.unrecognized = true
		}
	case isPreDispatchRefusal(curr):
		ev.hasNotSent = true
	default:
		ev.unrecognized = true
	}
}

func extractOutcome(err error) (gotmux.Outcome, bool) {
	if op, ok := errors.AsType[*gotmux.OperationError](err); ok {
		return op.Outcome, true
	}

	if cmd, ok := errors.AsType[*gotmux.CommandError](err); ok {
		return cmd.Outcome, true
	}

	return gotmux.Outcome{Effect: gotmux.EffectNotSent, Steps: nil, Created: nil}, false
}

func isPreDispatchRefusal(err error) bool {
	return errors.Is(err, ErrWorkingDirNotFound) ||
		errors.Is(err, ErrSessionCollision) ||
		errors.Is(err, ErrGroupCollision) ||
		errors.Is(err, gotmux.ErrInvalidHandle) ||
		errors.Is(err, gotmux.ErrInvalidArgument) ||
		errors.Is(err, gotmux.ErrNotInsideTmux)
}

func firstCreatedID(created []gotmux.CreatedObject) string {
	for _, object := range created {
		if object.RawID != "" {
			return object.RawID
		}
	}

	return ""
}

func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), dispatchTimeout)
}
