//go:build linux

package doctor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/doctor"
)

var errBusUnavailable = errors.New("bus unavailable")

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

	cases := []struct {
		active     string
		wantStatus doctor.Status
		wantFix    string
	}{
		{"inactive", doctor.StatusWarn, "systemctl --user enable --now sesh-coordinator.service"},
		{"failed", doctor.StatusWarn, "systemctl --user enable --now sesh-coordinator.service"},
		{"active", doctor.StatusInfo, ""},
		{"activating", doctor.StatusInfo, ""},
		{"deactivating", doctor.StatusInfo, ""},
	}
	for _, test := range cases {
		check := daemonCheck(t, doctor.Options{
			Daemon: absent, ServiceDirs: []string{unitDir},
			UnitActiveState: func(context.Context, string) (string, error) { return test.active, nil },
		})

		fix := ""
		if check.Fix != nil {
			fix = check.Fix.Command
		}

		if check.Status != test.wantStatus || fix != test.wantFix {
			t.Fatalf("installed coordinator %s: daemon check = %+v, want %s with fix %q", test.active, check, test.wantStatus, test.wantFix)
		}
	}
}

func TestInstalledDaemonWithUnreadableUnitStateWarns(t *testing.T) {
	t.Parallel()

	unitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unitDir, "sesh-coordinator.service"), []byte("[Service]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	check := daemonCheck(t, doctor.Options{
		Daemon: doctor.Daemon{State: doctor.DaemonAbsent}, ServiceDirs: []string{unitDir},
		UnitActiveState: func(context.Context, string) (string, error) { return "", errBusUnavailable },
	})
	if check.Status != doctor.StatusWarn || check.Reason != doctor.ReasonInspectionFailed || check.Fix == nil ||
		check.Fix.Command != "systemctl --user status sesh-coordinator.service" {
		t.Fatalf("unreadable unit state must not read as stopped: %+v", check)
	}
}

func TestAbsentDaemonRetainsUnitInspectionErrors(test *testing.T) {
	test.Parallel()

	dir := test.TempDir()
	unit := filepath.Join(dir, "sesh-coordinator.service")
	if err := os.Symlink(filepath.Base(unit), unit); err != nil {
		test.Fatal(err)
	}

	result := daemonCheck(test, doctor.Options{Daemon: doctor.Daemon{State: doctor.DaemonAbsent}, ServiceDirs: []string{dir}})
	if result.Status != doctor.StatusWarn || result.Reason != doctor.ReasonInspectionFailed || result.Fix == nil {
		test.Fatalf("unit inspection error was treated as absence: %+v", result)
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
