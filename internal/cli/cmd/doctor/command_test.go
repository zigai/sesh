package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
	checks "github.com/zigai/sesh/internal/doctor"
)

func TestNewCommand(t *testing.T) {
	cmd := NewCommand(Build{})
	if cmd == nil || cmd.Use != "doctor" {
		t.Fatalf("unexpected doctor command: %#v", cmd)
	}
}

func TestReportsShareApplicableChecksAndCounts(test *testing.T) {
	test.Parallel()

	for _, scenario := range []string{"unsupported", "no services", "stale unit", "inspection error", "autosave enabled", "installed coordinator"} {
		test.Run(scenario, func(test *testing.T) {
			test.Parallel()

			report := newApplicableChecksReport(test, scenario)
			text, decoded := renderReportConsumers(test, report)

			byID := make(map[string]checks.Result)
			for _, result := range decoded.Checks {
				byID[result.ID] = result
			}

			assertReportApplicability(test, scenario, text, byID)
			assertReportOutcomes(test, report, decoded, text, byID)
			assertReportRepairs(test, text, byID)
		})
	}
}

func newApplicableChecksReport(tb testing.TB, scenario string) Report {
	tb.Helper()

	root := tb.TempDir()
	dir := filepath.Join(root, "units")
	opts := checks.Options{
		SnapshotDir:     filepath.Join(root, "snapshots"),
		HistoryPath:     filepath.Join(root, "history.json"),
		LogPath:         filepath.Join(root, "sesh.log"),
		Executable:      filepath.Join(root, "bin", "sesh"),
		ConfigFiles:     []checks.ConfigFile{{Path: filepath.Join(root, "config.toml"), Status: checks.ConfigSkipped}},
		Flags:           map[string]string{"--no-config": ""},
		ServiceDirs:     []string{dir},
		Daemon:          checks.Daemon{State: checks.DaemonAbsent},
		UnitActiveState: func(context.Context, string) (string, error) { return "inactive", nil },
		ValidateCommand: func([]string) error { return clibase.ErrDoctorChecksFailed },
	}

	switch scenario {
	case "unsupported":
		opts.ServiceDirs = nil
	case "stale unit", "installed coordinator":
		if err := os.Mkdir(dir, 0o700); err != nil {
			tb.Fatal(err)
		}

		name, content := "stale.service", "[Service]\nExecStart=sesh removed-command\n"
		if scenario == "installed coordinator" {
			name, content = "sesh-coordinator.service", "[Service]\n"
		}

		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			tb.Fatal(err)
		}
	case "inspection error":
		if err := os.WriteFile(dir, nil, 0o600); err != nil {
			tb.Fatal(err)
		}
	case "autosave enabled":
		opts.AutosaveEnabled = true
	}

	results, err := checks.Run(tb.Context(), opts)
	if err != nil {
		tb.Fatal(err)
	}

	return newReport(Build{Version: "test"}, opts, ReportPaths{
		Config:    opts.ConfigFiles,
		Data:      Location{Path: filepath.Join(root, "data")},
		State:     Location{Path: filepath.Join(root, "state")},
		Log:       Location{Path: opts.LogPath},
		Snapshots: Location{Path: opts.SnapshotDir},
		History:   Location{Path: opts.HistoryPath},
		Socket:    Location{Path: filepath.Join(root, "absent.sock")},
	}, results)
}

