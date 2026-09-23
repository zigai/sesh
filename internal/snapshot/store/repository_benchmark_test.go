//go:build linux || darwin

package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkRepositoryHistory(b *testing.B) {
	for _, size := range []int{1, 100, 1000, 10000} {
		b.Run(fmt.Sprintf("Attempts%d", size), func(b *testing.B) {
			repository := newBenchmarkHistoryRepository(b, size)
			b.ReportAllocs()

			for b.Loop() {
				catalog, err := repository.Inspect(b.Context())
				if err != nil {
					b.Fatal(err)
				}

				if len(catalog.Attempts) != size || catalog.Attempts[size-1].Sequence != catalog.Sequence {
					b.Fatalf("history count/sequence = %d/%d, want %d", len(catalog.Attempts), catalog.Sequence, size)
				}
			}
		})
	}
}

func newBenchmarkHistoryRepository(b *testing.B, size int) *Repository {
	b.Helper()

	repository, err := Open(b.Context(), filepath.Join(b.TempDir(), "repository"))
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() {
		if err := repository.Close(); err != nil {
			b.Error(err)
		}
	})

	if err := repository.update(b.Context(), func(catalog *Catalog) error {
		for index := range size {
			catalog.Sequence++
			catalog.Attempts = append(catalog.Attempts, Attempt{
				ID: fmt.Sprintf("%032x", index+1), Sequence: catalog.Sequence, State: "failed", Reason: "save_failed",
			})
		}

		return nil
	}); err != nil {
		b.Fatal(err)
	}

	return repository
}
