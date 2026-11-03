package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/tui/providers"
)

func TestResourceCacheIsPrivateAndReopensRecentSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "resource-sample.json")

	sample := providers.ResourceSample{At: time.Now(), Processes: map[int]providers.ProcessSample{
		123: {PID: 123, StartTime: 7, CPUTicks: 100},
	}}
	if err := writeResourceCache(path, sample); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permission = %o", info.Mode().Perm())
	}

	got := readResourceCache(path)
	if got.Processes[123].StartTime != 7 || got.At.IsZero() {
		t.Fatalf("reopened sample = %+v", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink("/etc/passwd", path); err != nil {
		t.Fatal(err)
	}

	if got := readResourceCache(path); !got.At.IsZero() {
		t.Fatalf("symlink cache read = %+v", got)
	}
}
