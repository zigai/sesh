package autosave

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/session"
)

func TestLoadAutosaveHistoryDistinguishesMissingFromUnreadable(t *testing.T) {
	stateHome := t.TempDir()

	path := filepath.Join(stateHome, "sesh", "history.json")

	last, err := loadAutosaveHistory(path)
	if err != nil || last != 0 {
		t.Fatalf("missing history = (%d, %v), want (0, nil)", last, err)
	}

	if _, err := session.UpdateState(t.Context(), path, func(state *session.State) {
		state.LastAutosaveUnix = 1_700_000_000
	}); err != nil {
		t.Fatalf("recording history failed: %v", err)
	}

	last, err = loadAutosaveHistory(path)
	if err != nil || last != 1_700_000_000 {
		t.Fatalf("recorded history = (%d, %v), want the recorded timestamp", last, err)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := loadAutosaveHistory(path); err == nil {
		t.Fatal("an unreadable history file must report an error instead of a zero timestamp")
	}
}
