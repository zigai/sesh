package cmdsnapshot_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestSnapshotOperationRequiresIdentityBeforeOpeningRepository(t *testing.T) {
	for _, command := range [][]string{{"reconcile"}, {"abandon"}, {"rewrite", "reconcile"}, {"rewrite", "resume"}} {
		t.Run(strings.Join(command, "_"), func(t *testing.T) {
			cfg, logPath := runtimeConfig(t, "info")
			path := filepath.Join(t.TempDir(), "missing")

			var stdout, stderr bytes.Buffer

			args := append([]string{"snapshot"}, command...)
			args = append(args, testRepositoryFlag, path, testConfigFlag, cfg, testJSONFlag)

			err := cli.ExecuteApplication(t.Context(), args, nil, &stdout, &stderr)
			if clibase.ExitCode(err) != clibase.ExitCodeUsage {
				t.Fatalf("missing identity: %v", err)
			}

			if stdout.Len() != 0 || singleRuntimeEnvelope(t, stderr.Bytes()).OK {
				t.Fatal("usage did not produce one stderr failure")
			}

			for _, untouched := range []string{path, logPath} {
				if _, err := os.Stat(untouched); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("usage mutated state: %v", err)
				}
			}
		})
	}
}

func TestSnapshotOperationMissingIDPreservesRepository(t *testing.T) {
	for _, name := range []string{"reconcile", "abandon"} {
		t.Run(name, func(t *testing.T) {
			testMissingOperation(t, name)
		})
	}
}

func testMissingOperation(t *testing.T, name string) {
	t.Helper()
	cfg, logPath := runtimeConfig(t, "info")
	path := filepath.Join(t.TempDir(), "repository")

	repo, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	err = cli.ExecuteApplication(t.Context(), []string{"snapshot", name, testRepositoryFlag, path, "--operation", store.NewOperationID(), testConfigFlag, cfg, testJSONFlag}, nil, &stdout, &stderr)
	if !errors.Is(err, store.ErrUnavailable) || stdout.Len() != 0 {
		t.Fatalf("missing operation: %v / %s", err, stdout.String())
	}

	if singleRuntimeEnvelope(t, stderr.Bytes()).OK {
		t.Fatal("missing operation reported success")
	}

	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("missing operation changed catalog")
	}

	assertRuntimeLog(t, logPath, "cli", name+"_restore", "error", "failed", path, cfg)
}

func TestSnapshotRewriteMissingReceiptIsVisibleWithoutMutation(t *testing.T) {
	for _, command := range []string{"reconcile", "resume"} {
		t.Run(command, func(t *testing.T) {
			checkMissingRewriteReceipt(t, command)
		})
	}
}

func checkMissingRewriteReceipt(t *testing.T, command string) {
	t.Helper()
	cfg, logPath := runtimeConfig(t, "info")
	path := filepath.Join(t.TempDir(), "repository")

	repo, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	id := store.NewOperationID()

	var stdout, stderr bytes.Buffer

	args := []string{"snapshot", "rewrite", command, testRepositoryFlag, path, "--operation", id, testConfigFlag, cfg, testJSONFlag}

	err = cli.ExecuteApplication(t.Context(), args, nil, &stdout, &stderr)
	if err == nil || stderr.Len() != 0 {
		t.Fatalf("missing receipt outcome: %v / %s", err, stderr.String())
	}

	envelope := singleRuntimeEnvelope(t, stdout.Bytes())

	result := decodeJSONPayload[testSnapshotRewriteOutput](t, envelope.Result)
	if envelope.OK || result.OperationID != id {
		t.Fatalf("missing failure receipt: %s", stdout.String())
	}

	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unknown rewrite changed catalog")
	}

	assertRuntimeLog(t, logPath, "cli", command+"_rewrite", "error", "failed", path, cfg, id)
}
