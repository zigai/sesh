package cmdsnapshot_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/cli"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	testRepositoryFlag = "--repository"
	testConfigFlag     = "--config"
	testJSONFlag       = "--json"
)

type testJSONEnvelope struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Command       string                    `json:"command"`
	OK            bool                      `json:"ok"`
	Result        json.RawMessage           `json:"result"`
	Error         *clibase.JSONErrorPayload `json:"error"`
}

type testSnapshotPruneResult struct {
	Removed     []string `json:"removed"`
	Retained    []string `json:"retained"`
	BudgetUnmet bool     `json:"budgetUnmet"`
}

type testSnapshotGCResult struct {
	Removed []string `json:"removed"`
	Intents []string `json:"intents"`
	Bytes   int64    `json:"bytes"`
}

type testSnapshotRewriteOutput struct {
	OperationID string `json:"operationId"`
}

func executeSnapshotCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(t.Context(), args, nil, &stdout, &stderr)

	return stdout.String(), stderr.String(), err
}

func runtimeConfig(t *testing.T, level string) (string, string) {
	t.Helper()
	base := t.TempDir()
	path, logPath := filepath.Join(base, "config.toml"), filepath.Join(base, "private-log.jsonl")

	data := fmt.Sprintf("[settings]\nlog_level = %q\n[paths]\nlog_file = %q\n", level, logPath)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return path, logPath
}

func writeMaintenanceConfig(t *testing.T, snapshotBody string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	logPath := filepath.Join(dir, "maintenance.jsonl")

	data := fmt.Sprintf("[paths]\nlog_file = %q\n[snapshot]\n%s", logPath, snapshotBody)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func commitCLIMaintenanceFixtures(t *testing.T, path string, count int) {
	t.Helper()

	repository, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Error(err)
		}
	})

	for index := range count {
		attempt, err := repository.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		manifest := model.Manifest{
			Format: "sesh", Version: model.Schema, Started: int64(index*2 + 1), Finished: int64(index*2 + 2),
			Source:   model.Source{TmuxVersion: "3.6", LibraryVersion: "v0.3.0", Consistency: "observed"},
			Sessions: []model.Session{{ID: "s0", Name: model.Bytes("private-session"), Directory: model.Bytes("/private/path")}},
			Windows:  []model.Window{{ID: "w0", Name: model.Bytes("private-window"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"}}},
			Panes:    []model.Pane{{ID: "p0", Window: "w0", Width: 80, Height: 24, Active: true, Directory: model.Bytes("/private/path"), Launch: model.Launch{Recovery: "unverified"}}},
			Links:    []model.Link{{Session: "s0", Window: "w0", Active: true}},
		}
		for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
			manifest.Evidence = append(manifest.Evidence, model.Evidence{Domain: domain, Status: "unverified", Reason: "not_captured"})
		}

		if _, err := repository.Commit(t.Context(), attempt.ID, manifest, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func singleRuntimeEnvelope(t *testing.T, data []byte) testJSONEnvelope {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(data))

	var env testJSONEnvelope
	if err := decoder.Decode(&env); err != nil {
		t.Fatalf("invalid JSON %q: %v", data, err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("extra output %q: %v", data, err)
	}

	return env
}

func decodeJSONPayload[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()

	var val T
	if err := json.Unmarshal(raw, &val); err != nil {
		t.Fatalf("failed to unmarshal JSON payload: %v\nraw: %s", err, string(raw))
	}

	return val
}

func assertRuntimeLog(t *testing.T, path, component, operation, level, status string, private ...string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range private {
		if bytes.Contains(data, []byte(value)) {
			t.Fatalf("runtime log exposed private argument: %s", value)
		}
	}

	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected start and one outcome: %s", data)
	}

	for i, line := range lines {
		wantLevel, wantStatus := "info", "started"
		if i == 1 {
			wantLevel, wantStatus = level, status
		}

		assertRuntimeRecord(t, line, component, operation, wantLevel, wantStatus)
	}
}

func assertRuntimeRecord(t *testing.T, line []byte, component, operation, level, status string) {
	t.Helper()

	var record map[string]any
	if err := json.Unmarshal(line, &record); err != nil {
		t.Fatal(err)
	}

	if record["component"] != component || record["operation"] != operation {
		t.Fatalf("wrong boundary: %s", line)
	}

	if record["level"] != level || record["status"] != status {
		t.Fatalf("wrong event: %s", line)
	}

	if status != "started" && status != "ok" && record["reason"] == nil && record["phase"] == nil && record["code"] == nil {
		t.Fatalf("missing fixed reason: %s", line)
	}

	assertRuntimeLogFields(t, record)
}

func assertRuntimeLogFields(t *testing.T, record map[string]any) {
	t.Helper()

	for key := range record {
		switch key {
		case "time", "level", "component", "operation", "status", "message", "reason", "phase", "code", "sessions", "windows", "panes", "error_code":
		case "verified":
			if _, ok := record[key].(bool); !ok || record["operation"] != "restore" || record["component"] != "snapshot" {
				t.Fatalf("invalid verification field: %#v", record)
			}
		case "terminal_text_omitted":
			if _, ok := record[key].(float64); !ok || record["operation"] != "save" || record["component"] != "snapshot" {
				t.Fatalf("invalid terminal text count: %#v", record)
			}
		default:
			t.Fatalf("unexpected log field: %s", key)
		}
	}
}
