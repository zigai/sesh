//go:build linux || darwin

package doctor_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/doctor"
)

func TestDaemonRuntimeCheckRequiresRunningPhaseAndHeldInhibitors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, state, phase     string
		held                   bool
		wantStatus, wantReason string
	}{
		{"ready", "ready", "running", true, doctor.StatusOK, ""},
		{"released", "not_ready", "running", false, doctor.StatusWarning, doctor.ReasonInhibitorReleased},
		{"initializing", "not_ready", "initializing", true, doctor.StatusWarning, doctor.ReasonInitializing},
		{"contradictory_ready_phase", "ready", "initializing", true, doctor.StatusWarning, doctor.ReasonInitializing},
		{"contradictory_ready_locks", "ready", "running", false, doctor.StatusWarning, doctor.ReasonInhibitorReleased},
		{"draining", "not_ready", "draining", true, doctor.StatusWarning, doctor.ReasonLifecycleActive},
		{"final_save", "not_ready", "final_save", true, doctor.StatusWarning, doctor.ReasonLifecycleActive},
		{"stopping", "not_ready", "stopping", true, doctor.StatusWarning, doctor.ReasonLifecycleActive},
		{"absent", "absent", "", false, doctor.StatusWarning, doctor.ReasonNotRunning},
		{"stale", "stale", "", false, doctor.StatusWarning, doctor.ReasonStale},
		{"unverifiable", "unverifiable", "", false, doctor.StatusWarning, doctor.ReasonUnverifiable},
		{"unknown", "unknown", "", false, doctor.StatusWarning, doctor.ReasonUnverifiable},
		{"unsupported", "unsupported", "", false, doctor.StatusWarning, doctor.ReasonIncompatible},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var logs bytes.Buffer

			logger := zerolog.New(&logs)

			results, err := doctor.Run(logger.WithContext(t.Context()), doctor.Options{
				DaemonState: test.state, DaemonPhase: test.phase, InhibitorsHeld: test.held,
				LogindDelayMillis: 5000,
			})
			if err != nil {
				t.Fatal(err)
			}

			check := findCheck(results, doctor.CheckDaemonRuntime)
			if check.Status != test.wantStatus || check.Reason != test.wantReason {
				t.Fatalf("runtime check = %+v, want %s/%s", check, test.wantStatus, test.wantReason)
			}

			assertRuntimeDoctorLog(t, logs.Bytes(), test.wantStatus == doctor.StatusWarning)
		})
	}
}

func assertRuntimeDoctorLog(t *testing.T, data []byte, runtimeWarns bool) {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("runtime diagnostic duplicated outcome records: %s", data)
	}

	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}

		if record["component"] != "doctor" || record["operation"] != "run" {
			t.Fatalf("unexpected diagnostic owner: %s", line)
		}

		for key := range record {
			switch key {
			case "component", "operation", "status", "reason", "level", "message", "check_count", "error_count", "warning_count", "warning_checks":
			default:
				t.Fatalf("unexpected potentially private field %q: %s", key, line)
			}
		}
	}

	assertDoctorDegradedCompletion(t, lines[1], runtimeWarns)
}

func assertDoctorDegradedCompletion(t *testing.T, line []byte, runtimeWarns bool) {
	t.Helper()

	var record map[string]any
	if err := json.Unmarshal(line, &record); err != nil {
		t.Fatal(err)
	}

	if record["level"] != "warn" || record["reason"] != "checks_degraded" {
		t.Fatalf("capability warnings lost their diagnostic level/reason: %s", line)
	}

	warned, _ := record["warning_checks"].(string)
	if slices.Contains(strings.Split(warned, ","), doctor.CheckDaemonRuntime) != runtimeWarns {
		t.Fatalf("warning_checks disagrees with the daemon-runtime warning: %s", line)
	}
}
