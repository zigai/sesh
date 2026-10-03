package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/agents"
)

func TestAgentViewHistoryFailureLogsDegradationWithoutIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "PRIVATE_HISTORY")
	if err := os.WriteFile(path, []byte("PRIVATE_INVALID_HISTORY"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := NewActionFocusCommand()
	if err := cmd.Flags().Set("history", path); err != nil {
		t.Fatal(err)
	}

	var logs, stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	ctx := zerolog.New(&logs).WithContext(t.Context())
	recordNavigatedAgentView(ctx, cmd, agents.PaneLocation{SessionName: "PRIVATE_SESSION", PaneID: "PRIVATE_PANE", ServerSocket: "PRIVATE_SOCKET"})

	if bytes.Count(logs.Bytes(), []byte("\n")) != 1 || strings.Contains(logs.String(), "PRIVATE_") || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("incorrect history logging: %s", logs.String())
	}

	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"level": "warn", "component": "agents", "operation": "record_view", "status": "degraded",
		"reason": "history_update_failed", "message": "agent navigation history was not recorded",
	}
	if len(record) != len(want) {
		t.Fatalf("unexpected history schema: %s", logs.String())
	}

	for key, value := range want {
		if record[key] != value {
			t.Fatalf("incorrect history field %s: %s", key, logs.String())
		}
	}

	logs.Reset()
	recordNavigatedAgentView(ctx, cmd, agents.PaneLocation{})

	if logs.Len() != 0 {
		t.Fatal("absent navigation target should be silent")
	}
}
