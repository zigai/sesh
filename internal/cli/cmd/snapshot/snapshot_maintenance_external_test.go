//go:build linux || darwin

package cmdsnapshot_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestSnapshotPruneUsesConfigAndOnlyChangedFlagsOverridePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	commitCLIMaintenanceFixtures(t, path, 7)
	configPath := writeMaintenanceConfig(t, "retention_days = 0\nmax_count = 5\nmax_bytes = 0\n")

	stdout, stderr, err := executeSnapshotCommand(
		t,
		"--config", configPath, "snapshot", "prune", "--repository", path,
		"--max-count", "6", "--json",
	)
	if err != nil || stderr != "" {
		t.Fatalf("prune failed: %v / %s", err, stderr)
	}

	var envelope struct {
		OK     bool                    `json:"ok"`
		Result testSnapshotPruneResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}

	if !envelope.OK || len(envelope.Result.Removed) != 1 || len(envelope.Result.Retained) != 6 {
		t.Fatalf("flag did not override only configured count: %s", stdout)
	}

	repository, err := store.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Error(err)
		}
	})

	catalog, err := repository.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Generations) != 6 {
		t.Fatalf("catalog membership = %d", len(catalog.Generations))
	}
}

func TestSnapshotPruneThenGCReportsSeparatePhysicalRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	commitCLIMaintenanceFixtures(t, path, 7)
	configPath := writeMaintenanceConfig(t, "retention_days = 0\nmax_count = 5\nmax_bytes = 0\n")

	if stdout, stderr, err := executeSnapshotCommand(
		t,
		"--config", configPath, "snapshot", "prune", "--repository", path, "--json",
	); err != nil || stderr != "" || !strings.Contains(stdout, `"removed"`) {
		t.Fatalf("prune failed: %v / %s / %s", err, stdout, stderr)
	}

	stdout, stderr, err := executeSnapshotCommand(
		t,
		"--config", configPath, "snapshot", "gc", "--repository", path,
		"--max-generations", "8", "--max-bytes", "1073741824", "--json",
	)
	if err != nil || stderr != "" {
		t.Fatalf("gc failed: %v / %s", err, stderr)
	}

	var envelope struct {
		OK     bool                 `json:"ok"`
		Result testSnapshotGCResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}

	if !envelope.OK || len(envelope.Result.Removed) != 2 || envelope.Result.Bytes < 1 || len(envelope.Result.Intents) != 2 {
		t.Fatalf("physical collection receipt = %s", stdout)
	}
}

func TestSnapshotMaintenanceInvalidFlagsDoNotMutateRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	commitCLIMaintenanceFixtures(t, path, 1)
	configPath := writeMaintenanceConfig(t, "retention_days = 0\nmax_count = 0\nmax_bytes = 0\n")
	logPath := filepath.Join(filepath.Dir(configPath), "maintenance.jsonl")
	catalogPath := filepath.Join(path, "catalog.json")

	before, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"snapshot", "prune", "--repository", path, "--max-count", "4"},
		{"snapshot", "gc", "--repository", path, "--max-generations", "0", "--max-bytes", "1"},
		{"snapshot", "gc", "--repository", path, "--max-generations", "1"},
	} {
		fullArgs := append([]string{"--config", configPath}, args...)

		_, _, err := executeSnapshotCommand(t, fullArgs...)
		if clibase.ExitCode(err) != clibase.ExitCodeUsage {
			t.Fatalf("invalid flags returned %v", err)
		}

		after, readErr := os.ReadFile(catalogPath)
		if readErr != nil || !bytes.Equal(before, after) {
			t.Fatalf("invalid flags changed repository: %v", readErr)
		}

		if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid flags opened or created log file: %v", err)
		}
	}
}

func TestSnapshotGCOverLimitStopsBeforeConfigLoggingAndRepository(t *testing.T) {
	base := t.TempDir()
	repositoryPath := filepath.Join(base, "missing-repository")
	configPath := filepath.Join(base, "invalid-config.toml")
	logPath := filepath.Join(base, "maintenance.jsonl")
	configBytes := []byte("this is not valid toml = [")

	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SESH_PATHS_LOG_FILE", logPath)

	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(t.Context(), []string{
		"snapshot", "gc", "--repository", repositoryPath,
		"--max-generations", "10001", "--max-bytes", "1",
		"--config", configPath, "--json",
	}, nil, &stdout, &stderr)
	if clibase.ExitCode(err) != clibase.ExitCodeUsage || stdout.Len() != 0 {
		t.Fatalf("over-limit budget = %v / %s / %s", err, stdout.String(), stderr.String())
	}

	afterConfig, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(afterConfig, configBytes) {
		t.Fatalf("parse validation changed config: %v", err)
	}

	for _, path := range []string{repositoryPath, logPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("parse validation created %s: %v", filepath.Base(path), err)
		}
	}
}

func TestSnapshotPruneProtectedOverageEmitsOneEngineOwnedJSONOutcome(t *testing.T) {
	repositoryPath := filepath.Join(t.TempDir(), "private-repository")
	commitCLIMaintenanceFixtures(t, repositoryPath, 1)
	cfg, logPath := runtimeConfig(t, "info")

	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(t.Context(), []string{
		"snapshot", "prune", "--repository", repositoryPath,
		"--retention-days", "0", "--max-count", "0", "--max-bytes", "1",
		"--config", cfg, "--json",
	}, nil, &stdout, &stderr)
	if !errors.Is(err, engine.ErrRetentionBudgetUnmet) || stderr.Len() != 0 {
		t.Fatalf("protected overage = %v / %s / %s", err, stdout.String(), stderr.String())
	}

	envelope := singleRuntimeEnvelope(t, stdout.Bytes())
	result := decodeJSONPayload[testSnapshotPruneResult](t, envelope.Result)

	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "retentionBudgetUnmet" || !result.BudgetUnmet {
		t.Fatalf("protected overage receipt = %s", stdout.String())
	}

	logs, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}

	for _, private := range []string{repositoryPath, cfg, "private-session", "/private/path"} {
		if bytes.Contains(logs, []byte(private)) {
			t.Fatalf("runtime log leaked private argument %q: %s", private, logs)
		}
	}

	records := bytes.Split(bytes.TrimSpace(logs), []byte("\n"))
	if len(records) != 3 {
		t.Fatalf("expected started, engine started, and one engine-owned outcome: %s", logs)
	}

	assertRuntimeRecord(t, records[0], "cli", "prune", "info", "started")
	assertRuntimeRecord(t, records[1], "snapshot", "prune", "info", "started")
	assertRuntimeRecord(t, records[2], "snapshot", "prune", "warn", "degraded")
}
