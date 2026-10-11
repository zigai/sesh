//go:build integration && linux

package snapshot_test

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestProcessObservationExactLaunchFacts(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)

	dir := filepath.Join(t.TempDir(), "cwd with spaces and \t tabs and \n newlines")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	reportPath := filepath.Join(t.TempDir(), "launch.report")
	testArgs := []string{
		"-report", reportPath,
		"-hold", "300s",
		"normal_arg",
		"",
		"arg with spaces and \t tabs",
		"arg\nwith\nnewlines",
		"arg=with=equals",
		"arg\x01\x02arbitrary\xfe",
	}

	sessionEnv := map[string]string{
		"SELECTED_VAR":           "observed_value",
		"EMPTY_VAR":              "",
		"UNSELECTED_PRIVATE_KEY": "unselected_private_val_12345",
		"TMUX":                   "/tmp/ambient.sock,1,0",
		"TMUX_PANE":              "%99",
	}

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    "proc-facts",
		Dir:     dir,
		Program: gotmux.Exec(helper, testArgs...),
		TmuxEnv: sessionEnv,
		Size:    gotmux.Size{Width: 100, Height: 30},
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	report := awaitProcessReport(t, reportPath)

	envKeys := []string{"SELECTED_VAR", "EMPTY_VAR", "ABSENT_VAR", "TMUX", "TMUX_PANE"}

	observation, err := capture.ObserveWithProcessEnrichment(t.Context(), server, []gotmux.SessionID{session.ID()}, envKeys)
	if err != nil {
		t.Fatalf("observe with process enrichment failed: %v", err)
	}

	if len(observation.Manifest.Panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(observation.Manifest.Panes))
	}

	launch := observation.Manifest.Panes[0].Launch
	assertObservedLaunchMetadata(t, launch, helper, dir, report)
	assertObservedLaunchEnvironment(t, launch.Environment)
	assertObservedEvidence(t, observation.Manifest.Evidence)
}

func assertObservedLaunchMetadata(t *testing.T, launch model.Launch, helper, dir string, report processReport) {
	t.Helper()

	if string(launch.Executable) != helper {
		t.Fatalf("executable mismatch: got %q, want %q", string(launch.Executable), helper)
	}

	if len(launch.Arguments) != len(report.Argv) {
		t.Fatalf("arguments length mismatch: got %d, want %d", len(launch.Arguments), len(report.Argv))
	}

	for i, arg := range launch.Arguments {
		if !bytes.Equal(arg, []byte(report.Argv[i])) {
			t.Fatalf("argument %d mismatch: got %q, want %q", i, arg, report.Argv[i])
		}
	}

	if string(launch.Directory) != dir {
		t.Fatalf("directory mismatch: got %q, want %q", string(launch.Directory), dir)
	}

	if len(launch.Birth) == 0 {
		t.Fatal("expected non-empty birth bytes")
	}

	parsedBirth, err := process.ParseIdentity(string(launch.Birth))
	if err != nil || parsedBirth.IsZero() {
		t.Fatalf("invalid birth format %q: %v", string(launch.Birth), err)
	}

	if launch.Recovery != "unverified" {
		t.Fatalf("expected unverified recovery, got %q", launch.Recovery)
	}
}

func assertObservedLaunchEnvironment(t *testing.T, env []model.Variable) {
	t.Helper()

	envMap := make(map[string]model.Variable)
	for _, v := range env {
		envMap[string(v.Name)] = v
	}

	assertExpectedEnvironmentVars(t, envMap)
	assertExcludedEnvironmentVars(t, envMap)
}

func assertExpectedEnvironmentVars(t *testing.T, envMap map[string]model.Variable) {
	t.Helper()

	if v, exists := envMap["SELECTED_VAR"]; !exists {
		t.Fatal("missing SELECTED_VAR")
	} else if v.State != "set" || string(v.Value) != "observed_value" {
		t.Fatalf("SELECTED_VAR mismatch: state=%q value=%q", v.State, string(v.Value))
	}

	if v, exists := envMap["EMPTY_VAR"]; !exists {
		t.Fatal("missing EMPTY_VAR")
	} else if v.State != "set" || v.Value == nil || len(v.Value) != 0 {
		t.Fatalf("EMPTY_VAR mismatch: state=%q value=%v", v.State, v.Value)
	}

	if v, exists := envMap["ABSENT_VAR"]; !exists {
		t.Fatal("missing ABSENT_VAR")
	} else if v.State != "absent" || v.Value != nil {
		t.Fatalf("ABSENT_VAR mismatch: state=%q value=%v", v.State, v.Value)
	}
}

