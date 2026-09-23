//go:build linux || darwin

package store

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/zigai/sesh/internal/strictjson"
)

func TestCatalogCorruptionFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(*Catalog){
		"unsupported version":    func(c *Catalog) { c.Version++ },
		"missing attempt":        func(c *Catalog) { c.Attempts = nil },
		"duplicate attempt":      func(c *Catalog) { c.Attempts = append(c.Attempts, c.Attempts[0]) },
		"unsafe generation":      func(c *Catalog) { c.Generations[0].ID = "../outside" },
		"missing current":        func(c *Catalog) { c.Current = newID() },
		"wrong sequence":         func(c *Catalog) { c.Generations[0].Sequence++ },
		"pending publication":    func(c *Catalog) { c.Attempts[0].State, c.Attempts[0].Reason = "pending", "capture_pending" },
		"false completeness":     func(c *Catalog) { c.Generations[0].Complete = false },
		"digest":                 func(c *Catalog) { c.Generations[0].ManifestSHA256 = "invalid" },
		"missing attempt digest": func(c *Catalog) { c.Attempts[0].ManifestSHA256 = "" },
		"changed attempt digest": func(c *Catalog) { c.Attempts[0].ManifestSHA256 = digest([]byte("different manifest")) },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			commitCompleteFixture(t, r)

			catalog, err := r.Inspect(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			mutate(&catalog)

			data, err := json.Marshal(catalog)
			if err != nil {
				t.Fatal(err)
			}

			writeCatalogCorruption(t, path, data)

			if _, err := r.Current(t.Context()); !errors.Is(err, ErrCorrupt) {
				t.Fatal("accepted corrupt catalog", err)
			}
		})
	}
}

func TestCatalogJSONCannotGuessOrFallback(t *testing.T) {
	for _, kind := range []string{"unknown", "duplicate", "missing", "null", "trailing", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repository")
			r := openRepository(t, path)
			commitCompleteFixture(t, r)

			data, err := os.ReadFile(filepath.Join(path, "catalog.json"))
			if err != nil {
				t.Fatal(err)
			}

			writeCatalogCorruption(t, path, malformedCatalogBytes(data, kind))

			if _, err := r.Current(t.Context()); err == nil {
				t.Fatal("accepted malformed catalog")
			}

			opened, err := Open(t.Context(), path)
			if opened != nil {
				if closeErr := opened.Close(); closeErr != nil {
					t.Error(closeErr)
				}
			}

			if err == nil {
				t.Fatal("reopen rebuilt corrupt catalog from orphaned bytes")
			}
		})
	}
}

func malformedCatalogBytes(data []byte, kind string) []byte {
	switch kind {
	case "unknown":
		return bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"other":true`), 1)
	case "duplicate":
		return bytes.Replace(data, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
	case "missing":
		return bytes.Replace(data, []byte(`"version":1,`), nil, 1)
	case "null":
		return bytes.Replace(data, []byte(`"version":1`), []byte(`"version":null`), 1)
	case "trailing":
		return append(data, []byte("\n{}")...)
	default:
		return bytes.Repeat([]byte(" "), maxCatalogBytes+1)
	}
}

func writeCatalogCorruption(t *testing.T, path string, data []byte) {
	t.Helper()

	rewriteFixtureFile(t, filepath.Join(path, "catalog.json"), data)
}

func FuzzNativeCatalog(f *testing.F) {
	seed := Catalog{Version: catalogVersion}

	data, err := json.Marshal(seed)
	if err != nil {
		f.Fatal(err)
	}

	f.Add(data)
	f.Add([]byte(`{"version":1,"sequence":0,"current":"../escape","attempts":[],"generations":[],"operations":[],"rewrites":[]}`))
	f.Add([]byte(`{"schemaVersion":1,"generation":1}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > maxCatalogBytes {
			return
		}

		catalog, err := strictjson.Decode[Catalog](input)
		if err != nil || catalog.validate() != nil {
			return
		}

		for _, generation := range catalog.Generations {
			if filepath.Base(generation.ID) != generation.ID || len(generation.ID) != idBytes*2 {
				t.Fatal("accepted escaping generation")
			}
		}

		encoded, err := json.Marshal(catalog)
		if err != nil {
			t.Fatal(err)
		}

		again, err := strictjson.Decode[Catalog](encoded)
		if err != nil {
			t.Fatal(err)
		}

		if diff := cmp.Diff(catalog, again, cmpopts.EquateEmpty()); diff != "" {
			t.Fatal(diff)
		}
	})
}
