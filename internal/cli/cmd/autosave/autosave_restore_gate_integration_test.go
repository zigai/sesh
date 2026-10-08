//go:build linux && integration

package autosave_test

import (
	"os"
	"path/filepath"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/testsupport/clitest"
	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func TestStartupRestoreAbandonsARestoreFromAnEarlierBootAndRestores(t *testing.T) {
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

	snapfixture.RegisterRestore(t, repoPath, os.Getpid(), "00000000-1111-2222-3333-444444444444:1")

	server := tmuxtest.NewServer(t)

	exec := executeJSON(t, "autosave", "restore-on-start",
		testSocketFlag, server.Endpoint().SocketPath, "--repository", repoPath,
		"--lease-root", leaseRoot, "--history", filepath.Join(root, "state", "history.json"),
		testNoConfigFlag, testJSONFlag)
	if exec.Err != nil || !exec.Envelope.OK {
		t.Fatalf("startup restore failed: %v\nstderr: %s", exec.Err, exec.Stderr)
	}

	result := clitest.DecodePayload[autosaveResultJSON](t, exec.Envelope.Result)
	if result.Abandoned != 1 || result.Generation == "" || result.Reason == "pending_operation" {
		t.Fatalf("startup restore = %#v, want the stale restore abandoned and the snapshot restored", result)
	}
}
