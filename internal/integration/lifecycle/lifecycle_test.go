package lifecycle_test

import (
	"slices"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/integration/lifecycle"
)

func TestEvaluateBudgetRespectsTheOSDeadline(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	event := lifecycle.Event{Kind: lifecycle.EventShutdown, Cycle: 1, Deadline: now.Add(4 * time.Second)}

	budget := lifecycle.EvaluateBudget(now, event, time.Second, 30*time.Second)

	if budget.Remaining != 4*time.Second || budget.Insufficient || budget.Unbounded {
		t.Fatalf("budget = %+v, want the full four-second OS window", budget)
	}

	if budget.Source != lifecycle.BudgetSourceDeadline {
		t.Fatalf("source = %q, want %q", budget.Source, lifecycle.BudgetSourceDeadline)
	}
}

func TestEvaluateBudgetNeverExceedsTheLocalMaximum(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	event := lifecycle.Event{Kind: lifecycle.EventShutdown, Cycle: 1, Deadline: now.Add(time.Minute)}

	budget := lifecycle.EvaluateBudget(now, event, time.Second, 5*time.Second)

	if budget.Remaining != 5*time.Second {
		t.Fatalf("remaining = %s, want the five-second local maximum", budget.Remaining)
	}
}

func TestEvaluateBudgetReportsInsufficientWindows(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	event := lifecycle.Event{Kind: lifecycle.EventSleep, Cycle: 3, Deadline: now.Add(100 * time.Millisecond)}

	budget := lifecycle.EvaluateBudget(now, event, time.Second, 30*time.Second)

	if !budget.Insufficient || budget.Remaining != 100*time.Millisecond {
		t.Fatalf("budget = %+v, want an insufficient 100ms window", budget)
	}

	expired := lifecycle.EvaluateBudget(now, lifecycle.Event{Kind: lifecycle.EventSleep, Cycle: 4, Deadline: now.Add(-time.Second)}, time.Second, time.Second)
	if !expired.Insufficient || expired.Remaining != 0 {
		t.Fatalf("expired budget = %+v, want zero remaining and insufficient", expired)
	}
}

func TestEvaluateBudgetWithoutDeadlineUsesTheLocalMaximum(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)

	budget := lifecycle.EvaluateBudget(now, lifecycle.Event{Kind: lifecycle.EventTerminate, Cycle: 0}, time.Second, 7*time.Second)

	if budget.Remaining != 7*time.Second || !budget.Unbounded || budget.Insufficient {
		t.Fatalf("budget = %+v, want the seven-second local maximum", budget)
	}
}

func TestEvaluateBudgetCanceledCycleIsRefused(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	event := lifecycle.Event{Kind: lifecycle.EventShutdown, Cycle: 5, Deadline: now.Add(time.Minute), Canceled: true}

	budget := lifecycle.EvaluateBudget(now, event, time.Second, time.Second)

	if !budget.Insufficient || budget.Remaining != 0 || budget.Source != lifecycle.BudgetSourceCanceled {
		t.Fatalf("budget = %+v, want a canceled cycle", budget)
	}
}

func TestPlanDrainOrdersReleaseAfterTheDurableOutcome(t *testing.T) {
	t.Parallel()

	steps := lifecycle.PlanDrain(lifecycle.Event{Kind: lifecycle.EventShutdown, Cycle: 1}, lifecycle.Budget{Remaining: time.Second}, true)

	want := []lifecycle.Step{
		lifecycle.StepStopScheduled,
		lifecycle.StepFinalSave,
		lifecycle.StepReconcile,
		lifecycle.StepReleaseInhibitor,
	}

	if len(steps) != len(want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}

	for index, step := range want {
		if steps[index] != step {
			t.Fatalf("steps = %v, want %v", steps, want)
		}
	}
}

func TestPlanDrainReportsIncompleteResultsAndRearmsCanceledCycles(t *testing.T) {
	t.Parallel()

	insufficient := lifecycle.PlanDrain(
		lifecycle.Event{Kind: lifecycle.EventSleep, Cycle: 2},
		lifecycle.Budget{Remaining: 10 * time.Millisecond, Insufficient: true},
		false,
	)

	if !containsStep(insufficient, lifecycle.StepReportIncomplete) {
		t.Fatalf("steps = %v, want an explicit incomplete report", insufficient)
	}

	if containsStep(insufficient, lifecycle.StepReleaseInhibitor) {
		t.Fatalf("steps = %v, must not release an inhibitor that is not held", insufficient)
	}

	canceled := lifecycle.PlanDrain(
		lifecycle.Event{Kind: lifecycle.EventShutdown, Cycle: 3, Canceled: true},
		lifecycle.Budget{Source: lifecycle.BudgetSourceCanceled, Insufficient: true},
		true,
	)

	if containsStep(canceled, lifecycle.StepFinalSave) {
		t.Fatalf("steps = %v, a canceled cycle must not run a final save", canceled)
	}

	if !containsStep(canceled, lifecycle.StepReleaseInhibitor) || !containsStep(canceled, lifecycle.StepRearm) {
		t.Fatalf("steps = %v, want release then rearm", canceled)
	}
}

func containsStep(steps []lifecycle.Step, want lifecycle.Step) bool {
	return slices.Contains(steps, want)
}

