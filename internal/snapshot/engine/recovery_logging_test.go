//go:build linux || darwin

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestRestorableSessionInspectionKeepsEmptyAndCanceledRepositoriesSilent(t *testing.T) {
	r := testRepository(t)

	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())
	if count := RestorableSessionCount(ctx, r); count != 0 {
		t.Fatalf("empty repository count = %d", count)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	if count := RestorableSessionCount(canceled, r); count != 0 || logs.Len() != 0 {
		t.Fatalf("empty or canceled inspection logged: %s", logs.String())
	}
}

func TestRestorableSessionInspectionWarnsWhenRepositoryCannotBeRead(t *testing.T) {
	r, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "PRIVATE_REPOSITORY"))
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())
	if count := RestorableSessionCount(ctx, r); count != 0 {
		t.Fatalf("unreadable repository count = %d", count)
	}

	assertRecoveryInspectionRecord(t, logs.Bytes(), "count_restorable", "current")
}

func TestUnrestoredSessionInspectionWarnsWhenSelectedServerCannotBeProbed(t *testing.T) {
	r := testRepository(t)

	server, err := tmuxserver.Open(filepath.Join(t.TempDir(), "PRIVATE_SOCKET"))
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())
	if found := FindUnrestored(ctx, server, r); len(found.Sessions) != 0 {
		t.Fatal("unavailable server produced sessions")
	}

	assertRecoveryInspectionRecord(t, logs.Bytes(), "find_unrestored", "probe")
}

func assertRecoveryInspectionRecord(t *testing.T, data []byte, operation, phase string) {
	t.Helper()

	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"level": "warn", "component": "snapshot", "operation": operation, "phase": phase,
		"status": "degraded", "reason": "recovery_inspection_failed",
		"message": "saved session recovery inspection could not be completed",
	}

	if record["error"] == nil {
		t.Fatalf("recovery inspection warning lacks its cause: %#v", record)
	}

	for _, key := range []string{"error", "error_domain", "error_code", "error_step"} {
		if value, ok := record[key]; ok {
			want[key] = value
		}
	}

	if diff := cmp.Diff(want, record); diff != "" {
		t.Fatalf("incorrect recovery inspection warning: %s", diff)
	}
}
