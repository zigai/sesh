//go:build integration

package autosave_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/automation"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

func TestAutosaveSaveIfDueSkipsWhenDefaultTmuxServerIsNotRunning(t *testing.T) {
	isolatedAutosaveHome(t)

	exec := executeJSON(t, "autosave", "save-if-due", testNoConfigFlag, testJSONFlag)
	if exec.Err != nil || !exec.Envelope.OK {
		t.Fatalf(
			"save-if-due without a running tmux server failed: %v\nstderr: %s",
			exec.Err,
			exec.Stderr,
		)
	}

	result := clitest.DecodePayload[resultJSON](t, exec.Envelope.Result)
	if result.Status != "skipped" || result.Reason != "tmux_not_running" || result.Recorded {
		t.Fatalf("result = %#v, want an unrecorded skip because tmux is not running", result)
	}

	if result.Attempt != "" || result.Generation != "" {
		t.Fatalf("result = %#v, want no save attempt without a tmux server", result)
	}
}

func TestAutosaveFinalSaveWaitsForASaveInProgress(t *testing.T) {
	tests := []struct {
		name       string
		holdFor    time.Duration
		deadline   string
		wantReason string
	}{
		{"lock released before the deadline", 100 * time.Millisecond, "5s", "server_gone"},
		{"lock held past the deadline", time.Minute, "1s", "lock_busy"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := isolatedAutosaveHome(t)
			statusPath := filepath.Join(home, "final-save-status.json")

			lock, acquired, err := automation.AcquireSaveLock(
				t.Context(),
				automation.SaveLockPath(),
				automation.SaveLockNonBlocking,
			)
			if err != nil || !acquired {
				t.Fatalf("hold save lock = (%v, %v)", acquired, err)
			}

			release := time.AfterFunc(tc.holdFor, func() { _ = lock.Release() })

			t.Cleanup(func() {
				release.Stop()

				_ = lock.Release()
			})

			executeJSON(
				t, "autosave", "final-save",
				testNoConfigFlag, testJSONFlag,
				testSocketFlag, filepath.Join(home, "absent.sock"),
				"--status-file", statusPath,
				"--deadline", tc.deadline,
			)

			status, err := automation.ReadFinalSaveStatus(statusPath)
			if err != nil {
				t.Fatal(err)
			}

			if status.Reason != tc.wantReason || !status.Incomplete {
				t.Fatalf(
					"final save status = %+v, want incomplete with reason %q",
					status,
					tc.wantReason,
				)
			}
		})
	}
}
