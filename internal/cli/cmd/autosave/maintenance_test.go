package autosave

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestAutosaveMaintenanceRetiresAndReclaimsGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshots")

	repo, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	})

	for range 7 {
		commitMaintenanceObservation(t, repo)
	}

	if err := maintainAutosaveRepository(t.Context(), repo, config.SnapshotConfig{RetentionDays: 0, MaxCount: 5, MaxBytes: 0}); err != nil {
		t.Fatal(err)
	}

	catalog, err := repo.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Generations) != 5 {
		t.Fatalf("catalog generations = %d, want the 5 newest retained", len(catalog.Generations))
	}

	entries, err := os.ReadDir(filepath.Join(path, "generations"))
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 5 {
		t.Fatalf("generation directories = %d, want retired generations reclaimed from disk", len(entries))
	}
}

func commitMaintenanceObservation(t *testing.T, repo *store.Repository) {
	t.Helper()

	attempt, err := repo.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("terminal text\n")
	hash := sha256.Sum256(content)

	manifest := model.Manifest{
		Format: "sesh", Version: model.Schema, Started: 1, Finished: 2,
		Source:    model.Source{TmuxVersion: "3.6", LibraryVersion: "v0.3.0", Consistency: "observed"},
		Sessions:  []model.Session{{ID: "s0", Name: model.Bytes("work"), Directory: model.Bytes("/tmp")}},
		Windows:   []model.Window{{ID: "w0", Name: model.Bytes("editor"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"}}},
		Links:     []model.Link{{Session: "s0", Window: "w0", Index: 0, Active: true}},
		Panes:     []model.Pane{{ID: "p0", Window: "w0", Directory: model.Bytes("/tmp"), Width: 80, Height: 24, Active: true, Terminal: model.Terminal{Artifact: "a0"}, Launch: model.Launch{Recovery: model.RecoveryUnverified}}},
		Artifacts: []model.Artifact{{ID: "a0", Kind: "terminal-text", Length: int64(len(content)), SHA256: hex.EncodeToString(hash[:])}},
	}

	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		manifest.Evidence = append(manifest.Evidence, model.Evidence{Domain: domain, Status: "observed", Reason: "observed_only"})
	}

	if _, err := repo.Commit(t.Context(), attempt.ID, manifest, map[string]io.Reader{"a0": bytes.NewReader(content)}); err != nil {
		t.Fatal(err)
	}
}
