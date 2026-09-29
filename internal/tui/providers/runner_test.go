package providers

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
)

func TestRunnerRejectsStaleResultAndCancelsInvalidatedRequest(t *testing.T) {
	var runner Runner

	ctx, first, ok := runner.Begin(t.Context(), Git)
	if !ok || first == 0 {
		t.Fatal("first git request did not start")
	}

	if _, _, ok := runner.Begin(t.Context(), Git); ok {
		t.Fatal("overlapping git request started")
	}

	runner.Invalidate(Git)

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("invalidated request context = %v", ctx.Err())
	}

	if runner.Complete(Git, first, Rows{"%1": {display.FieldAgentGitLabel: {Text: "old", State: layout.Present}}}) {
		t.Fatal("stale result replaced the snapshot")
	}

	if runner.Snapshot(Git).Version != 0 {
		t.Fatal("stale result published a version")
	}
}

func TestRunnerCompletesCurrentRequest(t *testing.T) {
	var runner Runner

	ctx, request, ok := runner.Begin(t.Context(), Git)
	if !ok || request == 0 {
		t.Fatal("git request did not start")
	}

	if !runner.Complete(Git, request, Rows{"%1": {display.FieldAgentGitLabel: {Text: "new", State: layout.Present}}}) {
		t.Fatal("current result was rejected")
	}

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("completed request context = %v", ctx.Err())
	}

	if got := runner.Cell(Git, "%1", display.FieldAgentGitLabel); got.Text != "new" || got.State != layout.Present {
		t.Fatalf("git cell = %+v", got)
	}
}

func TestRunnerPublishesVersionedCopiesAndCellPresence(t *testing.T) {
	var runner Runner
	if got := runner.Cell(Clock, "%1", display.FieldStatusAge).State; got != layout.Pending {
		t.Fatalf("unreported cell state = %v, want Pending", got)
	}

	rows := Rows{"%1": {display.FieldStatusAge: {Text: "1m", Role: "muted", Variants: []string{"1"}, State: layout.Present}}}
	first := runner.Publish(Clock, rows)

	rows["%1"][display.FieldStatusAge] = layout.Cell{Text: "changed", Role: "", Variants: nil, State: layout.Present}
	if got := runner.Cell(Clock, "%1", display.FieldStatusAge).Text; got != "1m" {
		t.Fatalf("published cell changed with caller map: %q", got)
	}

	snapshot := runner.Snapshot(Clock)

	snapshot.Rows["%1"][display.FieldStatusAge] = layout.Cell{Text: "mutated", Role: "", Variants: nil, State: layout.Present}
	if got := runner.Cell(Clock, "%1", display.FieldStatusAge).Text; got != "1m" {
		t.Fatalf("published cell changed with snapshot copy: %q", got)
	}

	if next := runner.Publish(Clock, Rows{"%1": {display.FieldStatusAge: {Text: "1m", Role: "muted", Variants: []string{"1"}, State: layout.Present}}}); next != first {
		t.Fatalf("unchanged publish advanced version: %d to %d", first, next)
	}

	if got := runner.Cell(Clock, "%2", display.FieldStatusAge).State; got != layout.Absent {
		t.Fatalf("missing row state = %v, want Absent", got)
	}

	runner.Close()
}

func TestProviderDefinitionsUseFieldRegistryAndCadence(t *testing.T) {
	git, ok := DefinitionFor(Git)
	if !ok || git.Cadence.Kind != CadencePoll || git.Cadence.Interval <= 0 {
		t.Fatalf("git definition = %+v", git)
	}

	if len(git.Fields) == 0 || !slices.Contains(git.Fields, display.FieldAgentGitLabel) {
		t.Fatalf("git fields = %v", git.Fields)
	}

	clock, ok := DefinitionFor(Clock)
	if !ok || clock.Cadence.Kind != CadenceClock || clock.Cadence.Interval <= 0 {
		t.Fatalf("clock definition = %+v", clock)
	}

	if !slices.Contains(clock.Fields, display.FieldStatusAge) {
		t.Fatalf("clock fields = %v", clock.Fields)
	}
}
