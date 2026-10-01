// Package snapfixture commits small native snapshot generations for tests that need a repository
// with saved sessions but no tmux server.
package snapfixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	fixtureStarted  = 1
	fixtureFinished = 2
	fixtureWidth    = 80
	fixtureHeight   = 24
)

// CommitOneSession opens or creates the repository at repoPath and commits a generation holding a
// single session named "work".
func CommitOneSession(t *testing.T, repoPath string) {
	t.Helper()

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()

	attempt, err := repo.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("terminal text\n")
	hash := sha256.Sum256(content)

	manifest := fixtureManifest(hex.EncodeToString(hash[:]), int64(len(content)))

	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		manifest.Evidence = append(manifest.Evidence, model.Evidence{Domain: domain, Status: "observed", Reason: "observed_only"})
	}

	if _, err := repo.Commit(t.Context(), attempt.ID, manifest, map[string]io.Reader{"a0": bytes.NewReader(content)}); err != nil {
		t.Fatal(err)
	}
}

func fixtureManifest(digest string, length int64) model.Manifest {
	return model.Manifest{
		Format:   "sesh",
		Version:  model.Schema,
		Started:  fixtureStarted,
		Finished: fixtureFinished,
		Source: model.Source{
			TmuxVersion:    "3.6",
			LibraryVersion: "v0.3.0",
			Consistency:    "observed",
			EndpointScope:  "",
			Options:        nil,
			Environment:    nil,
		},
		Evidence: nil,
		Sessions: []model.Session{{
			ID: "s0", Name: model.Bytes("work"), Directory: model.Bytes("/tmp"),
			Group: "", Options: nil, Environment: nil,
		}},
		Groups: nil,
		Windows: []model.Window{{
			ID: "w0", Name: model.Bytes("editor"), Width: fixtureWidth, Height: fixtureHeight,
			Layout: model.Layout{Width: fixtureWidth, Height: fixtureHeight, Left: 0, Top: 0, Pane: "p0", Axis: "", Children: nil},
			Zoomed: false, Options: nil,
		}},
		Links: []model.Link{{Session: "s0", Window: "w0", Index: 0, Active: true, Last: false, Flags: nil}},
		Panes: []model.Pane{{
			ID: "p0", Window: "w0", Index: 0, Title: nil, Directory: model.Bytes("/tmp"),
			Width: fixtureWidth, Height: fixtureHeight, Left: 0, Top: 0, Active: true, Dead: false, Options: nil,
			Terminal: model.Terminal{Artifact: "a0", History: 0, Alternate: false, CursorX: 0, CursorY: 0, Mode: nil},
			Launch: model.Launch{
				Executable: nil, Arguments: nil, Directory: nil, Environment: nil, Birth: nil,
				Recovery: model.RecoveryUnverified, Reason: "",
			},
		}},
		Artifacts: []model.Artifact{{ID: "a0", Kind: "terminal-text", Length: length, SHA256: digest}},
	}
}
