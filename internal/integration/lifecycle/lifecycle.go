package lifecycle

import (
	"errors"
	"time"
)

const (
	CoverageNone              Coverage = "none"
	CoverageOrderedStop       Coverage = "ordered_stop"
	CoverageEarlyNotification Coverage = "early_notification"
)

const (
	EventShutdown  EventKind = "shutdown"
	EventSleep     EventKind = "sleep"
	EventLogout    EventKind = "logout"
	EventTerminate EventKind = "terminate"
)

const maxDrainSteps = 6

const (
	BudgetSourceDeadline = "os_deadline"
	BudgetSourceCanceled = "canceled"
	BudgetSourceMaximum  = "local_maximum"
)

const DefaultFinalSaveMaximum = 20 * time.Second

const MinimumFinalSaveWindow = 250 * time.Millisecond

const (
	StepStopScheduled    Step = "stop_scheduled_autosave"
	StepFinalSave        Step = "final_save"
	StepReconcile        Step = "reconcile_result"
	StepReportIncomplete Step = "report_incomplete"
	StepReleaseInhibitor Step = "release_inhibitor"
	StepRearm            Step = "rearm"
)

const (
	OrderingGuaranteed   StopOrder = "guaranteed"
	OrderingCrossManager StopOrder = "cross_manager"
	OrderingReversed     StopOrder = "reversed"
	OrderingUnknown      StopOrder = "unknown"
)

var ErrCanceledCycle = errors.New("lifecycle cycle was canceled")

type Coverage string

type EventKind string

type Step string

type StopOrder string

type Event struct {
	Kind     EventKind
	Cycle    uint64
	Deadline time.Time
	Canceled bool
}

type Budget struct {
	Remaining    time.Duration
	Source       string
	Insufficient bool
	Unbounded    bool
}

type Facts struct {
	GOOS               string
	Manager            string
	InhibitorAvailable bool
	ManagedTmuxService bool
	SameUserManager    bool
	StopOrderDeclared  bool
	SaverAfterTmux     bool
}

type Report struct {
	GOOS              string
	Manager           string
	Coverage          Coverage
	OrderedStop       bool
	EarlyNotification bool
	Gaps              []string
}

func EvaluateBudget(now time.Time, event Event, required time.Duration, maximum time.Duration) Budget {
	if event.Canceled {
		return Budget{Remaining: 0, Source: BudgetSourceCanceled, Insufficient: true, Unbounded: false}
	}

	if maximum <= 0 {
		maximum = DefaultFinalSaveMaximum
	}

	if event.Deadline.IsZero() {
		return Budget{Remaining: maximum, Source: BudgetSourceMaximum, Unbounded: true, Insufficient: false}
	}

	remaining := event.Deadline.Sub(now)
	if remaining <= 0 {
		return Budget{Remaining: 0, Source: BudgetSourceDeadline, Insufficient: true, Unbounded: false}
	}

	if remaining > maximum {
		remaining = maximum
	}

	return Budget{
		Remaining:    remaining,
		Source:       BudgetSourceDeadline,
		Insufficient: remaining < required,
		Unbounded:    false,
	}
}

func PlanDrain(event Event, budget Budget, inhibitHeld bool) []Step {
	steps := make([]Step, 0, maxDrainSteps)

	if event.Canceled {
		if inhibitHeld {
			steps = append(steps, StepReleaseInhibitor)
		}

		return append(steps, StepRearm)
	}

	steps = append(steps, StepStopScheduled, StepFinalSave, StepReconcile)

	if budget.Insufficient {
		steps = append(steps, StepReportIncomplete)
	}

	if inhibitHeld {
		steps = append(steps, StepReleaseInhibitor)
	}

	return steps
}

func EvaluateStopOrder(saverAfterTmux bool, sameManager bool, declared bool) StopOrder {
	if !declared {
		return OrderingUnknown
	}

	if !sameManager {
		return OrderingCrossManager
	}

	if saverAfterTmux {
		return OrderingGuaranteed
	}

	return OrderingReversed
}

func EvaluateReport(facts Facts) Report {
	report := Report{
		GOOS:              facts.GOOS,
		Manager:           facts.Manager,
		Coverage:          CoverageNone,
		OrderedStop:       false,
		EarlyNotification: false,
		Gaps:              nil,
	}

	order := EvaluateStopOrder(facts.SaverAfterTmux, facts.SameUserManager, facts.StopOrderDeclared)
	report.OrderedStop = facts.ManagedTmuxService && order == OrderingGuaranteed

	if report.OrderedStop {
		report.Coverage = CoverageOrderedStop
	}

	if facts.InhibitorAvailable {
		report.Coverage = CoverageEarlyNotification
		report.EarlyNotification = true
	}

	switch {
	case !facts.ManagedTmuxService:
		report.Gaps = append(report.Gaps, "unmanaged tmux servers receive no ordered final save")
	case order == OrderingCrossManager:
		report.Gaps = append(report.Gaps, "saver and tmux units are in different service managers, which creates no stop ordering")
	case order == OrderingReversed:
		report.Gaps = append(report.Gaps, "the tmux unit stops before the saver, so the final save runs after the saved processes")
	case order == OrderingUnknown:
		report.Gaps = append(report.Gaps, "saver and tmux unit stop ordering was not established")
	}

	if !facts.InhibitorAvailable {
		report.Gaps = append(report.Gaps, "no early shutdown or sleep notification: only the managed unit's own stop work is covered")
	}

	return report
}

func (r Report) Covers(required Coverage) bool {
	switch required {
	case CoverageNone:
		return true
	case CoverageOrderedStop:
		return r.OrderedStop || r.EarlyNotification
	case CoverageEarlyNotification:
		return r.EarlyNotification
	default:
		return false
	}
}
