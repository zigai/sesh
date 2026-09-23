//go:build linux || darwin

package store

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/model"
)

type cancelArtifactWriter struct {
	cancel context.CancelFunc
	err    error
}

func (w cancelArtifactWriter) Write(data []byte) (int, error) {
	w.cancel()

	if w.err != nil {
		return 0, w.err
	}

	return len(data), nil
}

func TestArtifactVerificationPreservesFinalReadCancellation(t *testing.T) {
	for name, outputErr := range map[string]error{"successful_output": nil, "failed_output": io.ErrClosedPipe} {
		t.Run(name, func(t *testing.T) {
			file, descriptor := artifactVerificationFile(t, []byte("synthetic artifact"))

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			_, err := verifyArtifactFile(ctx, file, descriptor, cancelArtifactWriter{cancel: cancel, err: outputErr})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("final read cancellation lost: %v", err)
			}

			if outputErr != nil && !errors.Is(err, outputErr) {
				t.Fatalf("independent output failure lost: %v", err)
			}
		})
	}
}

func TestArtifactVerificationRejectsOversizedCompressedFile(t *testing.T) {
	file, descriptor := artifactVerificationFile(t, []byte("synthetic artifact"))
	if err := file.Truncate(maxCompressedBytes + 1); err != nil {
		t.Fatal(err)
	}

	_, err := verifyArtifactFile(t.Context(), file, descriptor, io.Discard)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("oversized compressed file accepted: %v", err)
	}
}

func artifactVerificationFile(t *testing.T, content []byte) (*os.File, model.Artifact) {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), "artifact")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})

	compressed := gzip.NewWriter(file)

	_, writeErr := compressed.Write(content)
	if err := errors.Join(writeErr, compressed.Close()); err != nil {
		t.Fatal(err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	return file, model.Artifact{ID: "a0", Kind: "terminal-text", Length: int64(len(content)), SHA256: digest(content)}
}
