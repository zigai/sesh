//go:build linux || darwin

package restore

import (
	"errors"
	"testing"

	"github.com/zigai/gotmux/tmux"
)

func TestMutationUnspecifiedEffectPreservesUncertainty(t *testing.T) {
	t.Parallel()

	unspecified := &tmux.OperationError{Outcome: tmux.Outcome{}}

	tests := []struct {
		name string
		err  error
	}{
		{name: "isolated", err: unspecified},
		{name: "joined not sent", err: errors.Join(unspecified, &tmux.OperationError{Outcome: tmux.Outcome{Effect: tmux.EffectNotSent}})},
		{name: "joined confirmed", err: errors.Join(unspecified, &tmux.OperationError{Outcome: tmux.Outcome{Effect: tmux.EffectConfirmed}})},
		{name: "joined rejected", err: errors.Join(unspecified, &tmux.OperationError{Outcome: tmux.Outcome{Effect: tmux.EffectRejected}})},
		{name: "command error", err: &tmux.CommandError{Outcome: tmux.Outcome{}}},
		{name: "unrecognized effect", err: &tmux.OperationError{Outcome: tmux.Outcome{Effect: tmux.Effect(255)}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome := classifyMutation(tt.err)
			if outcome.Effect != tmux.EffectUnknown || outcome.RuntimeID != "" {
				t.Fatalf("mutation outcome = %+v, want unknown effect without runtime identity", outcome)
			}
		})
	}
}

func TestMutationUnspecifiedEffectPreservesCreatedIdentity(t *testing.T) {
	t.Parallel()

	err := &tmux.OperationError{Outcome: tmux.Outcome{Created: []tmux.CreatedObject{{RawID: "$7"}}}}

	outcome := classifyMutation(err)
	if outcome.Effect != tmux.EffectUnknown || outcome.RuntimeID != "$7" {
		t.Fatalf("mutation outcome = %+v, want unknown effect with created identity $7", outcome)
	}
}