func renderReportConsumers(tb testing.TB, report Report) (string, Report) {
	tb.Helper()

	var text bytes.Buffer
	if err := writeReport(&text, report, 0, false); err != nil {
		tb.Fatal(err)
	}

	cmd := NewCommand(Build{})

	var encoded bytes.Buffer
	cmd.SetOut(&encoded)

	failure := clibase.NewExitError(clibase.ExitCodeGeneral, ErrDoctorChecksFailed)
	if err := clibase.WriteJSONResultError(cmd, report, failure); clibase.ExitCode(err) != clibase.ExitCodeGeneral {
		tb.Fatalf("required failure exit code lost: %v", err)
	}

	var payload struct {
		OK     bool                      `json:"ok"`
		Result Report                    `json:"result"`
		Error  *clibase.JSONErrorPayload `json:"error"`
	}
	if err := json.Unmarshal(encoded.Bytes(), &payload); err != nil {
		tb.Fatal(err)
	}

	tb.Logf("text consumer:\n%s\nJSON consumer:\n%s", text.String(), encoded.String())

	if payload.OK || payload.Error == nil || payload.Error.ExitCode != clibase.ExitCodeGeneral {
		tb.Fatalf("required failure envelope lost: %s", encoded.String())
	}

	if payload.Result.Summary != report.Summary || !strings.Contains(text.String(), counts(report.Summary)) {
		tb.Fatalf("counts disagree: text=%s JSON=%s", text.String(), encoded.String())
	}

	return text.String(), payload.Result
}

func applicableReportChecks(scenario string) map[string]bool {
	units := runtime.GOOS == "linux" && (scenario == "stale unit" || scenario == "inspection error")
	coordinator := runtime.GOOS == "linux" && (scenario == "autosave enabled" || scenario == "installed coordinator" || scenario == "inspection error")

	return map[string]bool{
		checks.CheckServiceUnits:     units,
		checks.CheckAutosaveDaemon:   coordinator,
		checks.CheckServiceOOMPolicy: runtime.GOOS == "linux" && scenario != "unsupported",
	}
}

func assertReportApplicability(tb testing.TB, scenario, text string, byID map[string]checks.Result) {
	tb.Helper()

	applicable := applicableReportChecks(scenario)
	for id, want := range applicable {
		result, found := byID[id]
		if found != want {
			tb.Errorf("check %s present=%t, want %t: %+v", id, found, want, result)
		}

		assertReportCheckRows(tb, text, id, want)
	}

	if (scenario == "unsupported" || runtime.GOOS != "linux") && strings.Contains(text, "\nServices\n") {
		tb.Errorf("empty services group rendered: %s", text)
	}

	if !applicable[checks.CheckAutosaveDaemon] && strings.Contains(text, "\nAutosave\n") {
		tb.Errorf("empty autosave group rendered: %s", text)
	}

	if runtime.GOOS == "linux" && scenario == "stale unit" && byID[checks.CheckServiceUnits].Status != checks.StatusFail {
		tb.Errorf("stale unit failure lost: %+v", byID)
	}
}

func assertReportCheckRows(tb testing.TB, text, id string, want bool) {
	tb.Helper()

	_, name, _ := strings.Cut(id, ".")
	textRows := 0

	for line := range strings.SplitSeq(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == name {
			textRows++
		}
	}

	if want && textRows != 1 || !want && textRows != 0 {
		tb.Errorf("check %s text rows=%d, want present=%t: %s", id, textRows, want, text)
	}
}

func assertReportOutcomes(tb testing.TB, report, decoded Report, text string, byID map[string]checks.Result) {
	tb.Helper()

	if report.Summary.Fail == 0 || byID[checks.CheckTmuxClient].Status != checks.StatusFail {
		tb.Errorf("required missing dependency failure lost: %+v", report)
	}

	if byID[checks.CheckConfigFile].Status != checks.StatusSkip || !strings.Contains(text, "skipped, --no-config") {
		tb.Errorf("explicit configuration skip lost: %s", text)
	}

	total := report.Summary.OK + report.Summary.Info + report.Summary.Warn + report.Summary.Fail + report.Summary.Skip
	if total != len(decoded.Checks) {
		tb.Fatalf("summary includes uncollected checks: %+v", report.Summary)
	}
}

func assertReportRepairs(tb testing.TB, text string, byID map[string]checks.Result) {
	tb.Helper()

	for _, id := range []string{checks.CheckServiceUnits, checks.CheckAutosaveDaemon} {
		result, found := byID[id]
		if !found {
			continue
		}

		if result.Fix == nil {
			tb.Fatalf("applicable warning/failure lost its repair: %+v", result)
		}

		if repair := result.Fix.Command + result.Fix.Manual; !strings.Contains(text, repair) {
			tb.Errorf("text lost JSON repair %q: %s", repair, text)
		}
	}
}
