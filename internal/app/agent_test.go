//go:build linux || darwin

package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/agents"
)

func TestAgentViewHistoryFailureLogsDegradationWithoutIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "PRIVATE_HISTORY")
	if err := os.WriteFile(path, []byte("PRIVATE_INVALID_HISTORY"), 0o600); err != nil {
		t.Fatal(err)
	}

	local := &Local{historyPath: path}

	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())
	local.recordAgentView(ctx, agents.PaneLocation{SessionName: "PRIVATE_SESSION", PaneID: "PRIVATE_PANE", ServerSocket: "PRIVATE_SOCKET"})

	if bytes.Count(logs.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("incorrect history logging: %s", logs.String())
	}

	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"level": "warn", "component": "agents", "operation": "record_view", "status": "degraded",
		"reason": "history_update_failed", "message": "agent navigation history was not recorded",
		"error_domain": "session", "error_code": "history_failed", "error_step": "update_history",
	}
	if record["error"] == nil || len(record) != len(want)+1 {
		t.Fatalf("unexpected history schema: %s", logs.String())
	}

	for key, value := range want {
		if record[key] != value {
			t.Fatalf("incorrect history field %s: %s", key, logs.String())
		}
	}

	logs.Reset()
	local.recordAgentView(ctx, agents.PaneLocation{})

	if logs.Len() != 0 {
		t.Fatal("absent navigation target should be silent")
	}
}
