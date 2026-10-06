package errorstep

import (
	"errors"
	"fmt"
	"testing"

	"github.com/samber/oops"
)

var errCause = errors.New("cause")

func TestOwnedFindsTheDeepestStepOfTheDomain(t *testing.T) {
	inner := oops.In("restore").With("operation", "import_options", "reason", "option_set_outcome_unknown").Wrap(errCause)
	outer := oops.In("restore").With("operation", "import_scoped_state", "reason", "import_failed_uncertain").
		Wrap(errors.Join(errCause, inner))

	tests := []struct {
		name string
		err  error
		want Step
		ok   bool
	}{
		{name: "nil error", err: nil},
		{name: "plain error", err: errCause},
		{name: "other domain", err: oops.In("cli").With("operation", "open_tmux").Wrap(errCause)},
		{name: "domain without operation", err: oops.In("restore").With("reason", "unused").Wrap(errCause)},
		{name: "single step", err: inner, want: Step{Operation: "import_options", Reason: "option_set_outcome_unknown"}, ok: true},
		{name: "step inside joined error", err: outer, want: Step{Operation: "import_options", Reason: "option_set_outcome_unknown"}, ok: true},
		{name: "step under plain wrapping", err: fmt.Errorf("context: %w", inner), want: Step{Operation: "import_options", Reason: "option_set_outcome_unknown"}, ok: true},
		{
			name: "first joined branch holding an oops error wins",
			err: errors.Join(
				errCause,
				oops.In("restore").With("operation", "list_live_sessions").Wrap(errCause),
				fmt.Errorf("context: %w", inner),
			),
			want: Step{Operation: "list_live_sessions"},
			ok:   true,
		},
		{name: "operation without reason", err: oops.In("restore").With("operation", "list_live_sessions").Wrap(errCause), want: Step{Operation: "list_live_sessions"}, ok: true},
		{
			name: "deeper layer of another domain is ignored",
			err:  oops.In("restore").With("operation", "open_tmux").Wrap(oops.In("cli").With("operation", "list_live_sessions").Wrap(errCause)),
			want: Step{Operation: "open_tmux"},
			ok:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Owned(tt.err, "restore")
			if got != tt.want || ok != tt.ok {
				t.Fatalf("Owned = %+v, %t; want %+v, %t", got, ok, tt.want, tt.ok)
			}
		})
	}
}
