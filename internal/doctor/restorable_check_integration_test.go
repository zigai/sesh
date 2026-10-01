//go:build (linux || darwin) && integration

package doctor_test

import (
	"path/filepath"
	"strings"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/doctor"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func absentTmuxServer(t *testing.T) *gotmux.Server {
	t.Helper()

	server, err := gotmux.New(gotmux.Config{SocketPath: filepath.Join(t.TempDir(), "absent.sock")})
	if err != nil {
		t.Fatalf("failed to create gotmux server: %v", err)
	}

	return server
}

func savedRepository(t *testing.T) string {
	t.Helper()

	repoDir := filepath.Join(t.TempDir(), "saved")

	repo, err := store.Open(t.Context(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	commitObservationalTestFixture(t, repo)

	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	return repoDir
}

func TestRestorableSnapshotIsReportedWhenNoServerRunsButSessionsAreSaved(t *testing.T) {
	results, err := doctor.Run(t.Context(), doctor.Options{Server: absentTmuxServer(t), SnapshotDir: savedRepository(t)})
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}

	result := findCheck(results, doctor.CheckRestorableSnapshot)
	if result.Status != doctor.StatusWarning || result.Reason != doctor.ReasonNotRunning {
		t.Fatalf("expected a not-running warning, got %#v", result)
	}

	if !strings.Contains(result.Message, "sessions") || !strings.Contains(result.Fix, "sesh snapshot restore") {
		t.Fatalf("the result must say sessions are waiting and how to restore them: %#v", result)
	}
}

func TestRestorableSnapshotIsSilentWhileTmuxRunsOrNothingIsSaved(t *testing.T) {
	live, err := doctor.Run(t.Context(), doctor.Options{Server: tmuxtest.NewServer(t), SnapshotDir: savedRepository(t)})
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}

	if got := findCheck(live, doctor.CheckRestorableSnapshot); got.Check != "" {
		t.Fatalf("a running server has nothing to restore, got %#v", got)
	}

	empty, err := doctor.Run(t.Context(), doctor.Options{Server: absentTmuxServer(t), SnapshotDir: filepath.Join(t.TempDir(), "none")})
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}

	if got := findCheck(empty, doctor.CheckRestorableSnapshot); got.Check != "" {
		t.Fatalf("no repository means nothing to restore, got %#v", got)
	}
}
