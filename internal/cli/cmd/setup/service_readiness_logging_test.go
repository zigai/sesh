package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/setup"
)

var errPrivateServiceInspection = errors.New("PRIVATE_SERVICE_OUTPUT /private/unit")

func TestAutomaticSaveReadinessLogsOnlyDegradedObservations(t *testing.T) {
	for _, scenario := range []string{"healthy", "failed", "unavailable", "not_reloaded"} {
		t.Run(scenario, func(t *testing.T) {
			testServiceReadinessLogging(t, scenario)
		})
	}
}

func testServiceReadinessLogging(t *testing.T, scenario string) {
	t.Helper()

	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())
	manager := setup.ServiceManager{Inspect: func(context.Context, string, ...string) (string, error) {
		if scenario == "unavailable" {
			return "", errPrivateServiceInspection
		}

		if scenario == "failed" {
			return "failed", nil
		}

		return "active", nil
	}}
	definitions := []setup.ServiceDefinition{{Name: setup.AutosaveTimerName}}

	states := automaticSaveStates(ctx, manager, definitions, &setup.ServiceRefreshResult{Reloaded: scenario != "not_reloaded"})
	if scenario == "healthy" || scenario == "not_reloaded" {
		if logs.Len() != 0 {
			t.Fatalf("healthy or unobserved state logged: %s", logs.String())
		}

		return
	}

	assertServiceReadinessWarning(t, logs.Bytes(), scenario)

	if scenario == "unavailable" && states != nil {
		t.Fatal("unavailable readiness must remain unknown")
	}
}

func assertServiceReadinessWarning(t *testing.T, data []byte, scenario string) {
	t.Helper()

	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"level": "warn", "component": "setup", "operation": "inspect_autosave_services", "status": "degraded",
		"reason": "service_state_unavailable", "message": "automatic save service readiness could not be verified",
	}
	if scenario == "failed" {
		want["reason"], want["message"], want["failed"] = "service_failed", "automatic save services are failing", float64(1)
		want["units"] = []any{setup.AutosaveTimerName}
	} else {
		want["error"] = record["error"]
		if !strings.Contains(fmt.Sprint(record["error"]), errPrivateServiceInspection.Error()) {
			t.Fatalf("readiness warning lacks its cause: %#v", record)
		}
	}

	if diff := cmp.Diff(want, record); diff != "" {
		t.Fatalf("incorrect readiness warning: %s", diff)
	}
}
