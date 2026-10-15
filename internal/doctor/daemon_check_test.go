//go:build linux

package doctor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/doctor"
)

func TestDaemonCheckRequiresRunningPhaseAndHeldInhibitors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, state, phase string
		held               bool
		wantStatus         doctor.Status
		wantFix            string
	}{
		{"ready", doctor.DaemonReady, "running", true, doctor.StatusOK, ""},
		{"released", doctor.DaemonNotReady, "running", false, doctor.StatusWarn, "systemctl --user status sesh-coordinator.service"},
		{"initializing", doctor.DaemonNotReady, "initializing", true, doctor.StatusInfo, ""},
		{"contradictory_ready_phase", doctor.DaemonReady, "initializing", true, doctor.StatusInfo, ""},
		{"contradictory_ready_locks", doctor.DaemonReady, "running", false, doctor.StatusWarn, "systemctl --user status sesh-coordinator.service"},
		{"final_save", doctor.DaemonNotReady, "final_save", true, doctor.StatusInfo, ""},
		{"stale", doctor.DaemonStale, "", false, doctor.StatusWarn, "rm /run/user/1000/sesh/daemon-1.sock"},
		{"unverifiable", doctor.DaemonUnverifiable, "", false, doctor.StatusWarn, "systemctl --user status sesh-coordinator.service"},
		{"absent_autosave_off", doctor.DaemonAbsent, "", false, doctor.StatusInfo, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			check := daemonCheck(t, doctor.Options{Daemon: doctor.Daemon{
				State: test.state, Phase: test.phase, InhibitorsHeld: test.held, RuntimePath: "/run/user/1000/sesh/daemon-1.sock",
			}})

			fix := ""
			if check.Fix != nil {
				fix = check.Fix.Command
			}

			if check.Status != test.wantStatus || fix != test.wantFix {
				t.Fatalf("daemon check = %+v, want %s with fix %q", check, test.wantStatus, test.wantFix)
			}
		})
	}
}

func TestAbsentDaemonFixDependsOnWhatIsInstalled(t *testing.T) {
	t.Parallel()

	unitDir := t.TempDir()
	absent := doctor.Daemon{State: doctor.DaemonAbsent}

	notInstalled := daemonCheck(t, doctor.Options{Daemon: absent, AutosaveEnabled: true, ServiceDirs: []string{unitDir}})
	if notInstalled.Status != doctor.StatusWarn || notInstalled.Fix == nil ||
		!strings.Contains(notInstalled.Fix.Command, "sesh setup services --apply --with-coordinator") {
		t.Fatalf("autosave without a coordinator must say how to install it: %+v", notInstalled)
	}

	if err := os.WriteFile(filepath.Join(unitDir, "sesh-coordinator.service"), []byte("[Service]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stopped := daemonCheck(t, doctor.Options{Daemon: absent, ServiceDirs: []string{unitDir}})
	if stopped.Status != doctor.StatusWarn || stopped.Fix == nil ||
		stopped.Fix.Command != "systemctl --user enable --now sesh-coordinator.service" {
		t.Fatalf("an installed coordinator that does not run must be started: %+v", stopped)
	}
}

func daemonCheck(t *testing.T, opts doctor.Options) doctor.Result {
	t.Helper()

	results, err := doctor.Run(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}

	return findCheck(results, doctor.CheckAutosaveDaemon)
}
