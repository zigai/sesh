package autosave

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func reportPendingRestoreRecords(t *testing.T, env autosaveEnv, pending pendingRestore, runs int) []map[string]any {
	t.Helper()

	var buffer bytes.Buffer

	ctx := zerolog.New(&buffer).WithContext(t.Context())
	for range runs {
		reportPendingRestore(ctx, env, pending)
	}

	var records []map[string]any

	for line := range strings.SplitSeq(strings.TrimSpace(buffer.String()), "\n") {
		if line == "" {
			continue
		}

		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decoding log record failed: %v", err)
		}

		records = append(records, record)
	}

	return records
}

func TestPendingRestoreIsReportedOncePerServerStart(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "state", "history.json")
	env := autosaveEnv{historyPath: historyPath}
	pending := pendingRestore{sessions: 8, serverStarted: time.Unix(2_000, 0)}

	records := reportPendingRestoreRecords(t, env, pending, 3)
	if len(records) != 1 {
		t.Fatalf("expected exactly one record across repeated intervals, got %d: %+v", len(records), records)
	}

	assertPendingRestoreRecord(t, records[0], historyPath)

	restarted := pendingRestore{sessions: 8, serverStarted: time.Unix(3_000, 0)}
	if records := reportPendingRestoreRecords(t, env, restarted, 2); len(records) != 1 {
		t.Fatalf("a later server start is a new episode reported once, got %d records", len(records))
	}
}

// assertPendingRestoreRecord checks the fixed schema of the restore-pending warning and that it
// carries no path.
func assertPendingRestoreRecord(t *testing.T, record map[string]any, historyPath string) {
	t.Helper()

	if record["level"] != "warn" || record["component"] != "snapshot" || record["operation"] != "autosave" ||
		record["status"] != "skipped" || record["reason"] != "restore_pending" || record["sessions"] != float64(8) {
		t.Fatalf("unexpected record schema: %+v", record)
	}

	for key := range record {
		if !slices.Contains([]string{"level", "component", "operation", "status", "reason", "sessions", "message"}, key) {
			t.Fatalf("unexpected field %q in %+v", key, record)
		}
	}

	message, ok := record["message"].(string)
	if !ok || strings.Contains(message, historyPath) || strings.Contains(message, filepath.Dir(historyPath)) {
		t.Fatalf("the record must have a fixed message naming no paths: %+v", record)
	}
}

func TestPendingRestoreStaysSilentWithoutHistoryPath(t *testing.T) {
	pending := pendingRestore{sessions: 1, serverStarted: time.Unix(2_000, 0)}

	if records := reportPendingRestoreRecords(t, autosaveEnv{historyPath: ""}, pending, 2); len(records) != 0 {
		t.Fatalf("without a marker location the report must stay silent, got %+v", records)
	}
}
