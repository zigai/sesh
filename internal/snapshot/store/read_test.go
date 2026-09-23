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

	file := filepath.Join(path, "generations", generation.ID, "manifest.json")

	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	changed := bytes.Replace(data, []byte(`"started":1`), []byte(`"started":2`), 1)
	if bytes.Equal(data, changed) || len(data) != len(changed) {
		t.Fatal("fixture did not change equal-sized contents")
	}

	rewriteFixtureFile(t, file, changed)

	if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Read(t.Context(), generation.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatal("stale validation cache accepted modified bytes", err)
	}
}
