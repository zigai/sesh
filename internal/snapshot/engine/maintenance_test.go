//go:build linux || darwin

package engine

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var errPrivateMaintenance = errors.New("secret command /private/repository")

func TestPruneReportsProtectedByteOverageWithoutRemovingProtectedGeneration(t *testing.T) {
	repository := testRepository(t)
	commitMaintenanceFixtures(t, repository, 1)

	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())

	result, err := Prune(ctx, repository, time.Now().UTC(), store.RetentionPolicy{MaxBytes: 1})
	if !errors.Is(err, ErrRetentionBudgetUnmet) || !result.BudgetUnmet {
		t.Fatalf("protected overage = %+v, %v", result, err)
	}

	if len(result.Removed) != 0 || len(result.Retained) != 1 || len(result.Protected) != 1 {
		t.Fatalf("protected membership changed: %+v", result)
	}

	if !commandOutcomeWasLogged(err) {
		t.Fatal("degraded outcome lost logging ownership")
	}

	records := maintenanceLogRecords(t, logs.Bytes())
	if len(records) != 2 || records[1]["status"] != "degraded" || records[1]["reason"] != "protected_overage" {
		t.Fatalf("unexpected logs: %s", logs.String())
	}
}

func TestPruneThenGarbageCollectDistinguishesMembershipAndPhysicalRemoval(t *testing.T) {
	repository := testRepository(t)
	commitMaintenanceFixtures(t, repository, 7)

	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())

	retention, err := Prune(ctx, repository, time.Now().UTC(), store.RetentionPolicy{MaxCount: 5})
	if err != nil {
		t.Fatal(err)
	}

	if len(retention.Removed) != 2 || len(retention.Retained) != 5 {
		t.Fatalf("membership result = %+v", retention)
	}

	collection, err := GarbageCollect(ctx, repository, store.CollectionBudget{Generations: 8, Bytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}

	if len(collection.Removed) != 2 || collection.Bytes < 1 || len(collection.Quarantined) != 0 {
		t.Fatalf("physical result = %+v", collection)
	}

	if len(collection.Intents) != 2 {
		t.Fatalf("missing durable collection identities: %+v", collection)
	}

	records := maintenanceLogRecords(t, logs.Bytes())
	if len(records) != 4 || records[1]["operation"] != "prune" || records[3]["operation"] != "gc" {
		t.Fatalf("unexpected logs: %s", logs.String())
	}
}

func TestMaintenancePreservesPinnedRecoverySource(t *testing.T) {
	repository := testRepository(t)
	generations := commitMaintenanceFixtures(t, repository, 7)

	if _, err := repository.Pin(t.Context(), generations[0].ID, model.Bytes("target"), nil); err != nil {
		t.Fatal(err)
	}

	retention, err := Prune(t.Context(), repository, time.Now().UTC(), store.RetentionPolicy{MaxCount: 5})
	if err != nil {
		t.Fatal(err)
	}

	if len(retention.Removed) != 1 || !containsGeneration(retention.Protected, generations[0].ID) {
		t.Fatalf("pinned source not protected: %+v", retention)
	}

	collection, err := GarbageCollect(t.Context(), repository, store.CollectionBudget{Generations: 8, Bytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}

	if len(collection.Removed) != 1 || containsGeneration(collection.Removed, generations[0].ID) {
		t.Fatalf("pinned source physically collected: %+v", collection)
	}
}

func TestMaintenanceUncertaintyLogsOneTerminalFailure(t *testing.T) {
	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())

	err := maintenanceFailure(ctx, "gc", "collection", errors.Join(
		store.ErrUncertain,
		errPrivateMaintenance,
	))
	if !errors.Is(err, store.ErrUncertain) || !commandOutcomeWasLogged(err) {
		t.Fatalf("uncertainty identity or ownership lost: %v", err)
	}

	records := maintenanceLogRecords(t, logs.Bytes())
	if len(records) != 1 {
		t.Fatalf("terminal failure records = %d: %s", len(records), logs.String())
	}

	record := records[0]
	if record["level"] != "error" || record["component"] != "snapshot" || record["operation"] != "gc" ||
		record["status"] != "failed" || record["phase"] != "collection" || record["reason"] != "publication_uncertain" {
		t.Fatalf("uncertainty schema = %#v", record)
	}

	assertMaintenanceFailureFields(t, record)
}

func TestGarbageCollectionQuarantineLogsOnePrivateDegradedOutcome(t *testing.T) {
	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())

	result, err := collectionOutcome(ctx, store.CollectionResult{
		Removed: []string{"0123456789abcdef0123456789abcdef"},
		Quarantined: []store.CollectionSkip{{
			Generation: "fedcba9876543210fedcba9876543210",
			Reason:     store.CollectionReasonQuarantined,
		}},
		Bytes: 42,
	})
	if !errors.Is(err, ErrCollectionQuarantined) || len(result.Quarantined) != 1 || !commandOutcomeWasLogged(err) {
		t.Fatalf("quarantine identity, receipt or ownership lost: %+v / %v", result, err)
	}

	records := maintenanceLogRecords(t, logs.Bytes())
	if len(records) != 1 {
		t.Fatalf("quarantine outcome records = %d: %s", len(records), logs.String())
	}

	record := records[0]
	if record["level"] != "warn" || record["component"] != "snapshot" || record["operation"] != "gc" ||
		record["status"] != "degraded" || record["reason"] != "quarantined" {
		t.Fatalf("quarantine schema = %#v", record)
	}

	assertMaintenanceOutcomeFields(t, record)
}

func assertMaintenanceFailureFields(t *testing.T, record map[string]any) {
	t.Helper()

	for key := range record {
		switch key {
		case "level", "component", "operation", "status", "phase", "reason", "message", "error":
		default:
			t.Fatalf("unexpected uncertainty log field %q", key)
		}
	}
}

func assertMaintenanceOutcomeFields(t *testing.T, record map[string]any) {
	t.Helper()

	for key := range record {
		switch key {
		case "level", "component", "operation", "status", "reason", "message":
		default:
			t.Fatalf("unexpected maintenance outcome field %q", key)
		}
	}
}

func commitMaintenanceFixtures(t *testing.T, repository *store.Repository, count int) []store.Generation {
	t.Helper()

	generations := make([]store.Generation, 0, count)
	for index := range count {
		attempt, err := repository.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		observation := observationFixture()
		observation.Manifest.Started = int64(index*2 + 1)
		observation.Manifest.Finished = int64(index*2 + 2)

		generation, err := repository.Commit(t.Context(), attempt.ID, observation.Manifest, nil)
		if err != nil {
			t.Fatal(err)
		}

		generations = append(generations, generation)
	}

	return generations
}

func containsGeneration(generations []string, target string) bool {
	return slices.Contains(generations, target)
}

func maintenanceLogRecords(t *testing.T, data []byte) []map[string]any {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})

	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}

		records = append(records, record)
	}

	return records
}

func commandOutcomeWasLogged(err error) bool {
	owner, ok := err.(interface{ OutcomeLogged() bool })
	return ok && owner.OutcomeLogged()
}