func TestEvaluateStopOrder(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		saverAfterTmux bool
		sameManager    bool
		declared       bool
		want           lifecycle.StopOrder
	}{
		{name: "ordered in one manager", saverAfterTmux: true, sameManager: true, declared: true, want: lifecycle.OrderingGuaranteed},
		{name: "reversed", saverAfterTmux: false, sameManager: true, declared: true, want: lifecycle.OrderingReversed},
		{name: "cross manager", saverAfterTmux: true, sameManager: false, declared: true, want: lifecycle.OrderingCrossManager},
		{name: "undeclared", declared: false, want: lifecycle.OrderingUnknown},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := lifecycle.EvaluateStopOrder(testCase.saverAfterTmux, testCase.sameManager, testCase.declared)
			if got != testCase.want {
				t.Fatalf("EvaluateStopOrder = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestEvaluateReportKeepsGapsVisible(t *testing.T) {
	t.Parallel()

	ordered := lifecycle.EvaluateReport(lifecycle.Facts{
		GOOS:               "linux",
		Manager:            "systemd",
		ManagedTmuxService: true,
		SameUserManager:    true,
		StopOrderDeclared:  true,
		SaverAfterTmux:     true,
	})

	if !ordered.OrderedStop || ordered.Coverage != lifecycle.CoverageOrderedStop {
		t.Fatalf("report = %+v, want ordered-stop coverage", ordered)
	}

	if ordered.EarlyNotification {
		t.Fatalf("report = %+v, must not claim early notification without an inhibitor", ordered)
	}

	if len(ordered.Gaps) == 0 {
		t.Fatal("report must list the missing early-notification coverage")
	}

	if ordered.Covers(lifecycle.CoverageEarlyNotification) {
		t.Fatal("ordered stop must not satisfy an early-notification requirement")
	}

	unmanaged := lifecycle.EvaluateReport(lifecycle.Facts{GOOS: "linux", Manager: "systemd"})
	if unmanaged.OrderedStop || unmanaged.Coverage == lifecycle.CoverageOrderedStop {
		t.Fatalf("report = %+v, want no coverage for an unmanaged server", unmanaged)
	}

	inhibited := lifecycle.EvaluateReport(lifecycle.Facts{
		GOOS:               "linux",
		Manager:            "systemd",
		InhibitorAvailable: true,
	})

	if !inhibited.EarlyNotification || !inhibited.Covers(lifecycle.CoverageEarlyNotification) {
		t.Fatalf("report = %+v, want early-notification coverage", inhibited)
	}
}

func TestPlatformAdaptersReportFactsAndGaps(t *testing.T) {
	t.Parallel()

	facts := lifecycle.ObserveFacts(false)
	if facts.GOOS == "" {
		t.Fatal("ObserveFacts must report the current platform")
	}

	if facts.InhibitorAvailable {
		t.Fatal("ObserveFacts must not claim an inhibitor that was not supplied")
	}

	if len(lifecycle.PlatformGaps()) == 0 {
		t.Fatal("platform gaps must be reported explicitly")
	}

	report := lifecycle.EvaluateReport(facts)
	if report.GOOS != facts.GOOS || report.Manager != facts.Manager {
		t.Fatalf("report = %+v, want the observed platform facts", report)
	}
}

func TestObserveFactsUnmanagedRemainsUnknown(t *testing.T) {
	t.Parallel()

	facts := lifecycle.ObserveFacts(false)
	if facts.ManagedTmuxService || facts.StopOrderDeclared || facts.SaverAfterTmux || facts.SameUserManager {
		t.Fatalf("unbound facts must not report managed service or stop order: %+v", facts)
	}

	report := lifecycle.EvaluateReport(facts)
	if report.OrderedStop {
		t.Fatalf("unbound facts must not report ordered stop: %+v", report)
	}

	if !slices.Contains(report.Gaps, "unmanaged tmux servers receive no ordered final save") {
		t.Fatalf("report gaps = %v, want unmanaged gap", report.Gaps)
	}
}

func TestEvaluateStopOrderNegativeCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		saverAfterTmux bool
		sameManager    bool
		declared       bool
		want           lifecycle.StopOrder
	}{
		{name: "all false", saverAfterTmux: false, sameManager: false, declared: false, want: lifecycle.OrderingUnknown},
		{name: "different manager", saverAfterTmux: true, sameManager: false, declared: true, want: lifecycle.OrderingCrossManager},
		{name: "undeclared ordering", saverAfterTmux: true, sameManager: true, declared: false, want: lifecycle.OrderingUnknown},
		{name: "saver not after tmux", saverAfterTmux: false, sameManager: true, declared: true, want: lifecycle.OrderingReversed},
		{name: "valid bound ordering", saverAfterTmux: true, sameManager: true, declared: true, want: lifecycle.OrderingGuaranteed},
	}

	for _, tc := range cases {
		if got := lifecycle.EvaluateStopOrder(tc.saverAfterTmux, tc.sameManager, tc.declared); got != tc.want {
			t.Errorf("%s: EvaluateStopOrder(%v, %v, %v) = %q, want %q", tc.name, tc.saverAfterTmux, tc.sameManager, tc.declared, got, tc.want)
		}
	}
}
