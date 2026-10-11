package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/samber/oops"
)

var errTestHistoryList = errors.New("test history list failure")

func TestResolveSessionDirectoryErrorWrapping(t *testing.T) {
	t.Parallel()

	t.Run("directory not found wraps oops", func(t *testing.T) {
		t.Parallel()

		cmd := newSessionNewCommand()
		missing := filepath.Join(t.TempDir(), "nonexistent")
		_ = cmd.Flags().Set("dir", missing)

		_, err := resolveSessionDirectory(cmd)
		if !errors.Is(err, errDirectoryNotFound) {
			t.Fatalf("expected errDirectoryNotFound, got %v", err)
		}

		oopsErr, ok := oops.AsOops(err)
		if !ok {
			t.Fatalf("expected oops error, got %T", err)
		}

		if oopsErr.Domain() != "cli" || oopsErr.Context()["operation"] != "stat_directory" {
			t.Fatalf("unexpected oops metadata: domain=%q context=%v", oopsErr.Domain(), oopsErr.Context())
		}
	})

	t.Run("directory not a dir wraps oops", func(t *testing.T) {
		t.Parallel()

		filePath := filepath.Join(t.TempDir(), "a-file")
		if err := os.WriteFile(filePath, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}

		cmd := newSessionNewCommand()
		_ = cmd.Flags().Set("dir", filePath)

		_, err := resolveSessionDirectory(cmd)
		if !errors.Is(err, errDirectoryNotDir) {
			t.Fatalf("expected errDirectoryNotDir, got %v", err)
		}

		oopsErr, ok := oops.AsOops(err)
		if !ok {
			t.Fatalf("expected oops error, got %T", err)
		}

		if oopsErr.Domain() != "cli" || oopsErr.Context()["operation"] != "stat_directory" {
			t.Fatalf("unexpected oops metadata: domain=%q context=%v", oopsErr.Domain(), oopsErr.Context())
		}
	})
}

func TestWrapHistoryListError(t *testing.T) {
	t.Parallel()

	if err := wrapHistoryListError(nil); err != nil {
		t.Fatalf("expected nil for nil error, got %v", err)
	}

	wrapped := wrapHistoryListError(errTestHistoryList)
	if !errors.Is(wrapped, errTestHistoryList) {
		t.Fatalf("expected errors.Is match, got %v", wrapped)
	}

	oopsErr, ok := oops.AsOops(wrapped)
	if !ok {
		t.Fatalf("expected oops error, got %T", wrapped)
	}

	if oopsErr.Domain() != "cli" || oopsErr.Context()["operation"] != "history" {
		t.Fatalf("unexpected oops metadata: domain=%q context=%v", oopsErr.Domain(), oopsErr.Context())
	}

	again := wrapHistoryListError(wrapped)

	againErr, ok := oops.AsOops(again)
	if !ok || len(againErr.Layers()) != len(oopsErr.Layers()) || again.Error() != wrapped.Error() {
		t.Fatalf("expected idempotent return of oops error, got %v", again)
	}
}
