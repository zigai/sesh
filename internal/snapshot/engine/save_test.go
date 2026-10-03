//go:build linux || darwin

package engine

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestSaveBoundaryLogging(t *testing.T) {
	for _, scenario := range []string{"partial", "capture-failed", "canceled", "invalid-graph"} {
		t.Run(scenario, func(t *testing.T) {
			r := testRepository(t)

			var output bytes.Buffer

			logger := zerolog.New(&output)

			ctx := logger.WithContext(t.Context())
			if scenario == "canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()

				ctx = canceled
			}

			leaseRoot, socketPath := testLeaseEndpoint(t)

			result, saveErr := save(ctx, r, leaseRoot, socketPath, func(context.Context) (capture.Observation, error) {
				if scenario == "capture-failed" {
					return capture.Observation{}, fmt.Errorf("private-error /secret/path SECRET_COMMAND=token: %w", os.ErrPermission)
				}

				observation := observationFixture()
				if scenario == "invalid-graph" {
					observation.Manifest.Version++
				}

				return observation, nil
			})
			if (saveErr == nil) != (scenario == "partial") {
				t.Fatalf("unexpected result: %#v, %v", result, saveErr)
			}

			assertSaveFailureOwnership(t, saveErr)

			assertSaveRecords(t, output.Bytes(), scenario)
		})
	}
}

func assertSaveFailureOwnership(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		return
	}

	owner, ok := errors.AsType[*saveOutcomeError](err)
	if !ok || !owner.OutcomeLogged() {
		t.Fatal("save failure lost its logging owner")
	}
}

func assertSaveRecords(t *testing.T, data []byte, scenario string) {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("expected exactly start and owning outcome: %s", data)
	}

	if bytes.ContainsAny(data, "\xff") || strings.Contains(string(data), "secret") || strings.Contains(string(data), "SECRET") {
		t.Fatal("private material in log")
	}

	for index, line := range lines {
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}

		validateLogSchema(t, fields)

		if index == 0 {
			if string(fields["level"]) != `"info"` || string(fields["status"]) != `"started"` {
				t.Fatalf("incorrect start event: %s", line)
			}

			continue
		}

		assertSaveOutcomeRecord(t, fields, scenario)
	}
}

func assertSaveOutcomeRecord(t *testing.T, fields map[string]jsontext.Value, scenario string) {
	t.Helper()

	// The fixture pane has no terminal text artifact; failures report no count.
	wantLevel, wantStatus, wantOmitted := `"error"`, `"failed"`, ""
	if scenario == "partial" {
		wantLevel, wantStatus, wantOmitted = `"warn"`, `"partial"`, "1"
	}

	if string(fields["level"]) != wantLevel || string(fields["status"]) != wantStatus {
		t.Fatalf("incorrect outcome event: level=%s status=%s", fields["level"], fields["status"])
	}

	if len(fields["reason"]) == 0 {
		t.Fatal("missing fixed reason")
	}

	if omitted := string(fields["terminal_text_omitted"]); omitted != wantOmitted {
		t.Fatalf("terminal_text_omitted = %q, want %q", omitted, wantOmitted)
	}
}

