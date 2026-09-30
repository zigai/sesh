//go:build linux || darwin

package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestSavedCatalogPreservesBytesAndNanosecondTimestamp(t *testing.T) {
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
	gen := commitTestSnapshot(t, repo, "name-\xff", "/tmp/path-\xfd", 1)

	rows, err := ListSaved(t.Context(), repo, path, gen.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("saved catalog: %v, %d", err, len(rows))
	}

	data, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}

	var row Session
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}

	if string(row.NameRaw) != "name-\xff" || string(row.PathRaw) != "/tmp/path-\xfd" || row.Activity.UnixNano() != 2000 {
		t.Fatalf("native bytes or timestamp changed: %+v", row)
	}
}

func TestCatalogAbsentServerRetainsIndependentFailures(t *testing.T) {
	if !serverAbsent(fmt.Errorf("query: %w", gotmux.ErrNoServer)) {
		t.Fatal("missing server not classified")
	}

	for _, cause := range []error{context.Canceled, io.ErrUnexpectedEOF} {
		if serverAbsent(fmt.Errorf("query: %w", errors.Join(gotmux.ErrNoServer, cause))) {
			t.Fatal("independent failure hidden")
		}
	}

	command := &gotmux.CommandError{Err: gotmux.ErrNoServer}
	if !serverAbsent(command) {
		t.Fatal("classified server absence not recognized")
	}

	if serverAbsent(fmt.Errorf("query: %w", errors.Join(command, io.ErrUnexpectedEOF))) {
		t.Fatal("classified command hid joined cleanup failure")
	}

	timedOut := &gotmux.CommandError{Err: command, Timeout: gotmux.TimeoutSourceCaller}
	if serverAbsent(fmt.Errorf("query: %w", timedOut)) {
		t.Fatal("nested classified command hid outer timeout")
	}
}

func commitTestSnapshot(t *testing.T, r *store.Repository, sessionName, dir string, linkCount int) store.Generation {
	t.Helper()

	return commitTestManifest(t, r, testSnapshotManifest(sessionName, dir, linkCount))
}

// commitTestObservation commits the observation-only result every real capture produces.
func commitTestObservation(t *testing.T, r *store.Repository, sessionName, dir string) store.Generation {
	t.Helper()

	m := testSnapshotManifest(sessionName, dir, 1)
	m.Source.Consistency = "observed"

	for i := range m.Evidence {
		m.Evidence[i].Status, m.Evidence[i].Reason = "observed", "observed_only"
	}

	return commitTestManifest(t, r, m)
}

func commitTestManifest(t *testing.T, r *store.Repository, m model.Manifest) store.Generation {
	t.Helper()

	attempt, err := r.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	gen, err := r.Commit(t.Context(), attempt.ID, m, map[string]io.Reader{
		"a0": bytes.NewReader(testSnapshotContent),
	})
	if err != nil {
		t.Fatal(err)
	}

	return gen
}

var testSnapshotContent = []byte("terminal content\n")

func testSnapshotManifest(sessionName, dir string, linkCount int) model.Manifest {
	content := testSnapshotContent
	hash := sha256.Sum256(content)

	m := model.Manifest{
		Format:   "sesh",
		Version:  model.Schema,
		Started:  1000,
		Finished: 2000,
		Source: model.Source{
			TmuxVersion:    "3.6",
			LibraryVersion: "v0.3.0",
			Consistency:    "checkpoint",
		},
		Sessions: []model.Session{
			{
				ID:        "s0",
				Name:      model.Bytes(sessionName),
				Directory: model.Bytes(dir),
			},
		},
		Windows: []model.Window{
			{
				ID:     "w0",
				Name:   model.Bytes("main"),
				Width:  80,
				Height: 24,
				Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"},
			},
		},
		Panes: []model.Pane{
			{
				ID:        "p0",
				Window:    "w0",
				Directory: model.Bytes(dir),
				Width:     80,
				Height:    24,
				Active:    true,
				Terminal:  model.Terminal{Artifact: "a0"},
				Launch:    model.Launch{Recovery: "unverified"},
			},
		},
		Artifacts: []model.Artifact{
			{
				ID:     "a0",
				Kind:   "terminal-text",
				Length: int64(len(content)),
				SHA256: hex.EncodeToString(hash[:]),
			},
		},
	}

	for i := range linkCount {
		m.Links = append(m.Links, model.Link{
			Session: "s0",
			Window:  "w0",
			Index:   i,
			Active:  i == 0,
		})
	}

	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		m.Evidence = append(m.Evidence, model.Evidence{
			Domain: domain,
			Status: "verified",
			Reason: "",
		})
	}

	return m
}

