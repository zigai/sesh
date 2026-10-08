package fields_test

import (
	"maps"
	"os"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/fields"
)

func TestCacheRoundTripsEntries(t *testing.T) {
	path := fields.CachePath("weather")

	entries := map[string]fields.CacheEntry{
		"%12":        {ChangeKey: "a1", Text: "\x1b[32mok\x1b[0m", At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		"session:$1": {ChangeKey: "b2", Text: "", At: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
	}
	if err := fields.MergeCache(t.Context(), path, entries); err != nil {
		t.Fatal(err)
	}

	got, err := fields.ReadCache(path)
	if err != nil {
		t.Fatal(err)
	}

	if !maps.Equal(got, entries) {
		t.Fatalf("ReadCache() = %#v, want %#v", got, entries)
	}

	if err := fields.MergeCache(t.Context(), path, map[string]fields.CacheEntry{}); err != nil {
		t.Fatal(err)
	}

	if got, err := fields.ReadCache(path); err != nil || !maps.Equal(got, entries) {
		t.Fatalf("ReadCache() after empty update = %#v, %v, want %#v", got, err, entries)
	}
}

func TestReadCacheRejectsMissingCorruptAndOtherVersions(t *testing.T) {
	path := fields.CachePath("build")

	if got, err := fields.ReadCache(path); err == nil || got != nil {
		t.Fatalf("missing cache = %#v, %v", got, err)
	}

	if err := fields.MergeCache(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}

	for name, content := range map[string]string{
		"corrupt":       "{not json",
		"other version": `{"version":2,"entries":{"%1":{"changeKey":"x","text":"y"}}}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		if got, _ := fields.ReadCache(path); got != nil {
			t.Fatalf("%s cache = %#v, want no entries", name, got)
		}
	}
}
