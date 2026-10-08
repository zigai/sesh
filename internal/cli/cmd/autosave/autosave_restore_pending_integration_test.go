//go:build (linux || darwin) && integration

package autosave_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/testsupport/clitest"
	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

type pendingRestoreFixture struct {
	server     *gotmux.Server
	socket     string
	repoPath   string
	history    string
	leaseRoot  string
	statusPath string
}

// newPendingRestoreFixture starts a disposable tmux server after the repository already holds a
// saved "work" session, the state right after a reboot and before anything is restored.
func newPendingRestoreFixture(t *testing.T) pendingRestoreFixture {
	t.Helper()

	isolatedAutosaveHome(t)

	root := t.TempDir()
	repoPath := filepath.Join(root, "snapshots")
	snapfixture.CommitOneSession(t, repoPath)

	server := tmuxtest.NewServer(t)

	return pendingRestoreFixture{
		server:     server,
		socket:     server.Endpoint().SocketPath,
		repoPath:   repoPath,
		history:    filepath.Join(root, "state", "history.json"),
		leaseRoot:  filepath.Join(root, "leases"),
		statusPath: filepath.Join(root, "state", "autosave-status.json"),
	}
}

func (f pendingRestoreFixture) autosave(t *testing.T) autosaveResultJSON {
	t.Helper()

	exec := executeJSON(t, "autosave", "save-if-due",
		testSocketFlag, f.socket, "--repository", f.repoPath, "--history", f.history, "--lease-root", f.leaseRoot,
		testNoConfigFlag, testJSONFlag)
	if exec.Err != nil || !exec.Envelope.OK {
		t.Fatalf("save-if-due failed: %v\nstderr: %s", exec.Err, exec.Stderr)
	}

	return clitest.DecodePayload[autosaveResultJSON](t, exec.Envelope.Result)
}

func (f pendingRestoreFixture) generations(t *testing.T) int {
	t.Helper()

	repo, err := store.Open(t.Context(), f.repoPath)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()

	catalog, err := repo.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return len(catalog.Generations)
}

func TestAutosaveHoldsBackSavesUntilSavedSessionsAreRestored(t *testing.T) {
	fixture := newPendingRestoreFixture(t)

	for range 2 {
		held := fixture.autosave(t)
		if held.Status != "skipped" || held.Reason != "restore_pending" || held.Restorable != 1 || held.Recorded {
			t.Fatalf("autosave on the unrestored server = %#v, want a restore_pending skip naming 1 session", held)
		}
	}

	if got := fixture.generations(t); got != 1 {
		t.Fatalf("generations = %d, want the saved one untouched", got)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if _, err := fixture.server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "work",
		Program: gotmux.Exec("/bin/sh"),
		Start:   gotmux.StartPolicyExistingOnly,
	}); err != nil {
		t.Fatalf("recreate the saved session: %v", err)
	}

	resumed := fixture.autosave(t)
	if resumed.Status != "saved" || resumed.Generation == "" {
		t.Fatalf("autosave after the saved session is live = %#v, want a saved generation", resumed)
	}
}

func TestExplicitSaveKeepsTheLiveStateAndResumesAutosave(t *testing.T) {
	fixture := newPendingRestoreFixture(t)

	live, err := fixture.server.Sessions(t.Context())
	if err != nil || len(live) != 1 {
		t.Fatalf("list the scratch session: %v, %d sessions", err, len(live))
	}

	explicit := executeJSON(t, "snapshot", "save", string(live[0].ID),
		testSocketFlag, fixture.socket, "--repository", fixture.repoPath, "--lease-root", fixture.leaseRoot,
		testNoConfigFlag, testJSONFlag)
	if explicit.Err != nil || !explicit.Envelope.OK {
		t.Fatalf("explicit save failed: %v\nstderr: %s", explicit.Err, explicit.Stderr)
	}

	resumed := fixture.autosave(t)
	if resumed.Status != "saved" || resumed.Reason == "restore_pending" {
		t.Fatalf("autosave after an explicit save = %#v, want it to save the live state", resumed)
	}
}

func TestFinalSaveDoesNotReplaceUnrestoredSessions(t *testing.T) {
	fixture := newPendingRestoreFixture(t)

	exec := executeJSON(t, "autosave", "final-save",
		testSocketFlag, fixture.socket, "--repository", fixture.repoPath, "--history", fixture.history,
		"--lease-root", fixture.leaseRoot, "--status-file", fixture.statusPath, "--deadline", "20s",
		testNoConfigFlag, testJSONFlag)
	if exec.Err != nil || !exec.Envelope.OK {
		t.Fatalf("final save on an unrestored server must not fail the stop: %v\nstderr: %s", exec.Err, exec.Stderr)
	}

	result := clitest.DecodePayload[autosaveResultJSON](t, exec.Envelope.Result)
	if result.Status != "skipped" || result.Reason != "restore_pending" || result.Restorable != 1 {
		t.Fatalf("final save = %#v, want a restore_pending skip", result)
	}

	if got := fixture.generations(t); got != 1 {
		t.Fatalf("generations = %d, want the saved one untouched", got)
	}
}
