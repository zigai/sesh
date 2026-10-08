//go:build linux

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func TestRestoreRefusedByAnUnfinishedRestoreLogsWhy(t *testing.T) {
	base := t.TempDir()
	cfg, logPath := filepath.Join(base, "config.toml"), filepath.Join(base, "log.jsonl")

	data := fmt.Sprintf("[settings]\nlog_level = \"info\"\n[paths]\nlog_file = %q\n", logPath)
	if err := os.WriteFile(cfg, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	repoPath := filepath.Join(base, "private-repository-canary")
	snapfixture.CommitOneSession(t, repoPath)

	self, err := process.ReadIdentity(t.Context(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}

	snapfixture.RegisterRestore(t, repoPath, os.Getpid(), self.String())

	var stdout, stderr bytes.Buffer

	args := []string{
		"autosave", "restore-on-start", testConfigFlag, cfg,
		testSocketFlag, filepath.Join(base, "absent.sock"), testRepositoryFlag, repoPath,
		"--history", filepath.Join(base, "state", "history.json"),
	}
	if err := ExecuteApplication(t.Context(), args, nil, &stdout, &stderr); err == nil {
		t.Fatal("an unfinished restore must refuse the startup restore")
	}

	logs, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(logs, []byte("private-repository-canary")) {
		t.Fatalf("log leaked the repository path: %s", logs)
	}

	lines := bytes.Split(bytes.TrimSpace(logs), []byte("\n"))
	last := lines[len(lines)-1]
	assertRuntimeRecord(t, last, "cli", "restore-on-start", "error", "failed")

	var record map[string]any
	if err := json.Unmarshal(last, &record); err != nil {
		t.Fatal(err)
	}

	if record["reason"] != "activeRestores" || record["failed_step"] != "restore_on_start" {
		t.Fatalf("refusal record = %s, want reason activeRestores at step restore_on_start", last)
	}
}