func assertExcludedEnvironmentVars(t *testing.T, envMap map[string]model.Variable) {
	t.Helper()

	if _, exists := envMap["UNSELECTED_PRIVATE_KEY"]; exists {
		t.Fatal("unselected private variable was leaked into Launch.Environment")
	}

	if _, exists := envMap["TMUX"]; exists {
		t.Fatal("TMUX was not excluded from Launch.Environment")
	}

	if _, exists := envMap["TMUX_PANE"]; exists {
		t.Fatal("TMUX_PANE was not excluded from Launch.Environment")
	}
}

func assertObservedEvidence(t *testing.T, evidence []model.Evidence) {
	t.Helper()

	var foundS07 bool

	for _, ev := range evidence {
		if ev.Domain == "S07" {
			foundS07 = true

			if ev.Status != "observed" || ev.Reason != "observed_only" {
				t.Fatalf("unexpected S07 evidence: %#v", ev)
			}
		}
	}

	if !foundS07 {
		t.Fatal("missing S07 evidence record")
	}
}

func TestProcessObservationDirectForegroundBirth(t *testing.T) {
	server := tmuxtest.NewServer(t)
	dir := t.TempDir()

	session, pane := saveSessionFixture(t, server, "root-vs-child", dir, "SHELL_ROOT_MARKER")

	paneInfo, err := pane.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	obs, err := capture.ObserveWithProcessEnrichment(t.Context(), server, []gotmux.SessionID{session.ID()}, nil)
	if err != nil {
		t.Fatalf("observation failed: %v", err)
	}

	if len(obs.Manifest.Panes) != 1 {
		t.Fatalf("expected 1 pane, got %d", len(obs.Manifest.Panes))
	}

	rootBirth, err := process.ReadIdentity(t.Context(), paneInfo.PID)
	if err != nil {
		t.Fatal(err)
	}

	launch := obs.Manifest.Panes[0].Launch
	if string(launch.Birth) != rootBirth.String() {
		t.Fatalf("birth identity was not bound to pane root PID: got %s, want %s", string(launch.Birth), rootBirth.String())
	}
}

func TestProcessObservationPreservesPaneWithDeletedExecutable(t *testing.T) {
	server := tmuxtest.NewServer(t)
	srcHelper := buildLaunchHelper(t)

	tempDir := t.TempDir()
	disposableHelper := filepath.Join(tempDir, "disposable_helper")
	copyExecutable(t, srcHelper, disposableHelper)

	reportPath := filepath.Join(tempDir, "del_exe.report")
	testArgs := []string{
		"-report", reportPath,
		"-hold", "300s",
	}

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    "del-exe",
		Dir:     tempDir,
		Program: gotmux.Exec(disposableHelper, testArgs...),
		Size:    gotmux.Size{Width: 100, Height: 30},
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = awaitProcessReport(t, reportPath)

	if err := os.Remove(disposableHelper); err != nil {
		t.Fatal(err)
	}

	observation, err := capture.ObserveWithProcessEnrichment(t.Context(), server, []gotmux.SessionID{session.ID()}, nil)
	if err != nil {
		t.Fatalf("observe pane with deleted executable: %v", err)
	}

	assertPreservedNonrestartablePane(t, observation, false, string(model.ReasonExecutableDeleted))
}

func TestProcessObservationPreservesPaneWithDeletedDirectory(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)

	parentDir := t.TempDir()

	disposableDir := filepath.Join(parentDir, "disposable_dir")
	if err := os.Mkdir(disposableDir, 0o700); err != nil {
		t.Fatal(err)
	}

	reportPath := filepath.Join(parentDir, "del_dir.report")
	testArgs := []string{
		"-report", reportPath,
		"-hold", "300s",
	}

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    "del-dir",
		Dir:     disposableDir,
		Program: gotmux.Exec(helper, testArgs...),
		Size:    gotmux.Size{Width: 100, Height: 30},
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = awaitProcessReport(t, reportPath)

	if err := os.Remove(disposableDir); err != nil {
		t.Fatal(err)
	}

	observation, err := capture.ObserveWithProcessEnrichment(t.Context(), server, []gotmux.SessionID{session.ID()}, nil)
	if err != nil {
		t.Fatalf("observe pane with deleted directory: %v", err)
	}

	assertPreservedNonrestartablePane(t, observation, false, string(model.ReasonWorkingDirectoryDeleted))
}

