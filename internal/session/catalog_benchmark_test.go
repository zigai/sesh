//go:build linux || darwin

package session

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func BenchmarkSavedSessionCatalog2000(b *testing.B) {
	const sessionCount = 2000

	repository, path := newBenchmarkCatalogRepository(b, sessionCount)

	b.Run("ReopenedRepository", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			reopened, err := store.OpenExisting(b.Context(), path)
			if err != nil {
				b.Fatal(err)
			}

			result, readErr := ListCatalog(b.Context(), CatalogOptions{Repository: reopened, RepositorySource: path})

			closeErr := reopened.Close()
			if readErr != nil || closeErr != nil {
				b.Fatalf("catalog read: %v; repository close: %v", readErr, closeErr)
			}

			checkBenchmarkCatalog(b, result, sessionCount)
		}
	})
	b.Run("ReusedRepository", func(b *testing.B) {
		options := CatalogOptions{Repository: repository, RepositorySource: path}

		result, err := ListCatalog(b.Context(), options)
		if err != nil {
			b.Fatal(err)
		}

		checkBenchmarkCatalog(b, result, sessionCount)
		b.ReportAllocs()

		for b.Loop() {
			result, err := ListCatalog(b.Context(), options)
			if err != nil {
				b.Fatal(err)
			}

			checkBenchmarkCatalog(b, result, sessionCount)
		}
	})
}

func newBenchmarkCatalogRepository(b *testing.B, size int) (*store.Repository, string) {
	b.Helper()

	path := filepath.Join(b.TempDir(), "repository")

	repository, err := store.Open(b.Context(), path)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		if err := repository.Close(); err != nil {
			b.Error(err)
		}
	})

	attempt, err := repository.Begin(b.Context())
	if err != nil {
		b.Fatal(err)
	}

	if _, err := repository.Commit(b.Context(), attempt.ID, benchmarkCatalogManifest(size), nil); err != nil {
		b.Fatal(err)
	}

	return repository, path
}

func checkBenchmarkCatalog(b *testing.B, result CatalogResult, size int) {
	b.Helper()

	if result.Status != StatusComplete || len(result.Sessions) != size {
		b.Fatalf("catalog status/count = %s/%d, want complete/%d", result.Status, len(result.Sessions), size)
	}

	last := result.Sessions[size-1]
	if last.ID != fmt.Sprintf("s%d", size-1) || last.WindowCount != 1 || last.Kind != KindSaved {
		b.Fatalf("last saved session = %+v", last)
	}
}

func benchmarkCatalogManifest(size int) model.Manifest {
	manifest := model.Manifest{
		Format: "sesh", Version: model.Schema, Started: 1, Finished: 2,
		Source: model.Source{TmuxVersion: "3.6", LibraryVersion: "v0.3.0", Consistency: "checkpoint"},
	}

	for index := range size {
		sessionID := fmt.Sprintf("s%d", index)
		windowID := fmt.Sprintf("w%d", index)
		paneID := fmt.Sprintf("p%d", index)
		manifest.Sessions = append(manifest.Sessions, model.Session{
			ID: sessionID, Name: model.Bytes(fmt.Sprintf("workspace-%04d", index)), Directory: model.Bytes("/tmp"),
		})
		manifest.Windows = append(manifest.Windows, model.Window{
			ID: windowID, Name: model.Bytes("editor"), Width: 80, Height: 24,
			Layout: model.Layout{Width: 80, Height: 24, Pane: paneID},
		})
		manifest.Links = append(manifest.Links, model.Link{Session: sessionID, Window: windowID, Active: true})
		manifest.Panes = append(manifest.Panes, model.Pane{
			ID: paneID, Window: windowID, Directory: model.Bytes("/tmp"), Width: 80, Height: 24, Active: true,
			Launch: model.Launch{Recovery: model.RecoveryUnverified},
		})
	}

	for index := 1; index <= 10; index++ {
		manifest.Evidence = append(manifest.Evidence, model.Evidence{Domain: fmt.Sprintf("S%02d", index), Status: "verified"})
	}

	return manifest
}
