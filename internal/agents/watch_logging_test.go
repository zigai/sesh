//go:build linux || darwin

package agents_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"
	"github.com/zigai/aht/v2/pkg/aht"

	"github.com/zigai/sesh/internal/agents"
)

type recoveringWatchSource struct {
	failures int
	calls    int
	cancel   context.CancelFunc
}

type recoveringSnapshotHandler struct {
	failures int
	calls    int
}

func (h *recoveringSnapshotHandler) HandleSnapshot(context.Context, []aht.Session) error {
	h.calls++
	if h.calls <= h.failures {
		return errWatchUnavailable
	}

	return nil
}

func (*recoveringSnapshotHandler) ClearAll(context.Context) error { return nil }

func (*recoveringWatchSource) List(context.Context, aht.Filter) ([]aht.Session, error) {
	return nil, nil
}

func (s *recoveringWatchSource) Watch(_ context.Context, _ aht.Filter, yield func(aht.StateSnapshot) error) error {
	s.calls++
	if s.calls <= s.failures {
		return errWatchUnavailable
	}

	if err := yield(aht.StateSnapshot{}); err != nil {
		return err
	}

	if err := yield(aht.StateSnapshot{}); err != nil {
		return err
	}

	s.cancel()

	return nil
}

func TestWatchLogsOneDisconnectAndRecoveryPerOutage(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "quiet"}[quiet], func(t *testing.T) {
			var logs bytes.Buffer

			ctx, cancel := context.WithCancel(zerolog.New(&logs).WithContext(t.Context()))
			defer cancel()

			source := &recoveringWatchSource{failures: 3, cancel: cancel}
			opts := agents.DefaultWatchOptions()
			opts.InitialBackoff, opts.MaxBackoff, opts.Quiet = time.Nanosecond, time.Nanosecond, quiet
			agents.RunWatchLoop(ctx, source, &recordingHandler{}, opts)

			records := decodeLogRecords(t, &logs)

			wantCount := 3
			if quiet {
				wantCount = 1
			}

			if len(records) != wantCount || source.calls != 4 || strings.Contains(logs.String(), errWatchUnavailable.Error()) {
				t.Fatalf("incorrect outage records: %s", logs.String())
			}

			if quiet && records[0]["status"] != "disconnected" {
				t.Fatal("quiet mode must retain the disconnect warning")
			}

			for _, record := range records {
				assertWatchLogRecord(t, record)
			}
		})
	}
}

func assertWatchLogRecord(t *testing.T, record map[string]any) {
	t.Helper()

	want := map[string]any{"component": "agents", "operation": "watch", "status": record["status"]}
	switch record["status"] {
	case "started":
		want["level"], want["operation"], want["message"] = "info", "watch_loop", "agent status watch loop started"
	case "disconnected":
		want["level"], want["reason"], want["message"] = "warn", "watch_failed", "AHT broker watch disconnected, will retry"

		if _, ok := record["retry_in"].(float64); !ok {
			t.Fatal("retry delay must be numeric")
		}

		want["retry_in"] = record["retry_in"]
	case "recovered":
		want["level"], want["reason"], want["message"] = "info", "broker_available", "AHT broker watch recovered"
	}

	if diff := cmp.Diff(want, record); diff != "" {
		t.Fatalf("incorrect watch record: %s", diff)
	}
}

func TestWatchDistinguishesSnapshotApplicationFailuresFromBrokerOutages(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "quiet"}[quiet], func(t *testing.T) {
			var logs bytes.Buffer

			ctx, cancel := context.WithCancel(zerolog.New(&logs).WithContext(t.Context()))
			defer cancel()

			source := &recoveringWatchSource{cancel: cancel}
			handler := &recoveringSnapshotHandler{failures: 3}
			opts := agents.WatchOptions{InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond, Quiet: quiet}
			agents.RunWatchLoop(ctx, source, handler, opts)

			records := decodeLogRecords(t, &logs)
			if !quiet {
				assertWatchLogRecord(t, records[0])
				records = records[1:]
			}

			assertSnapshotApplicationLogs(t, records, quiet)
		})
	}
}

func assertSnapshotApplicationLogs(t *testing.T, records []map[string]any, quiet bool) {
	t.Helper()

	want := []map[string]any{{
		"level": "warn", "component": "agents", "operation": "apply_snapshot", "status": "degraded",
		"reason": "snapshot_apply_failed", "message": "agent status snapshot could not be applied",
	}}
	if !quiet {
		want = append(want, map[string]any{
			"level": "info", "component": "agents", "operation": "apply_snapshot", "status": "recovered",
			"reason": "snapshot_applied", "message": "agent status snapshot application recovered",
		})
	}

	if diff := cmp.Diff(want, records); diff != "" {
		t.Fatalf("incorrect snapshot application records: %s", diff)
	}
}
