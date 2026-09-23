//go:build integration

package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/cli"

	"github.com/zigai/sesh/internal/snapshot/store"
)

type snapshotRewriteOutput struct {
	OperationID string `json:"operationId"`
	Source      string `json:"source"`
	Replacement string `json:"replacement"`
	Empty       bool   `json:"empty"`
	State       string `json:"state"`
}

func TestSnapshotRewriteCommandsPreserveCompletedDeletionReceipt(t *testing.T) {
	path, _, generation := captureRemovedSession(t, "rewrite-cli-source")
	id := completeSnapshotRewrite(t, path, generation)

	before, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	for _, command := range []string{"reconcile", "resume"} {
		cfg, logPath := runtimeConfig(t, "info")

		var stdout, stderr bytes.Buffer

		args := []string{snapshotCmd, "rewrite", command, testRepositoryFlag, path, "--operation", id, testConfigFlag, cfg, testJSONFlag}
		if err := cli.ExecuteApplication(t.Context(), args, nil, &stdout, &stderr); err != nil {
			t.Fatalf("%s: %v / %s", command, err, stderr.String())
		}

		envelope := singleRuntimeEnvelope(t, stdout.Bytes())

		result := decodeJSONPayload[snapshotRewriteOutput](t, envelope.Result)
		if !envelope.OK || stderr.Len() != 0 || result.OperationID != id || result.Source != generation || !result.Empty || result.State != "retired" {
			t.Fatalf("wrong rewrite receipt: %s / %s", stdout.String(), stderr.String())
		}

		assertRuntimeLog(t, logPath, "cli", command+"_rewrite", "info", "ok", path, cfg, id, generation)
	}

	after, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection or replay of completed rewrite changed catalog")
	}
}

func completeSnapshotRewrite(t *testing.T, path, generation string) string {
	t.Helper()

	repo, err := store.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	manifest, err := repo.Read(t.Context(), generation)
	if err != nil {
		t.Fatal(err)
	}

	id := store.NewOperationID()
	if _, err := repo.RemoveSessions(t.Context(), id, generation, []string{manifest.Sessions[0].ID}); err != nil {
		t.Fatal(err)
	}

	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	return id
}
