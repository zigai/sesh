//go:build (linux || darwin) && integration

package autosave_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/automation"
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
	config     []string
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
		config:     []string{testNoConfigFlag},
	}
}

// newSavedSessionFixture saves a real "work" session from one tmux server and then starts another,
// a reboot whose startup restore never ran.
func newSavedSessionFixture(t *testing.T) pendingRestoreFixture {
	t.Helper()

	isolatedAutosaveHome(t)

	root := t.TempDir()
	repoPath := filepath.Join(root, "snapshots")
	leaseRoot := filepath.Join(root, "leases")

	source := tmuxtest.NewServer(t)

	work, err := source.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: "work", Program: gotmux.Exec("/bin/sh"), Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create the session to save: %v", err)
	}

	saved := executeJSON(t, "snapshot", "save", string(work.ID()),
		testSocketFlag, source.Endpoint().SocketPath, "--repository", repoPath, "--lease-root", leaseRoot,
		testNoConfigFlag, testJSONFlag)
	if saved.Err != nil || !saved.Envelope.OK {
		t.Fatalf("save the source session: %v\nstderr: %s", saved.Err, saved.Stderr)
	}

	// tmux reports its start time in whole seconds, so the new server must start in a later second
	// than the save to count as started after it.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))

	server := tmuxtest.NewServer(t)

	return pendingRestoreFixture{
		server:     server,
		socket:     server.Endpoint().SocketPath,
		repoPath:   repoPath,
		history:    filepath.Join(root, "state", "history.json"),
		leaseRoot:  leaseRoot,
		statusPath: filepath.Join(root, "state", "autosave-status.json"),
		config:     []string{testNoConfigFlag},
	}
}

func (f pendingRestoreFixture) withoutStartupRestore(t *testing.T) pendingRestoreFixture {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[autosave]\nrestore_last_on_start = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f.config = []string{testConfigFlag, path}

	return f
}

func (f pendingRestoreFixture) workSessionLive(t *testing.T) bool {
	t.Helper()

	_, err := f.server.FindSession(t.Context(), "work")
	if err != nil && !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatal(err)
	}

	return err == nil
}

func (f pendingRestoreFixture) killWorkSession(t *testing.T) {
	t.Helper()

	work, err := f.server.FindSession(t.Context(), "work")
	if err != nil {
		t.Fatal(err)
	}

	if err := work.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (f pendingRestoreFixture) autosave(t *testing.T) resultJSON {
	t.Helper()

	exec := executeJSON(t, append([]string{
		"autosave", "save-if-due",
		testSocketFlag, f.socket, "--repository", f.repoPath, "--history", f.history, "--lease-root", f.leaseRoot,
		testJSONFlag,
	}, f.config...)...)
	if exec.Err != nil || !exec.Envelope.OK {
		t.Fatalf("save-if-due failed: %v\nstderr: %s", exec.Err, exec.Stderr)
	}

	return clitest.DecodePayload[resultJSON](t, exec.Envelope.Result)
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
	fixture := newPendingRestoreFixture(t).withoutStartupRestore(t)

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

	result := clitest.DecodePayload[resultJSON](t, exec.Envelope.Result)
	if result.Status != "skipped" || result.Reason != "restore_pending" || result.Restorable != 1 {
		t.Fatalf("final save = %#v, want a restore_pending skip", result)
	}

	if got := fixture.generations(t); got != 1 {
		t.Fatalf("generations = %d, want the saved one untouched", got)
	}
}

func TestAutosaveRestoresAServerThatStartedWithoutTheStartupRestore(t *testing.T) {
	fixture := newSavedSessionFixture(t)

	fixture.autosave(t)

	if !fixture.workSessionLive(t) {
		t.Fatal("autosave on a server that missed its startup restore did not restore the saved session")
	}

	fixture.killWorkSession(t)

	resumed := fixture.autosave(t)
	if fixture.workSessionLive(t) {
		t.Fatal("the next autosave restored the closed session again")
	}

	if resumed.Status != "saved" {
		t.Fatalf("autosave after the late restore = %#v, want it to save the live state", resumed)
	}
}

func TestAutosaveRetriesALateRestoreThatFoundTheSaveLockBusy(t *testing.T) {
	fixture := newSavedSessionFixture(t)

	holder, acquired, err := automation.AcquireSaveLock(t.Context(), automation.SaveLockPath(), automation.SaveLockNonBlocking)
	if err != nil || !acquired {
		t.Fatalf("hold the save lock: acquired=%v err=%v", acquired, err)
	}

	busy := fixture.autosave(t)

	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}

	if busy.Reason != "lock_busy" || fixture.workSessionLive(t) {
		t.Fatalf("autosave while another save holds the lock = %#v, want a lock_busy skip without restoring", busy)
	}

	fixture.autosave(t)

	if !fixture.workSessionLive(t) {
		t.Fatal("the autosave after the lock was released did not run the late restore")
	}
}

func TestAutosaveDoesNotRestoreAServerItsStartupRestoreAlreadyHandled(t *testing.T) {
	fixture := newSavedSessionFixture(t)

	startup := executeJSON(t, "autosave", "restore-on-start",
		testSocketFlag, fixture.socket, "--repository", fixture.repoPath, "--lease-root", fixture.leaseRoot,
		"--history", fixture.history, testNoConfigFlag, testJSONFlag)
	if startup.Err != nil || !startup.Envelope.OK {
		t.Fatalf("startup restore failed: %v\nstderr: %s", startup.Err, startup.Stderr)
	}

	fixture.killWorkSession(t)

	resumed := fixture.autosave(t)

	if fixture.workSessionLive(t) {
		t.Fatal("autosave restored a session closed after the startup restore")
	}

	if resumed.Status != "saved" {
		t.Fatalf("autosave after the startup restore = %#v, want it to save the live state", resumed)
	}
}
