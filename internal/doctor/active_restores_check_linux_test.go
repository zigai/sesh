//go:build linux

package doctor_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/doctor"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/testsupport/snapfixture"
)

func activeRestoresResult(t *testing.T, repoPath string) doctor.Result {
	t.Helper()

	results, err := doctor.Run(t.Context(), doctor.Options{SnapshotDir: repoPath, Flags: map[string]string{"--repository": repoPath}})
	if err != nil {
		t.Fatalf("doctor.Run: %v", err)
	}

	return findCheck(results, doctor.CheckActiveRestores)
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

	result := activeRestoresResult(t, repoPath)
	if result.Status != doctor.StatusFail || result.Reason != doctor.ReasonRecoveryRequired {
		t.Fatalf("active restores = %+v, want a failure", result)
	}

	want := "sesh snapshot recover restore abandon --repository " + repoPath + " " + live
	if !slices.Equal(result.Details, []string{live}) || result.Fix == nil || result.Fix.Command != want {
		t.Fatalf("result must name the restore and the abandon command %q: %+v", want, result)
	}
}

func TestDoctorNotesRestoresWhoseServerIsGoneAsSelfClearing(t *testing.T) {
	t.Parallel()

	repoPath := filepath.Join(t.TempDir(), "repository")
	snapfixture.CommitOneSession(t, repoPath)
	dead := snapfixture.RegisterRestore(t, repoPath, os.Getpid(), "00000000-1111-2222-3333-444444444444:1")

	result := activeRestoresResult(t, repoPath)
	if result.Status != doctor.StatusInfo || result.Reason != doctor.ReasonStale ||
		!strings.Contains(result.Message, "abandons these") || !slices.Equal(result.Details, []string{dead}) {
		t.Fatalf("active restores = %+v, want a self-clearing note", result)
	}
}

func TestDoctorPassesActiveRestoresWhenNoneAreActive(t *testing.T) {
	t.Parallel()

	repoPath := filepath.Join(t.TempDir(), "repository")
	snapfixture.CommitOneSession(t, repoPath)

	if result := activeRestoresResult(t, repoPath); result.Status != doctor.StatusOK {
		t.Fatalf("no active restore must pass, got %+v", result)
	}
}
