//go:build linux || darwin

package store

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func readManifestFixture(t *testing.T, file string) []byte {
	t.Helper()

	compressed, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	data, err := decompressManifest(compressed)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func writeManifestFixture(t *testing.T, file string, data []byte) {
	t.Helper()

	compressed, err := compressManifest(data)
	if err != nil {
		t.Fatal(err)
	}

	rewriteFixtureFile(t, file, compressed)
}

func gzipFixture(t *testing.T, data []byte) []byte {
	t.Helper()

	var compressed bytes.Buffer

	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return compressed.Bytes()
}

func TestCommittedManifestIsCompressedAndReadsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generation := commitContentFixture(t, r, []byte("content"))
	directory := filepath.Join(path, "generations", generation.ID)

	if _, err := os.Stat(
		filepath.Join(directory, "manifest.json"),
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("plain manifest stat error = %v, want not exist", err)
	}

	data := readManifestFixture(t, filepath.Join(directory, "manifest.json.gz"))
	if digest(data) != generation.ManifestSHA256 {
		t.Fatal("catalog digest does not cover the uncompressed manifest")
	}

	if _, err := r.Read(t.Context(), generation.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReadRejectsDamagedManifestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	generation := commitContentFixture(t, r, []byte("content"))
	file := filepath.Join(path, "generations", generation.ID, manifestFileName)
	data := readManifestFixture(t, file)
	valid := gzipFixture(t, data)

	cases := map[string][]byte{
		"plain json":      data,
		"truncated gzip":  valid[:len(valid)-5],
		"trailing bytes":  append(bytes.Clone(valid), "trailing"...),
		"second stream":   append(bytes.Clone(valid), gzipFixture(t, []byte("{}"))...),
		"other content":   gzipFixture(t, []byte(`{"format":"sesh"}`)),
		"oversized bytes": gzipFixture(t, []byte(strings.Repeat(" ", model.MaxManifestBytes+1))),
		"empty":           {},
	}

	for name, damaged := range cases {
		t.Run(name, func(t *testing.T) {
			rewriteFixtureFile(t, file, damaged)

			if _, err := r.Read(t.Context(), generation.ID); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Read error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestOpenRejectsRepositoryWithUncompressedManifestVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository")
	r := openRepository(t, path)
	commitContentFixture(t, r, []byte("content"))

	catalog, err := os.ReadFile(filepath.Join(path, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}

	writeCatalogCorruption(
		t,
		path,
		bytes.Replace(catalog, []byte(`"version":2`), []byte(`"version":1`), 1),
	)

	reopened, err := Open(t.Context(), path)
	if reopened != nil {
		_ = reopened.Close()
	}

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open error = %v, want ErrCorrupt", err)
	}
}