func observationFixture() capture.Observation {
	m := model.Manifest{
		Format: "sesh", Version: model.Schema, Started: 1, Finished: 2,
		Source:   model.Source{TmuxVersion: "3.6", LibraryVersion: "v0.3.0", Consistency: "observed"},
		Sessions: []model.Session{{ID: "s0", Name: model.Bytes("secret-session"), Directory: model.Bytes("/secret/path")}},
		Windows:  []model.Window{{ID: "w0", Name: model.Bytes("secret-window"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"}}},
		Panes:    []model.Pane{{ID: "p0", Window: "w0", Width: 80, Height: 24, Active: true, Directory: model.Bytes("/secret/path"), Launch: model.Launch{Recovery: "unverified"}}},
		Links:    []model.Link{{Session: "s0", Window: "w0", Active: true}},
	}
	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		m.Evidence = append(m.Evidence, model.Evidence{Domain: domain, Status: "unverified", Reason: "not_captured"})
	}

	return capture.Observation{Manifest: m}
}

func TestUncertainPublicationReasonAndPrivacy(t *testing.T) {
	var output bytes.Buffer

	logger := zerolog.New(&output)

	cause := oops.In("secret-domain").Code("SECRET_CODE").
		With("operation", "SECRET_COMMAND", "phase", "secret-phase", "path", "/secret/path").
		Wrap(errors.Join(store.ErrUncertain, os.ErrPermission, context.Canceled))

	err := saveFailure(logger.WithContext(t.Context()), "commit", cause)
	if !errors.Is(err, store.ErrUncertain) || !errors.Is(err, os.ErrPermission) || !errors.Is(err, context.Canceled) {
		t.Fatal("lost joined failure identities")
	}

	assertSaveDiagnostic(t, err)
	assertPrivateFailureRecord(t, output.Bytes())
}

func TestOutputLimitFailureHasFixedReason(t *testing.T) {
	var output bytes.Buffer

	logger := zerolog.New(&output)

	err := saveFailure(logger.WithContext(t.Context()), "capture", fmt.Errorf("capture /secret/path: %w", gotmux.ErrOutputLimit))
	if !errors.Is(err, gotmux.ErrOutputLimit) {
		t.Fatal("lost output limit identity")
	}

	var fields map[string]jsontext.Value
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}

	validateLogSchema(t, fields)

	if string(fields["reason"]) != `"output_limit_exceeded"` || string(fields["phase"]) != `"capture"` || string(fields["level"]) != `"error"` {
		t.Fatalf("incorrect output limit record: %s", output.Bytes())
	}

	if bytes.Contains(output.Bytes(), []byte("secret")) {
		t.Fatal("error metadata leaked")
	}
}

func assertSaveDiagnostic(t *testing.T, err error) {
	t.Helper()

	diagnostic, ok := oops.AsOops(err)
	if !ok {
		t.Fatal("missing structured save error")
	}

	layers := diagnostic.Layers()
	if len(layers) != 2 || layers[0].Domain != "snapshot" || layers[0].Code != "publication_uncertain" {
		t.Fatal("owning save classification was lost")
	}

	if len(layers[0].Context) != 2 || layers[0].Context["phase"] != "commit" || layers[0].Context["operation"] != "save" {
		t.Fatal("wrong owning operation details")
	}

	if diagnostic.Stacktrace() == "" {
		t.Fatal("missing diagnostic stack")
	}
}

func assertPrivateFailureRecord(t *testing.T, data []byte) {
	t.Helper()

	if lines := bytes.Count(data, []byte{'\n'}); lines != 1 {
		t.Fatal("failure produced duplicate records")
	}

	var fields map[string]string
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}

	if fields["reason"] != "publication_uncertain" || fields["level"] != "error" || fields["phase"] != "commit" {
		t.Fatal("incorrect uncertainty record")
	}

	if bytes.Contains(data, []byte("secret")) || bytes.Contains(data, []byte("SECRET")) {
		t.Fatal("error metadata leaked")
	}

	var schema map[string]jsontext.Value
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}

	validateLogSchema(t, schema)
}

func testRepository(t *testing.T) *store.Repository {
	t.Helper()

	r, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "repository"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})

	return r
}

func validateLogSchema(t *testing.T, fields map[string]jsontext.Value) {
	t.Helper()

	for key := range fields {
		switch key {
		case "level", "component", "operation", "status", "message", "reason", "phase", "sessions", "panes", "terminal_text_omitted":
		default:
			t.Fatalf("unexpected log field %s", key)
		}
	}

	if string(fields["component"]) != `"snapshot"` || string(fields["operation"]) != `"save"` {
		t.Fatal("wrong log owner")
	}
}

