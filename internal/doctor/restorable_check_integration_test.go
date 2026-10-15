//go:build (linux || darwin) && integration

package doctor_test

import (
	"os"
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

func tmuxServerCheck(t *testing.T, server *gotmux.Server, snapshotDir string) doctor.Result {
	t.Helper()

	results, err := doctor.Run(t.Context(), doctor.Options{PathEnv: os.Getenv("PATH"), Server: server, SnapshotDir: snapshotDir})
	if err != nil {
		t.Fatalf("unexpected run error: %v", err)
	}

	return findCheck(results, doctor.CheckTmuxServer)
}

func TestRestorableSessionsAreNamedWhenNoServerRuns(t *testing.T) {
	result := tmuxServerCheck(t, absentTmuxServer(t), savedRepository(t))
	if result.Status != doctor.StatusFail || result.Reason != doctor.ReasonNotRunning || result.Fix == nil {
		t.Fatalf("expected a not-running failure with a fix, got %#v", result)
	}

	if len(result.Details) != 1 || !strings.Contains(result.Details[0], "holds 1 session;") ||
		!strings.Contains(result.Details[0], "'sesh snapshot restore'") {
		t.Fatalf("the result must say sessions are waiting and how to restore them: %#v", result)
	}
}

func TestRestorableSessionsAreNotMentionedWhileTmuxRunsOrNothingIsSaved(t *testing.T) {
	if live := tmuxServerCheck(t, tmuxtest.NewServer(t), savedRepository(t)); live.Status != doctor.StatusOK || len(live.Details) != 0 {
		t.Fatalf("a running server has nothing to restore, got %#v", live)
	}

	if empty := tmuxServerCheck(t, absentTmuxServer(t), filepath.Join(t.TempDir(), "none")); len(empty.Details) != 0 {
		t.Fatalf("no repository means nothing to restore, got %#v", empty)
	}
}
