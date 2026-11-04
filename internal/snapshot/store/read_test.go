//go:build linux || darwin

package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadsRevalidateSameSizeAndTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)

	generation := commitFixture(t, r)
	if _, err := r.Read(t.Context(), generation.ID); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(path, "generations", generation.ID, manifestFileName)

	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	changed := bytes.Clone(data)
	changed[len(changed)-1] ^= 1

	rewriteFixtureFile(t, file, changed)

	if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Read(t.Context(), generation.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatal("stale validation cache accepted modified bytes", err)
	}
}
