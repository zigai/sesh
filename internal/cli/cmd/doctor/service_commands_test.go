package doctor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	cmddoctor "github.com/zigai/sesh/internal/cli/cmd/doctor"
	"github.com/zigai/sesh/internal/doctor"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

// isolatedServiceHome points the systemd user unit directory doctor inspects at a temporary
// directory and returns it.
func isolatedServiceHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("TMUX", "")
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_STATE_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)

	return filepath.Join(home, "systemd", "user")
}

func serviceCommandsCheck(t *testing.T) doctor.Result {
	t.Helper()

	root := t.TempDir()

	exec := clitest.ExecuteJSON(t, "doctor",
		"--socket", filepath.Join(root, "absent.sock"), "--repository", filepath.Join(root, "snapshots"),
		"--no-config", "--json")
	if !exec.Envelope.OK && exec.Envelope.Result == nil {
		t.Fatalf("doctor produced no report: %v\nstderr: %s", exec.Err, exec.Stderr)
	}

	report := clitest.DecodePayload[cmddoctor.Report](t, exec.Envelope.Result)
	for _, check := range report.Checks {
		if check.ID == doctor.CheckServiceUnits {
			return check
		}
	}

	t.Fatalf("doctor report has no %s check: %+v", doctor.CheckServiceUnits, report.Checks)

	return doctor.Result{}
}

func TestDoctorAcceptsEveryServiceUnitSetupRenders(t *testing.T) {
	dir := isolatedServiceHome(t)

	setup := clitest.Execute(t, "setup", "services", "--apply", "--dir", dir,
		"--with-coordinator", "--with-tmux-service", "--tmux", "/usr/bin/tmux",
		"--socket", filepath.Join(t.TempDir(), "managed.sock"), "--no-config")
	if setup.Err != nil {
		t.Fatalf("setup services failed: %v\nstderr: %s", setup.Err, setup.Stderr)
	}

	check := serviceCommandsCheck(t)
	if check.Status != doctor.StatusOK {
		t.Fatalf("a freshly rendered unit was rejected: %+v", check)
	}
}

func TestDoctorReportsUnitsRunningRemovedCommandsAndFlags(t *testing.T) {
	dir := isolatedServiceHome(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	units := map[string]string{
		"old-coordinator.service": "[Service]\nExecStart=/opt/bin/sesh daemon run --socket /tmp/s\n",
		"tmux.service": "[Service]\nExecStart=/usr/bin/tmux -D\n" +
			"ExecStop=-/opt/bin/sesh autosave final-save --socket \"/tmp/a b\" \\\n  --deadline 20s --no-such-flag\n",
		"unrelated.service": "[Service]\nExecStart=/usr/bin/true --socket x\n",
	}

	for name, content := range units {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	check := serviceCommandsCheck(t)
	if check.Status != doctor.StatusFail || check.Reason != doctor.ReasonStale || !strings.HasPrefix(check.Message, "2 units ") {
		t.Fatalf("stale units were not reported: %+v", check)
	}

	details := strings.Join(check.Details, "\n")
	for _, want := range []string{
		`old-coordinator.service: unknown command "run" for "sesh daemon"`, "old-coordinator.service:2)",
		"tmux.service: unknown flag: --no-such-flag", "tmux.service:3)",
	} {
		if !strings.Contains(details, want) {
			t.Fatalf("details %q do not mention %q", details, want)
		}
	}

	if strings.Contains(details, "unrelated.service") {
		t.Fatalf("a unit that does not run sesh was reported: %q", details)
	}

	if check.Fix == nil || !strings.Contains(check.Fix.Manual, "sesh setup services --apply") {
		t.Fatalf("fix does not say how to regenerate units: %+v", check.Fix)
	}
}

func TestDoctorNotesWhenNoUnitRunsSesh(t *testing.T) {
	isolatedServiceHome(t)

	if check := serviceCommandsCheck(t); check.Status != doctor.StatusInfo {
		t.Fatalf("no unit runs sesh, so the check is only a note: %+v", check)
	}
}
