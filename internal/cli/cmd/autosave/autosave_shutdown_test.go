package autosave

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/automation"
)

func TestStartupWarnsOnlyWhenThePreviousBootEndedWithoutAFinalSave(t *testing.T) {
	t.Parallel()

	const boot = int64(1_000_000)

	tests := []struct {
		name      string
		lastSaved int64
		finalSave int64
		status    string
		wantWarn  bool
	}{
		{"no save recorded", 0, 0, "missing", false},
		{"saved since this boot", boot + 60, 0, "missing", false},
		{"final save after the last save", boot - 600, boot - 300, "written", false},
		{"final save older than the last save", boot - 600, boot - 9000, "written", true},
		{"no final save ever", boot - 600, 0, "missing", true},
		{"final save status unreadable", boot - 600, 0, "corrupt", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statusPath := finalSaveStatusFixture(t, tc.status, tc.finalSave)

			var logs bytes.Buffer

			ctx := zerolog.New(&logs).WithContext(t.Context())
			reportUnsavedShutdown(ctx, statusPath, tc.lastSaved, boot)

			if !tc.wantWarn {
				if logs.Len() != 0 {
					t.Fatalf("unexpected record: %s", logs.Bytes())
				}

				return
			}

			assertUnsavedShutdownRecord(t, logs.Bytes(), boot-tc.lastSaved)
		})
	}
}

func finalSaveStatusFixture(t *testing.T, kind string, completedAt int64) string {
	t.Helper()

	statusPath := filepath.Join(t.TempDir(), "SECRET_STATUS_DIR", "final.json")
	if err := os.MkdirAll(filepath.Dir(statusPath), 0o700); err != nil {
		t.Fatal(err)
	}

	switch kind {
	case "written":
		status := automation.FinalSaveStatus{Result: automation.FinalSaveSaved, CompletedAtUnix: completedAt}
		if err := automation.WriteFinalSaveStatus(statusPath, status); err != nil {
			t.Fatal(err)
		}
	case "corrupt":
		if err := os.WriteFile(statusPath, []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return statusPath
}

func assertUnsavedShutdownRecord(t *testing.T, data []byte, wantGap int64) {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("records = %d, want 1: %s", len(lines), data)
	}

	if strings.Contains(string(data), "SECRET_STATUS_DIR") {
		t.Fatalf("record leaks the status path: %s", data)
	}

	var record map[string]any
	if err := json.Unmarshal(lines[0], &record); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"level": "warn", "component": "snapshot", "operation": "final-save", "status": "degraded",
		"reason": autosaveReasonFinalSaveMissing, "last_save_before_boot_s": float64(wantGap),
		"message": "previous shutdown left no final save",
	}
	if len(record) != len(want) {
		t.Fatalf("record fields = %v, want %v", record, want)
	}

	for key, value := range want {
		if record[key] != value {
			t.Fatalf("%s = %v, want %v: %s", key, record[key], value, lines[0])
		}
	}
}
