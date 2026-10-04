package automation_test

import (
	"errors"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/automation"
)

var (
	errSaveFailed        = errors.New("save failed")
	errUncertainPublish  = errors.New("publication uncertain")
	errUnexpectedTrigger = errors.New("unexpected trigger")
)

func TestEvaluateDueState_Decisions(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)

	cases := []struct {
		name         string
		state        automation.DueState
		wantDue      bool
		wantReason   string
		wantElapsed  time.Duration
		elapsedCheck bool
	}{
		{
			name:       "disabled",
			state:      automation.DueState{Enabled: false, Interval: 3 * time.Minute, Now: now, LastCompletedUnix: now.Unix(), HistoryKnown: true},
			wantReason: "disabled",
		},
		{
			name:       "history unavailable",
			state:      automation.DueState{Enabled: true, Interval: 3 * time.Minute, Now: now, LastCompletedUnix: 0, HistoryKnown: false},
			wantReason: "history_unavailable",
		},
		{
			name:       "never run",
			state:      automation.DueState{Enabled: true, Interval: 3 * time.Minute, Now: now, LastCompletedUnix: 0, HistoryKnown: true},
			wantDue:    true,
			wantReason: "never_run",
		},
		{
			name:         "interval elapsed",
			state:        automation.DueState{Enabled: true, Interval: 3 * time.Minute, Now: now, LastCompletedUnix: now.Add(-5 * time.Minute).Unix(), HistoryKnown: true},
			wantDue:      true,
			wantReason:   "interval_elapsed",
			wantElapsed:  5 * time.Minute,
			elapsedCheck: true,
		},
		{
			name:       "not due",
			state:      automation.DueState{Enabled: true, Interval: 3 * time.Minute, Now: now, LastCompletedUnix: now.Add(-time.Minute).Unix(), HistoryKnown: true},
			wantReason: "not_due",
		},
		{
			name:       "exactly due",
			state:      automation.DueState{Enabled: true, Interval: 3 * time.Minute, Now: now, LastCompletedUnix: now.Add(-3 * time.Minute).Unix(), HistoryKnown: true},
			wantDue:    true,
			wantReason: "interval_elapsed",
		},
		{
			name:       "zero interval uses default",
			state:      automation.DueState{Enabled: true, Interval: 0, Now: now, LastCompletedUnix: now.Add(-4 * time.Minute).Unix(), HistoryKnown: true},
			wantDue:    true,
			wantReason: "interval_elapsed",
		},
		{
			name:       "future clock clamped",
			state:      automation.DueState{Enabled: true, Interval: 3 * time.Minute, Now: now, LastCompletedUnix: now.Add(10 * time.Minute).Unix(), HistoryKnown: true},
			wantReason: "not_due",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			decision := automation.EvaluateDueState(testCase.state)

			if decision.Due != testCase.wantDue {
				t.Fatalf("Due = %t, want %t", decision.Due, testCase.wantDue)
			}

			if decision.Reason != testCase.wantReason {
				t.Fatalf("Reason = %q, want %q", decision.Reason, testCase.wantReason)
			}

			if decision.Elapsed < 0 {
				t.Fatalf("Elapsed = %s, want non-negative", decision.Elapsed)
			}

			if testCase.elapsedCheck && decision.Elapsed != testCase.wantElapsed {
				t.Fatalf("Elapsed = %s, want %s", decision.Elapsed, testCase.wantElapsed)
			}
		})
	}
}

func TestEvaluateSaveCompletion_RecordsOnlyDurablePublication(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		result     automation.SaveCompletion
		err        error
		wantRecord bool
		wantStatus string
		wantReason string
	}{
		{
			name:       "durable partial generation still records completion",
			result:     automation.SaveCompletion{Attempt: "a", Generation: "g", Status: "partial"},
			wantRecord: true,
			wantStatus: automation.CompletionSaved,
			wantReason: automation.ReasonDurableGeneration,
		},
		{
			name:       "failed save does not record",
			result:     automation.SaveCompletion{Attempt: "a", Status: "failed"},
			err:        errSaveFailed,
			wantStatus: automation.CompletionUnsaved,
			wantReason: automation.ReasonSaveFailed,
		},
		{
			name:       "uncertain publication does not record",
			result:     automation.SaveCompletion{Attempt: "a", Generation: "g", Status: "failed", Uncertain: true},
			err:        errUncertainPublish,
			wantStatus: automation.CompletionUncertain,
			wantReason: automation.ReasonPublicationUnsafe,
		},
		{
			name:       "missing generation is not durable completion",
			result:     automation.SaveCompletion{Attempt: "a", Status: "partial"},
			wantStatus: automation.CompletionUnsaved,
			wantReason: automation.ReasonGenerationMissing,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			decision := automation.EvaluateSaveCompletion(testCase.result, testCase.err)

			if decision.Record != testCase.wantRecord {
				t.Fatalf("Record = %t, want %t", decision.Record, testCase.wantRecord)
			}

			if decision.Status != testCase.wantStatus {
				t.Fatalf("Status = %q, want %q", decision.Status, testCase.wantStatus)
			}

			if decision.Reason != testCase.wantReason {
				t.Fatalf("Reason = %q, want %q", decision.Reason, testCase.wantReason)
			}
		})
	}
}

func TestEffectiveInterval(t *testing.T) {
	t.Parallel()

	if got := automation.EffectiveInterval(0); got != automation.DefaultInterval {
		t.Fatalf("EffectiveInterval(0) = %v, want %v", got, automation.DefaultInterval)
	}

	if got := automation.EffectiveInterval(-5 * time.Minute); got != automation.DefaultInterval {
		t.Fatalf("EffectiveInterval(-5m) = %v, want %v", got, automation.DefaultInterval)
	}

	if got := automation.EffectiveInterval(7 * time.Minute); got != 7*time.Minute {
		t.Fatalf("EffectiveInterval(7m) = %v, want %v", got, 7*time.Minute)
	}
}

func TestMaintenanceDueOncePerCalendarDay(t *testing.T) {
	t.Parallel()

	location := time.FixedZone("test", 2*60*60)
	morning := time.Date(2026, 9, 25, 9, 0, 0, 0, location)

	cases := []struct {
		name     string
		previous int64
		now      time.Time
		want     bool
	}{
		{name: "no previous save", previous: 0, now: morning, want: true},
		{name: "earlier the same day", previous: morning.Add(-2 * time.Hour).Unix(), now: morning, want: false},
		{name: "just before midnight", previous: time.Date(2026, 9, 24, 23, 59, 0, 0, location).Unix(), now: morning, want: true},
		{name: "same day across utc midnight", previous: time.Date(2026, 9, 25, 1, 30, 0, 0, location).Unix(), now: morning, want: false},
	}

	for _, testCase := range cases {
		if got := automation.MaintenanceDue(testCase.previous, testCase.now); got != testCase.want {
			t.Errorf("%s: MaintenanceDue() = %t, want %t", testCase.name, got, testCase.want)
		}
	}
}
