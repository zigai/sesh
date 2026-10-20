//go:build linux

package daemon

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"
)

func TestRuntimeStatusListenerLogsUnexpectedFailureAndKeepsNormalShutdownSilent(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal_shutdown", true: "listener_failure"}[fail], func(t *testing.T) {
			testRuntimeStatusListenerLogging(t, fail)
		})
	}
}

func testRuntimeStatusListenerLogging(t *testing.T, fail bool) {
	t.Helper()

	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())
	dir := shortRuntimeDir(t)

	reporter, err := StartRuntimeReporter(ctx, validRuntimeConfig(dir, filepath.Join(dir, "PRIVATE_ENDPOINT"), nil))
	if err != nil {
		t.Fatal(err)
	}

	if !fail {
		if err := reporter.Close(); err != nil {
			t.Fatal(err)
		}

		if logs.Len() != 0 {
			t.Fatalf("normal shutdown logged: %s", logs.String())
		}

		return
	}

	t.Cleanup(func() {
		if err := reporter.Close(); err != nil {
			t.Error(err)
		}
	})

	if err := reporter.listener.SetDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-reporter.done:
	case <-time.After(time.Second):
		t.Fatal("reporter did not observe listener failure")
	}

	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"level": "error", "component": "daemon", "operation": "serve_runtime_status", "status": "failed",
		"reason": "listener_failed", "message": "runtime status listener stopped unexpectedly", "error": record["error"],
	}
	if record["error"] == nil {
		t.Fatalf("listener failure lacks its cause: %#v", record)
	}

	if diff := cmp.Diff(want, record); diff != "" {
		t.Fatalf("incorrect listener failure: %s", diff)
	}
}
