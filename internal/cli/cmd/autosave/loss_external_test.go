package autosave_test

import (
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/testsupport/clitest"
	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func TestAutosaveSkipReportsSavedSessionsOnceWhenNoServerIsRunning(t *testing.T) {
	isolatedAutosaveHome(t)

	root := t.TempDir()
	repoPath := filepath.Join(root, "snapshots")
	historyPath := filepath.Join(root, "state", "history.json")

	snapfixture.CommitOneSession(t, repoPath)

	if _, err := session.UpdateState(t.Context(), historyPath, func(state *session.State) { state.LastAutosaveUnix = 1 }); err != nil {
		t.Fatal(err)
	}

	run := func() resultJSON {
		exec := executeJSON(t, "autosave", "save-if-due",
			testSocketFlag, filepath.Join(root, "absent.sock"),
			"--repository", repoPath, "--history", historyPath,
			testNoConfigFlag, testJSONFlag)
		if exec.Err != nil || !exec.Envelope.OK {
			t.Fatalf("save-if-due without a server failed: %v\nstderr: %s", exec.Err, exec.Stderr)
		}

		return clitest.DecodePayload[resultJSON](t, exec.Envelope.Result)
	}

	first := run()
	if first.Status != "skipped" || first.Reason != "tmux_not_running" || first.Restorable != 1 {
		t.Fatalf("first run = %#v, want a skip that reports the one saved session", first)
	}

	second := run()
	if second.Reason != "tmux_not_running" || second.Restorable != 0 {
		t.Fatalf("second run = %#v, want the same loss reported only once", second)
	}
}

func TestFinalSaveAfterTheServerDiedRecordsServerGoneNotAGenericFailure(t *testing.T) {
	isolatedAutosaveHome(t)

	root := t.TempDir()
	repoPath := filepath.Join(root, "snapshots")
	statusPath := filepath.Join(root, "state", "autosave-status.json")

	snapfixture.CommitOneSession(t, repoPath)

	exec := executeJSON(t, "autosave", "final-save",
		testSocketFlag, filepath.Join(root, "absent.sock"),
		"--repository", repoPath, "--history", filepath.Join(root, "state", "history.json"),
		"--status-file", statusPath, "--deadline", "5s",
		testNoConfigFlag, testJSONFlag)
	if exec.Err == nil {
		t.Fatal("a final save with nothing to observe must not report success")
	}

	status, err := automation.ReadFinalSaveStatus(statusPath)
	if err != nil {
		t.Fatalf("the final save must still record why it was incomplete: %v", err)
	}

	if status.Result != automation.FinalSaveIncomplete || status.Reason != "server_gone" || !status.Incomplete {
		t.Fatalf("status = %+v, want incomplete/server_gone", status)
	}
}
