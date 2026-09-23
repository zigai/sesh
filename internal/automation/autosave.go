//go:build linux || darwin

package automation

import (
	"errors"
	"time"
)

const DefaultInterval = 3 * time.Minute

const (
	ReasonDisabled           = "disabled"
	ReasonNeverRun           = "never_run"
	ReasonIntervalElapsed    = "interval_elapsed"
	ReasonNotDue             = "not_due"
	ReasonHistoryUnavailable = "history_unavailable"
)

const (
	CompletionSaved     = "saved"
	CompletionUnsaved   = "unsaved"
	CompletionUncertain = "uncertain"
)

const (
	ReasonDurableGeneration = "durable_generation"
	ReasonGenerationMissing = "generation_missing"
	ReasonSaveFailed        = "save_failed"
	ReasonPublicationUnsafe = "publication_uncertain"
)

var ErrDisabled = errors.New("autosave is disabled")

type DueDecision struct {
	Due     bool
	Reason  string
	Elapsed time.Duration
}

type DueState struct {
	Enabled           bool
	Interval          time.Duration
	Now               time.Time
	LastCompletedUnix int64
	HistoryKnown      bool
}

type SaveCompletion struct {
	Attempt    string
	Generation string
	Status     string
	Uncertain  bool
}

type CompletionDecision struct {
	Record bool
	Status string
	Reason string
}

func EffectiveInterval(configured time.Duration) time.Duration {
	if configured <= 0 {
		return DefaultInterval
	}

	return configured
}

func EvaluateDueState(state DueState) DueDecision {
	if !state.Enabled {
		return DueDecision{Due: false, Reason: ReasonDisabled, Elapsed: 0}
	}

	if !state.HistoryKnown {
		return DueDecision{Due: false, Reason: ReasonHistoryUnavailable, Elapsed: 0}
	}

	interval := EffectiveInterval(state.Interval)
	if state.LastCompletedUnix <= 0 {
		return DueDecision{Due: true, Reason: ReasonNeverRun, Elapsed: 0}
	}

	elapsed := max(state.Now.Sub(time.Unix(state.LastCompletedUnix, 0)), 0)

	if elapsed >= interval {
		return DueDecision{Due: true, Reason: ReasonIntervalElapsed, Elapsed: elapsed}
	}

	return DueDecision{Due: false, Reason: ReasonNotDue, Elapsed: elapsed}
}

func EvaluateSaveCompletion(result SaveCompletion, err error) CompletionDecision {
	if err != nil {
		if result.Uncertain {
			return CompletionDecision{Record: false, Status: CompletionUncertain, Reason: ReasonPublicationUnsafe}
		}

		return CompletionDecision{Record: false, Status: CompletionUnsaved, Reason: ReasonSaveFailed}
	}

	if result.Generation == "" {
		return CompletionDecision{Record: false, Status: CompletionUnsaved, Reason: ReasonGenerationMissing}
	}

	return CompletionDecision{Record: true, Status: CompletionSaved, Reason: ReasonDurableGeneration}
}