func TestProcessObservationPreservesExitedAndUnaffectedPanes(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)

	dir := t.TempDir()
	reportPath := filepath.Join(dir, "churn.report")
	testArgs := []string{
		"-report", reportPath,
		"-hold", "150ms",
	}

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    "churn",
		Dir:     dir,
		Program: gotmux.Exec("sleep", "300"),
		Size:    gotmux.Size{Width: 100, Height: 30},
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := server.GlobalWindowOptions().SetRemainOnExit(t.Context(), gotmux.RemainOn); err != nil {
		t.Fatal(err)
	}

	ephemeralWin, err := session.NewWindow(t.Context(), gotmux.NewWindowOptions{
		Name:    "ephemeral",
		Dir:     dir,
		Program: gotmux.Exec(helper, testArgs...),
	})
	if err != nil {
		t.Fatal(err)
	}

	pane, err := ephemeralWin.Window().ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	_ = awaitProcessReport(t, reportPath)

	_ = awaitPaneDead(t, ephemeralWin.Window(), pane.ID())

	observation, err := capture.ObserveWithProcessEnrichment(t.Context(), server, []gotmux.SessionID{session.ID()}, nil)
	if err != nil {
		t.Fatalf("observe session with exited pane: %v", err)
	}

	assertPreservedDeadAndLivePanes(t, observation)
}

func assertPreservedNonrestartablePane(t *testing.T, observation capture.Observation, dead bool, reason string) {
	t.Helper()

	if len(observation.Manifest.Panes) != 1 {
		t.Fatalf("captured %d panes, want 1", len(observation.Manifest.Panes))
	}

	pane := observation.Manifest.Panes[0]
	if pane.Dead != dead {
		t.Fatalf("pane dead = %v, want %v", pane.Dead, dead)
	}

	assertPaneArtifactRetained(t, observation, pane)

	if pane.Launch.Recovery != model.RecoveryNonrestartable || pane.Launch.Reason != reason {
		t.Fatalf("launch recovery/reason = %q/%q, want %q/%q", pane.Launch.Recovery, pane.Launch.Reason, model.RecoveryNonrestartable, reason)
	}
}

func assertPreservedDeadAndLivePanes(t *testing.T, observation capture.Observation) {
	t.Helper()

	if len(observation.Manifest.Panes) != 2 {
		t.Fatalf("captured %d panes, want dead and unaffected panes", len(observation.Manifest.Panes))
	}

	var deadCount, liveCount int

	for _, pane := range observation.Manifest.Panes {
		assertPaneArtifactRetained(t, observation, pane)

		if pane.Dead {
			deadCount++

			if pane.Launch.Recovery != model.RecoveryNonrestartable || pane.Launch.Reason != string(model.ReasonPaneDead) {
				t.Fatalf("dead pane recovery/reason = %q/%q, want %q/%q", pane.Launch.Recovery, pane.Launch.Reason, model.RecoveryNonrestartable, string(model.ReasonPaneDead))
			}

			continue
		}

		liveCount++

		if pane.Launch.Recovery != model.RecoveryUnverified || pane.Launch.Reason != "" {
			t.Fatalf("unaffected pane recovery/reason = %q/%q, want %q with no reason", pane.Launch.Recovery, pane.Launch.Reason, model.RecoveryUnverified)
		}
	}

	if deadCount != 1 || liveCount != 1 {
		t.Fatalf("dead/live pane counts = %d/%d, want 1/1", deadCount, liveCount)
	}
}

func assertPaneArtifactRetained(t *testing.T, observation capture.Observation, pane model.Pane) {
	t.Helper()

	if pane.Terminal.Artifact == "" {
		t.Fatalf("pane %s has no terminal artifact reference", pane.ID)
	}

	if _, ok := observation.Artifacts[pane.Terminal.Artifact]; !ok {
		t.Fatalf("pane %s terminal artifact %s was not retained", pane.ID, pane.Terminal.Artifact)
	}
}

type processLifetime struct {
	pid   int
	birth string
}

func assertPaneProcessLifetimes(t *testing.T, panes []model.Pane, panePIDs map[string]int) {
	t.Helper()

	lifetimes := make(map[processLifetime]bool)

	for _, pane := range panes {
		if len(pane.Launch.Birth) == 0 {
			t.Fatal("pane missing birth bytes")
		}

		pid, ok := panePIDs[pane.ID]
		if !ok {
			t.Fatalf("captured pane %q has no corresponding live PID", pane.ID)
		}

		birth, err := process.ReadIdentity(t.Context(), pid)
		if err != nil {
			t.Fatalf("read identity for pane %q PID %d: %v", pane.ID, pid, err)
		}

		if got := string(pane.Launch.Birth); got != birth.String() {
			t.Fatalf("pane %q birth was not bound to PID %d: got %s, want %s", pane.ID, pid, got, birth.String())
		}

		lifetime := processLifetime{pid: pid, birth: birth.String()}
		if lifetimes[lifetime] {
			t.Fatalf("duplicate PID and birth pair assigned across panes: %+v", lifetime)
		}

		lifetimes[lifetime] = true
	}
}

