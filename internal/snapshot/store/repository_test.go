//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/zigai/sesh/internal/snapshot/model"
)

var errInjected = errors.New("injected storage fault")

func snapshotFixture() (model.Manifest, map[string]io.Reader) {
	content := []byte("  synthetic \x00\xff terminal history\n")
	hash := sha256.Sum256(content)

	m := model.Manifest{
		Format: "sesh", Version: model.Schema, Started: 1, Finished: 2,
		Source:    model.Source{TmuxVersion: "3.6", LibraryVersion: "v0.3.0", Consistency: "observed"},
		Sessions:  []model.Session{{ID: "s0", Name: model.Bytes("work"), Directory: model.Bytes("/tmp")}},
		Windows:   []model.Window{{ID: "w0", Name: model.Bytes("editor"), Width: 80, Height: 24, Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"}}},
		Links:     []model.Link{{Session: "s0", Window: "w0", Index: 7, Active: true}},
		Panes:     []model.Pane{{ID: "p0", Window: "w0", Directory: model.Bytes("/tmp"), Width: 80, Height: 24, Active: true, Terminal: model.Terminal{Artifact: "a0"}, Launch: model.Launch{Recovery: "unverified"}}},
		Artifacts: []model.Artifact{{ID: "a0", Kind: "terminal-text", Length: int64(len(content)), SHA256: hex.EncodeToString(hash[:])}},
	}
	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		m.Evidence = append(m.Evidence, model.Evidence{Domain: domain, Status: "unverified", Reason: "not_captured"})
	}

	return m, map[string]io.Reader{"a0": bytes.NewReader(content)}
}

func openRepository(t *testing.T, path string) *Repository {
	t.Helper()

	r, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})

	return r
}

func commitFixture(t *testing.T, r *Repository) Generation {
	t.Helper()

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	m, artifacts := snapshotFixture()

	generation, err := r.Commit(t.Context(), attempt.ID, m, artifacts)
	if err != nil {
		t.Fatal(err)
	}

	return generation
}

func TestRepositoryRoundTripAndExplicitIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generation := commitFixture(t, r)
	reopened := openRepository(t, path)

	got, err := reopened.Read(t.Context(), generation.ID)
	if err != nil {
		t.Fatal(err)
	}

	want, _ := snapshotFixture()
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Fatal(diff)
	}

	if _, err := reopened.Current(t.Context()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("automatic recovery = %v", err)
	}

	err = filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect fixture: %w", err)
		}

		want := os.FileMode(0o600)
		if entry.IsDir() {
			want = 0o700
		}

		if info.Mode().Perm() != want {
			t.Errorf("wrong mode for fixture %s: %v", path, info.Mode())
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicationFaultsPreservePriorGeneration(t *testing.T) {
	for _, phase := range []string{faultStageSynced, faultGenerationPublished, faultCatalogSynced, faultCatalogReplaced} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			prior := commitFixture(t, r)

			attempt, err := r.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			r.fault = func(current string) error {
				if current == phase {
					return errInjected
				}

				return nil
			}
			m, artifacts := snapshotFixture()

			_, err = r.Commit(t.Context(), attempt.ID, m, artifacts)
			if !errors.Is(err, errInjected) {
				t.Fatalf("fault = %v", err)
			}

			if phase == faultCatalogReplaced && !errors.Is(err, ErrUncertain) {
				t.Fatal("lost uncertainty classification")
			}

			verifyPublication(t, path, prior.ID, attempt.ID, phase == faultCatalogReplaced)
		})
	}
}

func TestCorruptionAndArtifactLimits(t *testing.T) {
	for _, kind := range []string{"manifest", "artifact", "trailing", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			generation := commitFixture(t, r)
			name := filepath.Join(path, "generations", generation.ID, "artifacts", "a0.bin.gz")

			corruptFixture(t, path, generation.ID, name, kind)

			if _, err := r.Read(t.Context(), generation.ID); err == nil {
				t.Fatal("accepted corrupt generation")
			}
		})
	}
}

func TestArtifactLimits(t *testing.T) {
	for _, content := range []string{"", "wrong", "too much content beyond declared length"} {
		r := openRepository(t, filepath.Join(t.TempDir(), "repository"))

		attempt, err := r.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		m, artifacts := snapshotFixture()

		artifacts["a0"] = bytes.NewBufferString(content)
		if _, err := r.Commit(t.Context(), attempt.ID, m, artifacts); err == nil {
			t.Fatal("accepted incorrect artifact")
		}
	}
}

func TestPinsAndUnknownActionsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	source := commitFixture(t, r)

	op, err := r.Pin(t.Context(), source.ID, model.Bytes("explicit-synthetic-target"), []Action{{ID: "launch0", Kind: actionLaunch, State: actionPlanned}})
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Transition(t.Context(), op.ID, "launch0", actionPlanned, actionIssued, nil); err != nil {
		t.Fatal(err)
	}

	if err := r.Transition(t.Context(), op.ID, "launch0", actionIssued, actionUnknown, nil); err != nil {
		t.Fatal(err)
	}

	reopened := openRepository(t, path)
	if err := reopened.Transition(t.Context(), op.ID, "launch0", actionUnknown, actionPlanned, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("uncertain launch replay allowed: %v", err)
	}

	if err := reopened.Finish(t.Context(), op.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatal("unverified completion allowed")
	}

	for range 6 {
		commitFixture(t, r)
	}

	if _, err := reopened.Retain(t.Context(), minimumRetained); err != nil {
		t.Fatal(err)
	}

	if _, err := reopened.Read(t.Context(), source.ID); err != nil {
		t.Fatal("pinned source lost", err)
	}

	if err := reopened.Delete(t.Context(), source.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("pinned source deletion allowed")
	}

	if err := reopened.Finish(t.Context(), op.ID, true); err != nil {
		t.Fatal(err)
	}

	if err := reopened.Delete(t.Context(), source.ID); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentAttemptsAndCanceledLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	other := openRepository(t, path)

	var workers sync.WaitGroup

	errorsFound := make(chan error, 2)

	for _, repository := range []*Repository{r, other} {
		workers.Go(func() { _, err := repository.Begin(t.Context()); errorsFound <- err })
	}

	workers.Wait()
	close(errorsFound)

	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}

	catalog, err := r.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Attempts) != 2 || catalog.Sequence != 2 || catalog.Attempts[0].ID == catalog.Attempts[1].ID {
		t.Fatal("lost concurrent attempt")
	}

	lock, err := r.root.Lock(t.Context(), "repository.lock")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, acquireErr := other.Begin(ctx)

	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	if !errors.Is(acquireErr, context.Canceled) {
		t.Fatalf("lock cancellation: %v", acquireErr)
	}
}

func verifyPublication(t *testing.T, path, prior, attempt string, published bool) {
	t.Helper()

	reopened := openRepository(t, path)
	if _, err := reopened.Read(t.Context(), prior); err != nil {
		t.Fatal(err)
	}

	catalog, err := reopened.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if (catalog.generationIndex(attempt) >= 0) != published {
		t.Fatal("incorrect visibility boundary")
	}

	if published {
		m, retryArtifacts := snapshotFixture()
		if _, err := reopened.Commit(t.Context(), attempt, m, retryArtifacts); err != nil {
			t.Fatal(err)
		}
	} else if _, err := reopened.Read(t.Context(), attempt); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("orphan promoted: %v", err)
	}
}

func corruptFixture(t *testing.T, path, generation, name, kind string) {
	t.Helper()

	switch kind {
	case "manifest":
		name = filepath.Join(path, "generations", generation, "manifest.json")
	case "trailing":
		file, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}

		_, writeErr := file.WriteString("trailing")
		if err := errors.Join(writeErr, file.Close()); err != nil {
			t.Fatal(err)
		}
	case "symlink":
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), name); err != nil {
			t.Fatal(err)
		}
	}

	if kind == "manifest" || kind == "artifact" {
		if err := os.WriteFile(name, []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func rewriteFixtureFile(t *testing.T, path string, data []byte) {
	t.Helper()

	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}

	writeErr := root.WriteFile(filepath.Base(path), data, 0o600)
	if err := errors.Join(writeErr, root.Close()); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyPointerCannotInitializeNativeRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	legacy := filepath.Join(t.TempDir(), "old-snapshot.txt")
	content := []byte("pane\tsynthetic\t0\t0\n")
	rewriteFixtureFile(t, legacy, content)

	if err := os.Symlink(legacy, filepath.Join(path, "last")); err != nil {
		t.Fatal(err)
	}

	r, err := Open(t.Context(), path)
	if r != nil {
		if closeErr := r.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatal("legacy pointer admitted as native repository", err)
	}

	after, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(content, after) {
		t.Fatal("external legacy source changed", err)
	}
}