func TestListCatalogNilOptionsReturnsEmpty(t *testing.T) {
	result, err := ListCatalog(t.Context(), CatalogOptions{})
	if err != nil {
		t.Fatalf("ListCatalog() error = %v, want nil", err)
	}

	if result.Status != StatusComplete {
		t.Fatalf("result.Status = %v, want complete", result.Status)
	}

	if len(result.Sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(result.Sessions))
	}
}

func TestListSavedMissingRepositoryNoEffects(t *testing.T) {
	sessions, err := ListSaved(t.Context(), nil, "repo-source", "")
	if err != nil {
		t.Fatalf("ListSaved(nil) error = %v, want nil", err)
	}

	if sessions != nil {
		t.Fatalf("expected nil sessions, got %#v", sessions)
	}
}

func TestListSavedPreservesManifestBytesAndMetadata(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()

	specialName := "special-🚀-name"
	specialDir := "/work/dir with spaces and utf8-✨"
	gen := commitTestSnapshot(t, repo, specialName, specialDir, 3)

	currentSessions, err := ListSaved(t.Context(), repo, repoPath, "")
	if err != nil {
		t.Fatalf("ListSaved(current) error = %v", err)
	}

	if len(currentSessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(currentSessions))
	}

	assertSavedSessionProperties(t, currentSessions[0], specialName, specialDir, gen.ID, repoPath, 3)

	explicitSessions, err := ListSaved(t.Context(), repo, repoPath, gen.ID)
	if err != nil {
		t.Fatalf("ListSaved(explicit) error = %v", err)
	}

	if len(explicitSessions) != 1 || explicitSessions[0].Name != specialName {
		t.Fatalf("explicit generation mismatch: %#v", explicitSessions)
	}
}

func TestCatalogListsSavedSessionsThroughLockFreeView(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()

	gen := commitTestSnapshot(t, repo, "viewed", "/work/viewed", 2)

	view, err := store.OpenView(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.Close() }()

	result, err := ListCatalog(t.Context(), CatalogOptions{Repository: view, RepositorySource: repoPath})
	if err != nil || result.Status != StatusComplete || len(result.Sessions) != 1 {
		t.Fatalf("catalog through view = %+v, %v; want one complete saved row", result, err)
	}

	assertSavedSessionProperties(t, result.Sessions[0], "viewed", "/work/viewed", gen.ID, repoPath, 2)
}

func TestCatalogThroughViewOfMissingRepositoryIsComplete(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "missing")

	view, err := store.OpenView(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.Close() }()

	result, err := ListCatalog(t.Context(), CatalogOptions{Repository: view, RepositorySource: repoPath})
	if err != nil || result.Status != StatusComplete || len(result.Sessions) != 0 {
		t.Fatalf("catalog of missing repository = %+v, %v; want complete and empty", result, err)
	}
}

func TestListSavedListsLatestObservationOnlySave(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()

	gen := commitTestObservation(t, repo, "observed", "/work")

	sessions, err := ListSaved(t.Context(), repo, repoPath, "")
	if err != nil {
		t.Fatalf("ListSaved(current) error = %v, want the observation-only save listed", err)
	}

	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	assertSavedSessionProperties(t, sessions[0], "observed", "/work", gen.ID, repoPath, 1)
}

func TestListSavedPreservesRecoveryBarriers(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()

	commitTestSnapshot(t, repo, "good", "/tmp", 1)

	_, err = repo.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	_, err = ListSaved(t.Context(), reopened, repoPath, "")
	if err == nil {
		t.Fatal("expected error on pending attempt barrier, got nil")
	}

	if !errors.Is(err, store.ErrRecoveryRequired) {
		t.Fatalf("expected store.ErrRecoveryRequired, got %v", err)
	}
}

func TestListCatalogConstituentFailureExposesPartialAndError(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()

	opts := CatalogOptions{
		Repository:       repo,
		RepositorySource: repoPath,
		GenerationID:     "non-existent-generation",
	}

	result, err := ListCatalog(t.Context(), opts)
	if err == nil {
		t.Fatal("expected error on constituent failure, got nil")
	}

	if result.Status != StatusFailed && result.Status != StatusPartial {
		t.Fatalf("result.Status = %v, want failed or partial", result.Status)
	}

	if result.SavedErr == nil {
		t.Fatal("result.SavedErr should not be nil")
	}
}

