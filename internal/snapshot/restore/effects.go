//go:build linux || darwin

package restore

import (
	"context"
	"errors"
	"time"

	"github.com/zigai/gotmux/tmux"
)

const (
	dispatchTimeout = 15 * time.Second
)

type mutationOutcome struct {
	Effect    tmux.Effect
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
		return mutationOutcome{Effect: tmux.Confirmed, RuntimeID: ""}
	}

	evidence := collectEvidence(err)

	if evidence.hasUnknown || evidence.unrecognized {
		return mutationOutcome{Effect: tmux.Unknown, RuntimeID: evidence.runtimeID}
	}

	if evidence.runtimeID != "" || evidence.hasConfirmed {
		return mutationOutcome{Effect: tmux.Confirmed, RuntimeID: evidence.runtimeID}
	}

	if evidence.hasRejected {
		return mutationOutcome{Effect: tmux.Rejected, RuntimeID: ""}
	}

	if evidence.hasNotSent && !evidence.hasUnknown && !evidence.unrecognized {
		return mutationOutcome{Effect: tmux.NotSent, RuntimeID: ""}
	}

	return mutationOutcome{Effect: tmux.Unknown, RuntimeID: evidence.runtimeID}
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
		if id := firstCreatedID(outcome.Created); id != "" && ev.runtimeID == "" {
			ev.runtimeID = id
		}

		switch outcome.Effect {
		case tmux.NotSent:
			ev.hasNotSent = true
		case tmux.Unknown:
			ev.hasUnknown = true
		case tmux.Confirmed:
			ev.hasConfirmed = true
		case tmux.Rejected:
			ev.hasRejected = true
		}
	case isPreDispatchRefusal(curr):
		ev.hasNotSent = true
	default:
		ev.unrecognized = true
	}
}

func extractOutcome(err error) (tmux.Outcome, bool) {
	if op, ok := errors.AsType[*tmux.OperationError](err); ok {
		return op.Outcome, true
	}

	if cmd, ok := errors.AsType[*tmux.CommandError](err); ok {
		return cmd.Outcome, true
	}

	return tmux.Outcome{Effect: tmux.NotSent, Steps: nil, Created: nil}, false
}

func isPreDispatchRefusal(err error) bool {
	return errors.Is(err, ErrWorkingDirNotFound) ||
		errors.Is(err, ErrSessionCollision) ||
		errors.Is(err, ErrGroupCollision) ||
		errors.Is(err, tmux.ErrInvalidHandle) ||
		errors.Is(err, tmux.ErrInvalidArgument) ||
		errors.Is(err, tmux.ErrNotInsideTmux)
}

func firstCreatedID(created []tmux.CreatedObject) string {
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