func TestProcessObservationMultiplePanesInClosure(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)
	dir := t.TempDir()

	report1 := filepath.Join(dir, "pane1.report")
	report2 := filepath.Join(dir, "pane2.report")

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    "multi-pane",
		Dir:     dir,
		Program: gotmux.Exec(helper, "-report", report1, "-hold", "300s", "pane1_arg"),
		TmuxEnv: map[string]string{"PANE_TAG": "one"},
		Size:    gotmux.Size{Width: 120, Height: 40},
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	firstPane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	_, err = firstPane.Split(t.Context(), gotmux.SplitOptions{
		Direction: gotmux.DirectionVertical,
		Program:   gotmux.Exec(helper, "-report", report2, "-hold", "300s", "pane2_arg"),
		TmuxEnv:   map[string]string{"PANE_TAG": "two"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = awaitProcessReport(t, report1)
	_ = awaitProcessReport(t, report2)

	liveSnapshot, err := server.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("snapshot pane identities: %v", err)
	}

	panePIDs := make(map[string]int, len(liveSnapshot.Panes()))
	for _, pane := range liveSnapshot.Panes() {
		panePIDs["p"+strings.TrimPrefix(string(pane.ID), "%")] = pane.PID
	}

	obs, err := capture.ObserveWithProcessEnrichment(t.Context(), server, []gotmux.SessionID{session.ID()}, []string{"PANE_TAG"})
	if err != nil {
		t.Fatalf("observation failed: %v", err)
	}

	if len(obs.Manifest.Panes) != 2 {
		t.Fatalf("expected 2 panes in closure, got %d", len(obs.Manifest.Panes))
	}

	assertPaneProcessLifetimes(t, obs.Manifest.Panes, panePIDs)
}

func TestProcessObservationSaveLoggingPrivacyAndIsolation(t *testing.T) {
	server := tmuxtest.NewServer(t)
	helper := buildLaunchHelper(t)
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "log_privacy.report")

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    "log-priv",
		Dir:     dir,
		Program: gotmux.Exec(helper, "-report", reportPath, "-hold", "300s", "CUSTOM_ARG_VALUE_9988"),
		TmuxEnv: map[string]string{"CUSTOM_ENV_KEY": "CUSTOM_ENV_VALUE_7766"},
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = awaitProcessReport(t, reportPath)

	r := openSaveRepository(t)
	leaseRoot := filepath.Join(t.TempDir(), "leases")

	var logBuf bytes.Buffer

	logger := zerolog.New(&logBuf)
	ctx := logger.WithContext(t.Context())

	cfg := engine.SaveConfig{
		LeaseRoot:              leaseRoot,
		ProcessEnrichment:      true,
		ProcessEnvironmentKeys: []string{"CUSTOM_ENV_KEY"},
	}

	result, err := engine.SaveObservation(ctx, r, server, []gotmux.SessionID{session.ID()}, cfg)
	if err != nil {
		t.Fatalf("save failed: %v", err)
	}

	if result.Status != "partial" {
		t.Fatalf("expected partial status, got %q", result.Status)
	}

	logData := logBuf.Bytes()
	logStr := string(logData)

	assertSaveLogPrivacy(t, logStr, []string{
		"CUSTOM_ARG_VALUE_9988",
		"CUSTOM_ENV_VALUE_7766",
		"log_privacy.report",
		dir,
		helper,
	})

	lines := bytes.Split(bytes.TrimSpace(logData), []byte{'\n'})
	assertSaveLogSchema(t, lines)
}

func assertSaveLogPrivacy(t *testing.T, logStr string, forbidden []string) {
	t.Helper()

	for _, f := range forbidden {
		if strings.Contains(logStr, f) {
			t.Fatalf("sensitive material %q leaked into log output:\n%s", f, logStr)
		}
	}
}

func assertSaveLogSchema(t *testing.T, lines [][]byte) {
	t.Helper()

	if len(lines) != 2 {
		t.Fatalf("expected exactly 2 log lines, got %d", len(lines))
	}

	for i, line := range lines {
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatalf("invalid JSON log line %d: %v", i, err)
		}

		if string(fields["component"]) != `"snapshot"` || string(fields["operation"]) != `"save"` {
			t.Fatalf("invalid component/operation at line %d: %s", i, line)
		}

		if i == 0 {
			if string(fields["level"]) != `"info"` || string(fields["status"]) != `"started"` {
				t.Fatalf("unexpected start log line: %s", line)
			}
		} else {
			if string(fields["level"]) != `"warn"` || string(fields["status"]) != `"partial"` {
				t.Fatalf("unexpected outcome log line: %s", line)
			}

			if string(fields["reason"]) != `"parity_incomplete"` {
				t.Fatalf("unexpected reason: %s", fields["reason"])
			}
		}
	}
}

func copyExecutable(t *testing.T, src, dst string) {
	t.Helper()

	srcFile, err := os.Open(filepath.Clean(src))
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	defer func() { _ = srcFile.Close() }()

	dstFile, err := os.OpenFile(filepath.Clean(dst), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	defer func() { _ = dstFile.Close() }()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		t.Fatalf("copy executable: %v", err)
	}
}
