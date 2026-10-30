//go:build linux || darwin

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/task"
)

var (
	errPrivateScan  = errors.New("PRIVATE_SCAN_FAILURE")
	errPrivateStore = errors.New("PRIVATE_STORE")
	errPrivateTmux  = errors.New("PRIVATE_TMUX")
)

func decodeRecords(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()

	var records []map[string]any

	for line := range strings.Lines(logs.String()) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}

		records = append(records, record)
	}

	return records
}

func runFinishTicks(t *testing.T, results ...error) []map[string]any {
	t.Helper()

	var logs bytes.Buffer

	ctx, cancel := context.WithCancel(zerolog.New(&logs).WithContext(t.Context()))
	ticks := make(chan time.Time)
	done := make(chan struct{})
	scans := 0

	go func() {
		finishOnTicks(ctx, ticks, func(context.Context, map[string]bool) error {
			scans = min(scans+1, len(results))

			return results[scans-1]
		})
		close(done)
	}()

	// The loop takes the extra tick only after it logged the last result, and repeating that
	// result logs nothing new.
	for range len(results) + 1 {
		ticks <- time.Now()
	}

	cancel()
	<-done

	return decodeRecords(t, &logs)
}

var (
	scanFailed = map[string]any{
		"level": "warn", "component": "task", "operation": "finish_merged_tasks", "status": "degraded",
		"reason": "task_scan_failed", "message": "periodic merged task finish failed", "error": errPrivateScan.Error(),
	}
	scanRecovered = map[string]any{
		"level": "info", "component": "task", "operation": "finish_merged_tasks", "status": "recovered",
		"message": "periodic merged task finish recovered",
	}
)

func TestRepeatedMergedTaskScanFailuresLogOneRecordPerStreak(t *testing.T) {
	records := runFinishTicks(t, errPrivateScan, errPrivateScan, nil, errPrivateScan, nil)

	if !reflect.DeepEqual(records, []map[string]any{scanFailed, scanRecovered, scanFailed, scanRecovered}) {
		t.Errorf("records = %#v, want a degraded and a recovered record for each of the two failing streaks", records)
	}
}

func TestSuccessfulMergedTaskScansLogNothing(t *testing.T) {
	if records := runFinishTicks(t, nil, nil); len(records) != 0 {
		t.Errorf("records = %#v, want none", records)
	}
}

func TestMergedTaskFinishOutcomesLogTheirReasonWithoutDetails(t *testing.T) {
	cases := []struct {
		err   error
		level string
		want  map[string]any
	}{
		{nil, "info", map[string]any{"status": "ok", "message": "finished a task whose pull request merged"}},
		{
			fmt.Errorf("PRIVATE_TASK: %w", &task.LossError{UnmergedCommits: 1, ChangedFiles: 0, ClosedUnmerged: false}), "warn",
			map[string]any{"status": "degraded", "reason": "would_lose_work"},
		},
		{&task.FinishRunError{ExitCode: 3}, "warn", map[string]any{"status": "degraded", "reason": "finish_run_failed"}},
		{
			errors.Join(errWorkNotFinished, errPrivateStore), "warn",
			map[string]any{"status": "degraded", "reason": "work_not_finished"},
		},
		{errPrivateTmux, "warn", map[string]any{"status": "degraded", "reason": "finish_failed"}},
	}

	for _, test := range cases {
		var logs bytes.Buffer

		logMergedTaskFinish(zerolog.New(&logs).WithContext(t.Context()), "task-1", test.err)

		want := map[string]any{"level": test.level, "component": "task", "operation": "finish_merged_task", "task_id": "task-1"}
		maps.Copy(want, test.want)

		if want["reason"] != nil {
			want["message"] = "task whose pull request merged was not finished"
			want["error"] = test.err.Error()
		}

		if records := decodeRecords(t, &logs); len(records) != 1 || !reflect.DeepEqual(records[0], want) {
			t.Errorf("logMergedTaskFinish(%v) records = %#v, want only %#v", test.err, records, want)
		}
	}
}
