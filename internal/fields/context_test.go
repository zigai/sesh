package fields_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/fields"
)

func agentContext() fields.Context {
	return fields.Context{
		SchemaVersion: 0,
		Field:         "build",
		Row:           fields.RowAgent,
		Session: fields.Session{
			ID: "$4", Name: "api", Kind: "live", Path: "/work/api", Origin: "local", ConnectionID: "",
			WindowCount: 2, Attached: true, Activity: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			ActivePaneID: "%9", ActivePanePath: "/work/api", ActivePaneCommand: "nvim", ActivePaneTitle: "editor",
		},
		Agent: &fields.Agent{
			PaneID: "%12", Harness: "claude", Status: "running",
			StatusSince: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC),
			CWD:         "/work/api/server", ProjectRoot: "/work/api", GitBranch: "main", Worktree: "",
			SessionPath: "/home/u/.claude/s.jsonl", PID: 4242, Model: "opus", Task: "refactor",
		},
	}
}

func TestContextEncodeWritesVersionedJSONLine(t *testing.T) {
	data, err := agentContext().Encode()
	if err != nil {
		t.Fatal(err)
	}

	text := string(data)
	if !strings.HasPrefix(text, `{"schemaVersion":1,"field":"build","row":"agent","session":{"id":"$4"`) || !strings.HasSuffix(text, "}\n") {
		t.Fatalf("Encode() = %s", text)
	}

	if strings.Count(text, "\n") != 1 || !strings.Contains(text, `"agent":{"paneId":"%12"`) {
		t.Fatalf("Encode() = %s", text)
	}
}

func TestContextChangeKeyIgnoresTimestampsAndFieldName(t *testing.T) {
	base := agentContext()

	baseKey, err := base.ChangeKey()
	if err != nil {
		t.Fatal(err)
	}

	moved := agentContext()
	moved.Field = "other"
	moved.Session.Activity = moved.Session.Activity.Add(time.Hour)
	moved.Agent.StatusSince = moved.Agent.StatusSince.Add(time.Minute)

	if key, _ := moved.ChangeKey(); key != baseKey {
		t.Fatal("ChangeKey() changed for timestamps or field name")
	}

	if base.Agent.StatusSince != agentContext().Agent.StatusSince {
		t.Fatal("ChangeKey() modified the caller's agent")
	}

	for name, change := range map[string]func(*fields.Context){
		"status":  func(c *fields.Context) { c.Agent.Status = "waiting" },
		"cwd":     func(c *fields.Context) { c.Agent.CWD = "/work/api/client" },
		"session": func(c *fields.Context) { c.Session.Name = "api-2" },
		"windows": func(c *fields.Context) { c.Session.WindowCount = 3 },
	} {
		changed := agentContext()
		change(&changed)

		if key, _ := changed.ChangeKey(); key == baseKey {
			t.Fatalf("ChangeKey() did not change for %s", name)
		}
	}
}
