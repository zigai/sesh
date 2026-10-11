//go:build linux || darwin

package restore

import (
	"errors"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
)

func TestMutationUnspecifiedEffectPreservesUncertainty(t *testing.T) {
	t.Parallel()

	unspecified := &gotmux.OperationError{Outcome: gotmux.Outcome{}}

	tests := []struct {
		name string
		err  error
	}{
		{name: "isolated", err: unspecified},
		{name: "joined not sent", err: errors.Join(unspecified, &gotmux.OperationError{Outcome: gotmux.Outcome{Effect: gotmux.EffectNotSent}})},
		{name: "joined confirmed", err: errors.Join(unspecified, &gotmux.OperationError{Outcome: gotmux.Outcome{Effect: gotmux.EffectConfirmed}})},
		{name: "joined rejected", err: errors.Join(unspecified, &gotmux.OperationError{Outcome: gotmux.Outcome{Effect: gotmux.EffectRejected}})},
		{name: "command error", err: &gotmux.CommandError{Outcome: gotmux.Outcome{}}},
		{name: "unrecognized effect", err: &gotmux.OperationError{Outcome: gotmux.Outcome{Effect: gotmux.Effect(255)}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome := classifyMutation(tt.err)
			if outcome.Effect != gotmux.EffectUnknown || outcome.RuntimeID != "" {
				t.Fatalf("mutation outcome = %+v, want unknown effect without runtime identity", outcome)
			}
		})
	}
}

func TestMutationUnspecifiedEffectPreservesCreatedIdentity(t *testing.T) {
	t.Parallel()

	err := &gotmux.OperationError{Outcome: gotmux.Outcome{Created: []gotmux.CreatedObject{{RawID: "$7"}}}}

	outcome := classifyMutation(err)
	if outcome.Effect != gotmux.EffectUnknown || outcome.RuntimeID != "$7" {
		t.Fatalf("mutation outcome = %+v, want unknown effect with created identity $7", outcome)
	}
}