func TestListCatalogBoundsTotalInventory(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()

	attempt, err := repo.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("terminal content\n")
	hash := sha256.Sum256(content)

	m := model.Manifest{
		Format:   "sesh",
		Version:  model.Schema,
		Started:  1,
		Finished: 2,
		Source:   model.Source{TmuxVersion: "3.6", LibraryVersion: "v0.3.0", Consistency: "checkpoint"},
		Windows: []model.Window{
			{
				ID:     "w0",
				Name:   model.Bytes("main"),
				Width:  80,
				Height: 24,
				Layout: model.Layout{Width: 80, Height: 24, Pane: "p0"},
			},
		},
		Panes: []model.Pane{
			{
				ID:        "p0",
				Window:    "w0",
				Directory: model.Bytes("/tmp"),
				Width:     80,
				Height:    24,
				Active:    true,
				Terminal:  model.Terminal{Artifact: "a0"},
				Launch:    model.Launch{Recovery: "unverified"},
			},
		},
		Artifacts: []model.Artifact{
			{
				ID:     "a0",
				Kind:   "terminal-text",
				Length: int64(len(content)),
				SHA256: hex.EncodeToString(hash[:]),
			},
		},
	}

	sessionCount := 4500
	for i := range sessionCount {
		id := "s" + strconv.Itoa(i)
		m.Sessions = append(m.Sessions, model.Session{
			ID:        id,
			Name:      model.Bytes("sess-" + strconv.Itoa(i)),
			Directory: model.Bytes("/tmp"),
		})
		m.Links = append(m.Links, model.Link{
			Session: id,
			Window:  "w0",
			Index:   0,
			Active:  true,
		})
	}

	for _, domain := range []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"} {
		m.Evidence = append(m.Evidence, model.Evidence{Domain: domain, Status: "verified", Reason: ""})
	}

	_, err = repo.Commit(t.Context(), attempt.ID, m, map[string]io.Reader{
		"a0": bytes.NewReader(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := ListCatalog(t.Context(), CatalogOptions{Repository: repo, RepositorySource: repoPath})
	if !errors.Is(err, ErrCatalogLimitExceeded) {
		t.Fatalf("ListCatalog() error = %v, want ErrCatalogLimitExceeded", err)
	}

	if result.Status != StatusPartial {
		t.Fatalf("result.Status = %v, want StatusPartial", result.Status)
	}

	if len(result.Sessions) != maxCatalogInventory {
		t.Fatalf("inventory count = %d, want exactly maxCatalogInventory %d", len(result.Sessions), maxCatalogInventory)
	}
}

func TestListSavedRejectsEmptyOrInvalidSource(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()

	commitTestSnapshot(t, repo, "good", "/tmp", 1)

	if _, err := ListSaved(t.Context(), repo, "", ""); !errors.Is(err, ErrEmptyRepositorySource) {
		t.Fatalf("empty source err = %v, want ErrEmptyRepositorySource", err)
	}

	if _, err := ListSaved(t.Context(), repo, "   ", ""); !errors.Is(err, ErrEmptyRepositorySource) {
		t.Fatalf("whitespace source err = %v, want ErrEmptyRepositorySource", err)
	}

	if _, err := ListSaved(t.Context(), repo, "bad\xffsource", ""); !errors.Is(err, ErrEmptyRepositorySource) {
		t.Fatalf("invalid UTF-8 source err = %v, want ErrEmptyRepositorySource", err)
	}
}

func assertSavedSessionProperties(t *testing.T, s Session, wantName, wantDir, wantGen, wantSource string, wantLinks int) {
	t.Helper()

	if s.ID != "s0" {
		t.Errorf("ID = %q, want s0", s.ID)
	}

	if s.RepositorySource != wantSource {
		t.Errorf("RepositorySource = %q, want %q", s.RepositorySource, wantSource)
	}

	if s.Name != wantName {
		t.Errorf("Name = %q, want %q", s.Name, wantName)
	}

	if s.ActivePanePath != wantDir {
		t.Errorf("ActivePanePath = %q, want %q", s.ActivePanePath, wantDir)
	}

	if s.Kind != KindSaved {
		t.Errorf("Kind = %v, want KindSaved", s.Kind)
	}

	if s.Generation != wantGen {
		t.Errorf("Generation = %q, want %q", s.Generation, wantGen)
	}

	if s.WindowCount != wantLinks {
		t.Errorf("WindowCount = %d, want %d", s.WindowCount, wantLinks)
	}

	if s.Activity.UnixNano() != 2000 {
		t.Errorf("Activity = %v, want unix nanoseconds 2000", s.Activity)
	}

	if s.Attached {
		t.Error("Attached should be false for saved sessions")
	}

	if s.IsCurrent {
		t.Error("IsCurrent should be false for saved sessions")
	}
}