func TestInterruptedCaptureRecordsFailedAttemptAndKeepsPriorSource(t *testing.T) {
	r := testRepository(t)
	leaseRoot, socketPath := testLeaseEndpoint(t)

	prior, err := save(t.Context(), r, leaseRoot, socketPath, func(context.Context) (capture.Observation, error) { return observationFixture(), nil })
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer

	logger := zerolog.New(&output)

	ctx, cancel := context.WithCancel(logger.WithContext(t.Context()))
	defer cancel()

	result, err := save(ctx, r, leaseRoot, socketPath, func(context.Context) (capture.Observation, error) {
		cancel()
		return capture.Observation{}, context.Canceled
	})
	if !errors.Is(err, context.Canceled) || result.Attempt == "" {
		t.Fatal("lost interrupted capture identity", err)
	}

	assertSaveRecords(t, output.Bytes(), "capture-failed")

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Attempts) != 2 || catalog.Attempts[1].ID != result.Attempt || catalog.Attempts[1].State != "failed" {
		t.Fatalf("interrupted capture was not recorded as failed: %+v", catalog.Attempts)
	}

	if catalog.Current != prior.Generation || len(catalog.Generations) != 1 {
		t.Fatal("interruption changed committed source")
	}

	assertPriorGenerationRecovers(t, r, prior.Generation)
}

// assertPriorGenerationRecovers checks that after an interrupted save the prior generation stays
// readable and is what practical recovery uses, while the strict current view still refuses it.
func assertPriorGenerationRecovers(t *testing.T, r *store.Repository, prior string) {
	t.Helper()

	if _, err := r.Read(t.Context(), prior); err != nil {
		t.Fatal("lost prior recovery source", err)
	}

	if _, err := r.Current(t.Context()); !errors.Is(err, store.ErrRecoveryRequired) {
		t.Fatal("strict current view recovered past interrupted save", err)
	}

	practical, _, err := r.ReadPracticalCurrent(t.Context())
	if err != nil || practical.ID != prior {
		t.Fatalf("practical recovery after interrupted save = %v, %v; want prior generation", practical.ID, err)
	}
}

func TestCaptureDeadlineRecordsFailedAttempt(t *testing.T) {
	r := testRepository(t)
	leaseRoot, socketPath := testLeaseEndpoint(t)

	ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
	defer cancel()

	result, err := save(ctx, r, leaseRoot, socketPath, func(context.Context) (capture.Observation, error) {
		cancel()
		return capture.Observation{}, context.DeadlineExceeded
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("save error = %v, want the capture deadline", err)
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Attempts) != 1 || catalog.Attempts[0].ID != result.Attempt || catalog.Attempts[0].State != "failed" {
		t.Fatalf("expired capture was not recorded as failed: %+v", catalog.Attempts)
	}
}

func testLeaseEndpoint(t *testing.T) (string, string) {
	t.Helper()
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	socketPath := filepath.Join(t.TempDir(), "test.sock")

	return leaseRoot, socketPath
}

func TestSaveObservationAdmissionValidation(t *testing.T) {
	r := testRepository(t)

	_, err := SaveObservation(t.Context(), r, nil, nil, SaveConfig{LeaseRoot: ""})
	if !errors.Is(err, ErrLeaseRootRequired) {
		t.Fatalf("expected ErrLeaseRootRequired, got %v", err)
	}

	_, err = SaveObservation(t.Context(), r, nil, nil, SaveConfig{LeaseRoot: filepath.Join(t.TempDir(), "leases")})
	if err == nil {
		t.Fatal("expected error for nil server")
	}

	server, err := integration.Open(filepath.Join(t.TempDir(), "absent.sock"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = SaveObservation(t.Context(), nil, server, nil, SaveConfig{LeaseRoot: filepath.Join(t.TempDir(), "leases")})
	if !errors.Is(err, ErrRepositoryRequired) {
		t.Fatalf("expected ErrRepositoryRequired, got %v", err)
	}
}

func save(ctx context.Context, repository *store.Repository, leaseRoot string, socketPath string, observe func(context.Context) (capture.Observation, error)) (SaveResult, error) {
	return saveWithProfile(ctx, repository, leaseRoot, socketPath, RecoveryExact, observe)
}
