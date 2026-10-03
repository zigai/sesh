//go:build linux

package doctor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/doctor"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func activeRestoresResult(t *testing.T, repoPath string) (doctor.Result, bool) {
	t.Helper()

	results, err := doctor.Run(t.Context(), doctor.Options{SnapshotDir: repoPath})
	if err != nil {
		t.Fatalf("doctor.Run: %v", err)
	}

	for _, result := range results {
		if result.Check == doctor.CheckActiveRestores {
			return result, true
		}
	}

	return doctor.Result{}, false
}

func TestDoctorNamesARestoreThatHoldsBackSavesAndHowToClearIt(t *testing.T) {
	t.Parallel()

	repoPath := filepath.Join(t.TempDir(), "repository")
	snapfixture.CommitOneSession(t, repoPath)

	self, err := process.ReadIdentity(t.Context(), os.Getpid())
	if err != nil {
		t.Fatalf("read own identity: %v", err)
	}

	live := snapfixture.RegisterRestore(t, repoPath, os.Getpid(), self.String())

	result, ok := activeRestoresResult(t, repoPath)
	if !ok || result.Status != doctor.StatusError || result.Reason != doctor.ReasonRecoveryRequired {
		t.Fatalf("active-restores = %+v (present %t), want an error", result, ok)
	}

	if !strings.Contains(result.Message, live) || !strings.Contains(result.Fix, "sesh snapshot recover restore abandon "+live) {
		t.Fatalf("result must name the restore and the abandon command: %+v", result)
	}
}

func TestDoctorNotesRestoresWhoseServerIsGoneAsSelfClearing(t *testing.T) {
	t.Parallel()

	repoPath := filepath.Join(t.TempDir(), "repository")
	snapfixture.CommitOneSession(t, repoPath)
	dead := snapfixture.RegisterRestore(t, repoPath, os.Getpid(), "00000000-1111-2222-3333-444444444444:1")

	result, ok := activeRestoresResult(t, repoPath)
	if !ok || result.Status != doctor.StatusWarning || result.Reason != doctor.ReasonStale ||
		!strings.Contains(result.Message, "abandon them automatically") || !strings.Contains(result.Fix, dead) {
		t.Fatalf("active-restores = %+v (present %t), want a self-clearing warning", result, ok)
	}
}

func TestDoctorOmitsActiveRestoresWhenNoneAreActive(t *testing.T) {
	t.Parallel()

	repoPath := filepath.Join(t.TempDir(), "repository")
	snapfixture.CommitOneSession(t, repoPath)

	if result, ok := activeRestoresResult(t, repoPath); ok {
		t.Fatalf("no active restore must add no result, got %+v", result)
	}
}
