//go:build linux

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/integration/lifecycle"
)

var errPrivateLifecycle = errors.New("PRIVATE_LIFECYCLE_ERROR")

type loggingLifecycleRuntime struct {
	events []lifecycle.Event
}

func (r *loggingLifecycleRuntime) Next(context.Context) (lifecycle.Event, error) {
	if len(r.events) == 0 {
		return lifecycle.Event{}, context.Canceled
	}

	event := r.events[0]
	r.events = r.events[1:]

	return event, nil
}

func (*loggingLifecycleRuntime) Release() error              { return nil }
func (*loggingLifecycleRuntime) Rearm(context.Context) error { return nil }

func TestLifecycleLogsReadinessDrainRecordedFailureAndRecovery(t *testing.T) {
	cases := []struct {
		name     string
		events   []lifecycle.Event
		failure  error
		statuses []string
	}{
		{"idle", nil, nil, []string{"ok"}},
		{"canceled_before_drain", []lifecycle.Event{{Canceled: true}}, nil, []string{"ok"}},
		{"save", []lifecycle.Event{{Kind: lifecycle.EventSleep}}, nil, []string{"ok", "started"}},
		{"recorded_failure", []lifecycle.Event{{Kind: lifecycle.EventShutdown}}, errors.Join(ErrFinalRecorded, errPrivateLifecycle), []string{"ok", "started", "degraded"}},
		{"resumed", []lifecycle.Event{{Kind: lifecycle.EventSleep}, {Canceled: true}}, nil, []string{"ok", "started", "recovered"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer

			ctx := zerolog.New(&logs).WithContext(t.Context())
			newScheduler := func() (*automation.Scheduler, error) {
				return automation.NewScheduler(automation.SchedulerConfig{
					Interval: time.Hour, AttemptTimeout: time.Second, DrainGrace: time.Second,
					Attempt: func(context.Context, automation.Trigger) (automation.AttemptResult, error) {
						return automation.AttemptResult{}, tc.failure
					},
				})
			}

			scheduler, err := newScheduler()
			if err != nil {
				t.Fatal(err)
			}

			if err := RunLifecycle(ctx, CoordinatorConfig{
				Runtime: &loggingLifecycleRuntime{events: tc.events}, Scheduler: scheduler,
				NewScheduler: newScheduler, DrainGrace: time.Second,
			}); err != nil {
				t.Fatal(err)
			}

			lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
			if len(lines) != len(tc.statuses) || strings.Contains(logs.String(), "PRIVATE_LIFECYCLE_ERROR") {
				t.Fatalf("unexpected lifecycle records: %s", logs.String())
			}

			for i, line := range lines {
				assertLifecycleLogRecord(t, line, tc.statuses[i])
			}
		})
	}
}

func assertLifecycleLogRecord(t *testing.T, line []byte, status string) {
	t.Helper()

	var record map[string]any
	if err := json.Unmarshal(line, &record); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{"component": "daemon", "operation": "run_lifecycle", "status": status, "level": "info"}
	switch status {
	case "ok":
		want["phase"], want["message"] = "running", "lifecycle coordinator ready"
	case "started":
		want["phase"], want["message"] = "final_save", "lifecycle final save started"
	case "degraded":
		want["level"], want["reason"], want["recorded"] = "warn", "final_save_incomplete", true
		want["message"] = "lifecycle final save failure recorded"
	case "recovered":
		want["reason"], want["message"] = "lifecycle_canceled", "lifecycle coordinator resumed"
	}

	if diff := cmp.Diff(want, record); diff != "" {
		t.Fatalf("incorrect lifecycle record: %s", diff)
	}
}
