//go:build linux || darwin

package automation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServerLossIsUnreportedUntilRecordedForThatAutosave(t *testing.T) {
	t.Parallel()

	path := ServerLossMarkerPath(filepath.Join(t.TempDir(), "state", "history.json"))

	unreported, err := ServerLossUnreported(path, 100)
	if err != nil || !unreported {
		t.Fatalf("a loss after an autosave must start unreported, got %v, %v", unreported, err)
	}

	if err := RecordServerLossReported(path, 100); err != nil {
		t.Fatalf("RecordServerLossReported failed: %v", err)
	}

	unreported, err = ServerLossUnreported(path, 100)
	if err != nil || unreported {
		t.Fatalf("the reported loss must stay reported, got %v, %v", unreported, err)
	}

	unreported, err = ServerLossUnreported(path, 200)
	if err != nil || !unreported {
		t.Fatalf("a later autosave starts a new episode, got %v, %v", unreported, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("marker mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestServerLossNeedsARecordedAutosave(t *testing.T) {
	t.Parallel()

	path := ServerLossMarkerPath(filepath.Join(t.TempDir(), "history.json"))

	for _, stamp := range []int64{0, -1} {
		unreported, err := ServerLossUnreported(path, stamp)
		if err != nil || unreported {
			t.Fatalf("stamp %d has nothing to lose, got %v, %v", stamp, unreported, err)
		}
	}
}

func TestServerLossRejectsACorruptMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := ServerLossMarkerPath(filepath.Join(dir, "history.json"))

	for name, content := range map[string]string{
		"not json":       "{not json",
		"unknown member": `{"reportedFor":1,"extra":true}`,
		"missing member": `{}`,
		"negative stamp": `{"reportedFor":-5}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := ServerLossUnreported(path, 10); err == nil {
			t.Fatalf("%s must be rejected, not treated as an unreported loss", name)
		}
	}
}
