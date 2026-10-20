//go:build integration

package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli"

	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/engine"
)

func TestApplicationRestoreReportsBestEffortWithSettledReceipt(t *testing.T) {
	const name = "restore-cli-source"

	repoPath, leasePath, generation := captureRemovedSession(t, name)
	target := tmuxtest.NewServer(t)
	cfg, logPath := runtimeConfig(t, "info")

	var stdout, stderr bytes.Buffer

	operationID := "0123456789abcdef0123456789abcdef"

	err := cli.ExecuteApplication(t.Context(), []string{"snapshot", "restore", testRepositoryFlag, repoPath, testLeaseRootFlag, leasePath, testSocketFlag, target.Endpoint().SocketPath, testGenerationFlag, generation, "--operation", operationID, "--no-processes", testConfigFlag, cfg, testJSONFlag}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("expected practical restore: %v / %s", err, stderr.String())
	}

	envelope := singleRuntimeEnvelope(t, stdout.Bytes())
	if !envelope.OK || envelope.Error != nil || stderr.Len() != 0 {
		t.Fatalf("unexpected restore output: %s / %s", stdout.String(), stderr.String())
	}

	result := decodeJSONPayload[snapshotRestoreResultJSON](t, envelope.Result)
	if result.OperationID != operationID || !result.Verified {
		t.Fatalf("missing restore receipt: %+v", result)
	}

	if _, err := target.FindSession(t.Context(), name); err != nil {
		t.Fatalf("restored session absent: %v", err)
	}

	assertRuntimeLog(t, logPath, "snapshot", "restore", "warn", "best_effort")
	assertCompletedRestoreOperation(t, repoPath, operationID)

	if _, err := target.FindSession(t.Context(), name); err != nil {
		t.Fatalf("abandonment changed restored session: %v", err)
	}
}

func assertCompletedRestoreOperation(t *testing.T, path, id string) {
	t.Helper()

	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	checkRestoreOperationCommand(t, path, id, "show", "complete")

	after, readErr := os.ReadFile(filepath.Join(path, "catalog.json"))
	if readErr != nil || !bytes.Equal(before, after) {
		t.Fatal("reconciliation changed journal or recovery source")
	}

	assertCompletedRestoreCannotBeAbandoned(t, path, id, before)
}

func assertCompletedRestoreCannotBeAbandoned(t *testing.T, path, id string, before []byte) {
	t.Helper()

	cfg, _ := runtimeConfig(t, "info")

	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(t.Context(), []string{
		snapshotCmd, "recover", "restore", "abandon", id, testRepositoryFlag, path, testConfigFlag, cfg, testJSONFlag,
	}, nil, &stdout, &stderr)
	if err == nil {
		t.Fatalf("completed restore was abandoned: %s", stdout.String())
	}

	after, readErr := os.ReadFile(filepath.Join(path, "catalog.json"))
	if readErr != nil || !bytes.Equal(before, after) {
		t.Fatal("failed abandonment changed completed restore journal")
	}
}

func checkRestoreOperationCommand(t *testing.T, path, id, command, want string) string {
	t.Helper()
	cfg, logPath := runtimeConfig(t, "info")

	var stdout, stderr bytes.Buffer

	args := []string{snapshotCmd, "recover", "restore", command, id, testRepositoryFlag, path, testConfigFlag, cfg, testJSONFlag}
	if err := cli.ExecuteApplication(t.Context(), args, nil, &stdout, &stderr); err != nil {
		t.Fatalf("%s: %v / %s", command, err, stderr.String())
	}

	envelope := singleRuntimeEnvelope(t, stdout.Bytes())

	status := decodeJSONPayload[engine.OperationStatus](t, envelope.Result)
	if status.OperationID != id || status.State != want || !envelope.OK || stderr.Len() != 0 {
		t.Fatalf("%s returned wrong state: %s / %s", command, stdout.String(), stderr.String())
	}

	return logPath
}

func captureRemovedSession(t *testing.T, name string) (string, string, string) {
	t.Helper()
	source := newCaptureCLIServer(t)

	sessions, err := source.Sessions(t.Context())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("source fixture: %v", err)
	}

	original := sessions[0].Handle()
	if err := original.Rename(t.Context(), name); err != nil {
		t.Fatal(err)
	}

	repoPath := filepath.Join(t.TempDir(), "repo")
	leasePath := filepath.Join(t.TempDir(), "leases")

	saved := executeJSON(t, "snapshot", "save", testRepositoryFlag, repoPath, testLeaseRootFlag, leasePath, testSocketFlag, source.Endpoint().SocketPath, string(sessions[0].ID), testNoConfigFlag, testJSONFlag)
	if saved.err != nil || !saved.envelope.OK {
		t.Fatalf("save: %v", saved.err)
	}

	generation := decodeJSONPayload[snapshotSaveResultJSON](t, saved.envelope.Result).Generation
	if err := original.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	return repoPath, leasePath, generation
}

func TestApplicationRestoreTablePrintsSkippedSessionsOnStdoutWhenItExitsNonZero(t *testing.T) {
	home := isolateJSONContractHome(t)
	server := newCaptureCLIServer(t)

	kept := newAutosaveSession(t, server)
	socket := server.Endpoint().SocketPath
	repository := filepath.Join(home, "repository")
	lease := filepath.Join(home, "leases")

	saved := runJSONContract(t, "snapshot.save", "snapshot", "save", testSocketFlag, socket, testRepositoryFlag, repository, testLeaseRootFlag, lease, string(kept.ID()))
	generation := contractJSONValue[string](t, saved, "generation")

	stdout, stderr, err := executeCommand(t, "snapshot", "restore", testSocketFlag, socket, testRepositoryFlag, repository, testLeaseRootFlag, lease, testGenerationFlag, generation, "--no-processes", testNoConfigFlag)
	if err == nil {
		t.Fatalf("a restore that created nothing must exit non-zero, stdout=%q", stdout)
	}

	for _, want := range []string{"Skipped:", "autosave-alpha (", "a live session already uses this name"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the result table must reach stdout even when the command fails; missing %q in stdout=%q stderr=%q", want, stdout, stderr)
		}
	}
}
