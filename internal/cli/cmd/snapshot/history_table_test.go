package snapshot

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func renderHistoryForTest(t *testing.T, repoPath string, args ...string) string {
	t.Helper()

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = repo.Close() }()

	catalog, err := repo.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	cmd := newSnapshotHistoryCommand()
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)

	if err := renderHistoryTable(cmd, loadHistoryEntries(t.Context(), repo, catalog, historyLimit(cmd)), len(catalog.Attempts), false); err != nil {
		t.Fatal(err)
	}

	return stdout.String()
}

func TestHistoryShowsSavesInPlainWordsAndWhichOneRestoreUses(t *testing.T) {
	t.Parallel()

	repoPath := filepath.Join(t.TempDir(), "repository")
	snapfixture.CommitOneSession(t, repoPath)

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repo.Begin(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	out := renderHistoryForTest(t, repoPath)

	for _, want := range []string{
		"Saved", "Result", "Sessions", "unfinished", "saved", "yes",
		"The latest save did not finish; restore uses the newest saved generation (Current).",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("history is missing %q:\n%s", want, out)
		}
	}

	for _, internal := range []string{"parity_incomplete", "incomplete", "capture_pending"} {
		if strings.Contains(out, internal) {
			t.Fatalf("history must not show the internal state %q:\n%s", internal, out)
		}
	}
}

func TestHistoryShowsOnlyTheNewestAttemptsUnlessAskedForAll(t *testing.T) {
	t.Parallel()

	repoPath := filepath.Join(t.TempDir(), "repository")
	for range 3 {
		snapfixture.CommitOneSession(t, repoPath)
	}

	limited := renderHistoryForTest(t, repoPath, "--limit", "2")
	if !strings.Contains(limited, "Showing the newest 2 of 3 save attempts; --all shows every one.") {
		t.Fatalf("a limited history must say how much it hides:\n%s", limited)
	}

	if all := renderHistoryForTest(t, repoPath, "--all"); strings.Contains(all, "Showing the newest") {
		t.Fatalf("--all must show every attempt:\n%s", all)
	}
}
