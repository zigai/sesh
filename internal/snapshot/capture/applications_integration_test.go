//go:build integration && linux

package capture

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zigai/aht/v2/pkg/aht"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const applicationCaptureTimeout = 5 * time.Second

type applicationAgentSource struct {
	sessions []aht.Session
}

type unsupportedApplicationCase struct {
	name       string
	command    string
	foreground string
	ambiguous  bool
}

func (s applicationAgentSource) List(context.Context, aht.Filter) ([]aht.Session, error) {
	return slices.Clone(s.sessions), nil
}

func TestCaptureClassifiesShellForegroundMonitorInsteadOfShell(t *testing.T) {
	topPath := requireApplicationExecutable(t, "top")

	server, session, pane, workingDir := applicationCaptureFixture(t, "foreground-monitor")
	if err := pane.Submit(t.Context(), topPath); err != nil {
		t.Fatalf("start foreground monitor: %v", err)
	}

	foreground := waitForApplicationForeground(t, pane, "top")

	observation, err := ObserveWithApplicationRecovery(
		t.Context(), server, []gotmux.SessionID{session.ID()}, []string{"HOME"}, nil,
	)
	if err != nil {
		t.Fatalf("capture foreground monitor: %v", err)
	}

	launch := singleApplicationLaunch(t, observation)
	if foreground.PID == panePID(t, pane) {
		t.Fatal("fixture did not run monitor below its shell root")
	}

	if filepath.Base(string(launch.Executable)) != "top" || launch.Recovery != "restart" {
		t.Fatalf("monitor launch = %#v, want top restart", launch)
	}

	if !equalLaunchArguments(launch.Arguments, []string{"top"}) {
		t.Fatalf("monitor arguments = %q, want sanitized top restart", launch.Arguments)
	}

	assertApplicationDirectory(t, launch, workingDir)
}

func TestCaptureKeepsUnsupportedForegroundAndPipelineUnverified(t *testing.T) {
	tests := []unsupportedApplicationCase{
		{name: "unsupported command", command: "sleep 300", foreground: "sleep"},
		{name: "pipeline", command: "sleep 300 | cat", ambiguous: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testUnsupportedApplicationCapture(t, tt)
		})
	}
}

func testUnsupportedApplicationCapture(t *testing.T, tt unsupportedApplicationCase) {
	t.Helper()

	server, session, pane, workingDir := applicationCaptureFixture(t, "unsupported-"+tt.name)
	if err := pane.Submit(t.Context(), tt.command); err != nil {
		t.Fatalf("start application: %v", err)
	}

	if tt.ambiguous {
		waitForAmbiguousForeground(t, pane)
	} else {
		waitForApplicationForeground(t, pane, tt.foreground)
	}

	observation, err := ObserveWithApplicationRecovery(
		t.Context(), server, []gotmux.SessionID{session.ID()}, nil, nil,
	)
	if err != nil {
		t.Fatalf("capture unsupported application: %v", err)
	}

	launch := singleApplicationLaunch(t, observation)
	if launch.Recovery != "unverified" {
		t.Fatalf("unsupported launch recovery = %q, want unverified", launch.Recovery)
	}

	if tt.ambiguous && filepath.Base(string(launch.Executable)) != "bash" {
		t.Fatalf("pipeline selected an arbitrary member: executable = %q", launch.Executable)
	}

	if !tt.ambiguous && filepath.Base(string(launch.Executable)) != tt.foreground {
		t.Fatalf("unsupported foreground executable = %q, want %q", launch.Executable, tt.foreground)
	}

	assertApplicationDirectory(t, launch, workingDir)
}

func TestCaptureAgentResumeRequiresExactEndpointBinding(t *testing.T) {
	nodePath := requireApplicationExecutable(t, "node")

	server, session, pane, _ := applicationCaptureFixture(t, "agent-resume")
	fixtureDir := t.TempDir()
	scriptPath := filepath.Join(fixtureDir, "codex.js")

	if err := os.WriteFile(scriptPath, []byte("setInterval(() => {}, 1000);\n"), 0o600); err != nil {
		t.Fatalf("write agent fixture: %v", err)
	}

	resumeExecutable := filepath.Join(fixtureDir, "codex")
	if err := os.WriteFile(resumeExecutable, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("write resume executable: %v", err)
	}

	if err := os.Chmod(resumeExecutable, 0o700); err != nil {
		t.Fatalf("make resume executable: %v", err)
	}

	t.Setenv("PATH", fixtureDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := pane.Submit(t.Context(), nodePath+" "+scriptPath); err != nil {
		t.Fatalf("start agent fixture: %v", err)
	}

	waitForApplicationForeground(t, pane, "node")

	info, err := pane.Info(t.Context())
	if err != nil {
		t.Fatalf("read pane identity: %v", err)
	}

	registrySession := aht.Session{
		SchemaVersion: 3,
		ID:            "capture-agent",
		Harness:       aht.HarnessCodex,
		Liveness:      aht.Live{Activity: aht.ActivityRunning},
		SessionID:     "exact-session",
		ResumeCommand: []string{"codex", "resume", "exact-session"},
		Location: aht.Location{
			Kind:      aht.MultiplexerTmux,
			ServerID:  server.Endpoint().SocketPath,
			SessionID: string(session.ID()),
			PaneID:    string(info.ID),
			PanePID:   info.PID,
		},
	}

	observation, err := ObserveWithApplicationRecovery(
		t.Context(), server, []gotmux.SessionID{session.ID()}, nil,
		applicationAgentSource{sessions: []aht.Session{registrySession}},
	)
	if err != nil {
		t.Fatalf("capture exact agent binding: %v", err)
	}

	launch := singleApplicationLaunch(t, observation)
	if launch.Recovery != "resume" || string(launch.Executable) != resumeExecutable ||
		!equalLaunchArguments(launch.Arguments, registrySession.ResumeCommand) {
		t.Fatalf("agent resume launch = %#v", launch)
	}

	registrySession.Location.ServerID = filepath.Join(t.TempDir(), "wrong.sock")

	wrongEndpoint, err := ObserveWithApplicationRecovery(
		t.Context(), server, []gotmux.SessionID{session.ID()}, nil,
		applicationAgentSource{sessions: []aht.Session{registrySession}},
	)
	if err != nil {
		t.Fatalf("capture wrong endpoint binding: %v", err)
	}

	if launch := singleApplicationLaunch(t, wrongEndpoint); launch.Recovery != "unverified" {
		t.Fatalf("wrong endpoint authorized agent resume: %#v", launch)
	}
}

func applicationCaptureFixture(
	t *testing.T,
	name string,
) (*gotmux.Server, gotmux.Session, gotmux.Pane, string) {
	t.Helper()

	bashPath := requireApplicationExecutable(t, "bash")

	server := tmuxtest.NewServer(t)
	workingDir := t.TempDir()

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: name, Dir: workingDir,
		Program: gotmux.Exec(bashPath, "--noprofile", "--norc"), Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create application session: %v", err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatalf("read application window: %v", err)
	}

	pane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatalf("read application pane: %v", err)
	}

	return server, session, pane, workingDir
}

func waitForApplicationForeground(t *testing.T, pane gotmux.Pane, executable string) *process.Observation {
	t.Helper()

	info, err := pane.Info(t.Context())
	if err != nil {
		t.Fatalf("read pane process: %v", err)
	}

	birth, err := process.ReadIdentity(t.Context(), info.PID)
	if err != nil {
		t.Fatalf("read pane birth: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), applicationCaptureTimeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		observation, observeErr := process.ObserveForeground(ctx, process.ForegroundRequest{
			PanePID: info.PID, ExpectedPaneBirth: birth, EnvironmentKeys: nil,
		})
		if observeErr == nil && filepath.Base(string(observation.Executable)) == executable {
			return observation
		}

		select {
		case <-ctx.Done():
			t.Fatalf("foreground %q not observed: %v", executable, errors.Join(observeErr, ctx.Err()))
		case <-ticker.C:
		}
	}
}

func waitForAmbiguousForeground(t *testing.T, pane gotmux.Pane) {
	t.Helper()

	info, err := pane.Info(t.Context())
	if err != nil {
		t.Fatalf("read pane process: %v", err)
	}

	birth, err := process.ReadIdentity(t.Context(), info.PID)
	if err != nil {
		t.Fatalf("read pane birth: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), applicationCaptureTimeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		_, observeErr := process.ObserveForeground(ctx, process.ForegroundRequest{
			PanePID: info.PID, ExpectedPaneBirth: birth, EnvironmentKeys: nil,
		})
		if errors.Is(observeErr, process.ErrForegroundAmbiguous) {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("ambiguous foreground not observed: %v", errors.Join(observeErr, ctx.Err()))
		case <-ticker.C:
		}
	}
}

func singleApplicationLaunch(t *testing.T, observation Observation) model.Launch {
	t.Helper()

	if len(observation.Manifest.Panes) != 1 {
		t.Fatalf("captured %d panes, want 1", len(observation.Manifest.Panes))
	}

	return observation.Manifest.Panes[0].Launch
}

func equalLaunchArguments(arguments []model.Bytes, expected []string) bool {
	if len(arguments) != len(expected) {
		return false
	}

	for index := range expected {
		if string(arguments[index]) != expected[index] {
			return false
		}
	}

	return true
}

func assertApplicationDirectory(t *testing.T, launch model.Launch, expected string) {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(expected)
	if err != nil {
		resolved = expected
	}

	if string(launch.Directory) != resolved {
		t.Fatalf("launch directory = %q, want %q", launch.Directory, resolved)
	}
}

func panePID(t *testing.T, pane gotmux.Pane) int {
	t.Helper()

	info, err := pane.Info(t.Context())
	if err != nil {
		t.Fatalf("read pane info: %v", err)
	}

	return info.PID
}

func requireApplicationExecutable(t *testing.T, name string) string {
	t.Helper()

	path, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("SESH_APPLICATION_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("required application fixture %s is unavailable: %v", name, err)
		}

		t.Skipf("%s is unavailable", name)
	}

	return path
}

func waitForPaneDead(t *testing.T, pane gotmux.Pane) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), applicationCaptureTimeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		info, err := pane.Info(ctx)
		if err == nil && (info.Dead || info.PID <= 0) {
			return
		}

		select {
		case <-ctx.Done():
			t.Fatalf("pane did not become dead within timeout: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestCapturePreservesDeadPaneAlongsideHealthyPane(t *testing.T) {
	bashPath := requireApplicationExecutable(t, "bash")
	server := tmuxtest.NewServer(t)
	workingDir := t.TempDir()

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: "dead-and-healthy", Dir: workingDir,
		Program: gotmux.Exec(bashPath, "--noprofile", "--norc"), Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatalf("read window: %v", err)
	}

	deadPane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatalf("read active pane: %v", err)
	}

	healthyPane, err := deadPane.Split(t.Context(), gotmux.SplitOptions{
		Program: gotmux.Exec(bashPath, "--noprofile", "--norc"),
		Dir:     workingDir,
	})
	if err != nil {
		t.Fatalf("split pane: %v", err)
	}

	if err := deadPane.Options().Set(t.Context(), "remain-on-exit", "on"); err != nil {
		t.Fatalf("set remain-on-exit: %v", err)
	}

	if err := deadPane.Submit(t.Context(), `printf "DEAD_PANE_OUTPUT_MARKER\n"; exit 0`); err != nil {
		t.Fatalf("submit dead pane command: %v", err)
	}

	if err := healthyPane.Submit(t.Context(), `printf "HEALTHY_PANE_OUTPUT_MARKER\n"; exec sleep 300`); err != nil {
		t.Fatalf("submit healthy pane command: %v", err)
	}

	waitForPaneDead(t, deadPane)
	waitForApplicationForeground(t, healthyPane, "sleep")

	observation, err := ObserveWithApplicationRecovery(
		t.Context(), server, []gotmux.SessionID{session.ID()}, []string{"HOME"}, nil,
	)
	if err != nil {
		t.Fatalf("ObserveWithApplicationRecovery failed: %v", err)
	}

	assertDeadAndHealthyPanes(t, observation)
}

func assertDeadAndHealthyPanes(t *testing.T, observation Observation) {
	t.Helper()

	if len(observation.Manifest.Panes) != 2 {
		t.Fatalf("captured %d panes, want 2", len(observation.Manifest.Panes))
	}

	var foundDead, foundHealthy bool

	for _, pane := range observation.Manifest.Panes {
		artifactData, ok := observation.Artifacts[pane.Terminal.Artifact]
		if !ok {
			t.Fatalf("missing artifact %s for pane %s", pane.Terminal.Artifact, pane.ID)
		}

		if pane.Dead {
			foundDead = true

			assertDeadPaneState(t, pane, artifactData)
		} else {
			foundHealthy = true

			assertHealthyPaneState(t, pane, artifactData)
		}
	}

	if !foundDead || !foundHealthy {
		t.Fatalf("foundDead=%v foundHealthy=%v, want both", foundDead, foundHealthy)
	}
}

func assertDeadPaneState(t *testing.T, pane model.Pane, data []byte) {
	t.Helper()

	if pane.Launch.Recovery != model.RecoveryNonrestartable {
		t.Fatalf("dead pane recovery = %q, want nonrestartable", pane.Launch.Recovery)
	}

	if pane.Launch.Reason != string(model.ReasonPaneDead) {
		t.Fatalf("dead pane reason = %q, want pane_dead", pane.Launch.Reason)
	}

	if !bytes.Contains(data, []byte("DEAD_PANE_OUTPUT_MARKER")) {
		t.Fatalf("dead pane artifact missing expected text: %q", string(data))
	}

	if bytes.Contains(data, []byte("HEALTHY_PANE_OUTPUT_MARKER")) {
		t.Fatalf("dead pane artifact contains healthy pane output: %q", string(data))
	}
}

func assertHealthyPaneState(t *testing.T, pane model.Pane, data []byte) {
	t.Helper()

	if pane.Launch.Recovery != model.RecoveryUnverified {
		t.Fatalf("healthy pane recovery = %q, want unverified", pane.Launch.Recovery)
	}

	if !bytes.Contains(data, []byte("HEALTHY_PANE_OUTPUT_MARKER")) {
		t.Fatalf("healthy pane artifact missing expected text: %q", string(data))
	}

	if bytes.Contains(data, []byte("DEAD_PANE_OUTPUT_MARKER")) {
		t.Fatalf("healthy pane artifact contains dead pane output: %q", string(data))
	}
}

func TestCapturePreservesPaneWithDeletedExecutable(t *testing.T) {
	sleepPath := requireApplicationExecutable(t, "sleep")
	server := tmuxtest.NewServer(t)
	workingDir := t.TempDir()
	tempBin := filepath.Join(workingDir, "temp-sleep")

	copyCmd := exec.CommandContext(t.Context(), "cp", sleepPath, tempBin)
	if err := copyCmd.Run(); err != nil {
		t.Fatalf("copy sleep binary: %v", err)
	}

	if err := os.Chmod(tempBin, 0o700); err != nil {
		t.Fatalf("chmod temp binary: %v", err)
	}

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: "deleted-exe", Dir: workingDir,
		Program: gotmux.Exec(tempBin, "300"), Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatalf("read window: %v", err)
	}

	pane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatalf("read pane: %v", err)
	}

	waitForApplicationForeground(t, pane, "temp-sleep")

	if err := os.Remove(tempBin); err != nil {
		t.Fatalf("remove temp binary: %v", err)
	}

	observation, err := ObserveWithApplicationRecovery(
		t.Context(), server, []gotmux.SessionID{session.ID()}, []string{"HOME"}, nil,
	)
	if err != nil {
		t.Fatalf("ObserveWithApplicationRecovery with deleted executable failed: %v", err)
	}

	assertDeletedExecutablePane(t, observation)
}

func assertDeletedExecutablePane(t *testing.T, observation Observation) {
	t.Helper()

	if len(observation.Manifest.Panes) != 1 {
		t.Fatalf("captured %d panes, want 1", len(observation.Manifest.Panes))
	}

	paneRecord := observation.Manifest.Panes[0]
	if paneRecord.Dead {
		t.Fatal("pane with running deleted binary was marked dead")
	}

	if paneRecord.Launch.Recovery != model.RecoveryNonrestartable {
		t.Fatalf("recovery = %q, want nonrestartable", paneRecord.Launch.Recovery)
	}

	if paneRecord.Launch.Reason != string(model.ReasonExecutableDeleted) {
		t.Fatalf("reason = %q, want executable_deleted", paneRecord.Launch.Reason)
	}

	artifactID := paneRecord.Terminal.Artifact
	if _, ok := observation.Artifacts[artifactID]; !ok {
		t.Fatalf("missing terminal artifact %s", artifactID)
	}
}

func TestCapturePreservesPaneWithDeletedWorkingDirectory(t *testing.T) {
	sleepPath := requireApplicationExecutable(t, "sleep")
	server := tmuxtest.NewServer(t)
	baseDir := t.TempDir()
	workingDir := filepath.Join(baseDir, "cwd-to-delete")

	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatalf("mkdir working dir: %v", err)
	}

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: "deleted-cwd", Dir: workingDir,
		Program: gotmux.Exec(sleepPath, "300"), Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatalf("read window: %v", err)
	}

	pane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatalf("read pane: %v", err)
	}

	waitForApplicationForeground(t, pane, "sleep")

	if err := os.Remove(workingDir); err != nil {
		t.Fatalf("remove working dir: %v", err)
	}

	observation, err := ObserveWithApplicationRecovery(
		t.Context(), server, []gotmux.SessionID{session.ID()}, []string{"HOME"}, nil,
	)
	if err != nil {
		t.Fatalf("ObserveWithApplicationRecovery with deleted cwd failed: %v", err)
	}

	assertDeletedWorkingDirPane(t, observation)
}

func assertDeletedWorkingDirPane(t *testing.T, observation Observation) {
	t.Helper()

	if len(observation.Manifest.Panes) != 1 {
		t.Fatalf("captured %d panes, want 1", len(observation.Manifest.Panes))
	}

	paneRecord := observation.Manifest.Panes[0]
	if paneRecord.Dead {
		t.Fatal("pane with running deleted cwd was marked dead")
	}

	if paneRecord.Launch.Recovery != model.RecoveryNonrestartable {
		t.Fatalf("recovery = %q, want nonrestartable", paneRecord.Launch.Recovery)
	}

	if paneRecord.Launch.Reason != string(model.ReasonWorkingDirectoryDeleted) {
		t.Fatalf("reason = %q, want working_directory_deleted", paneRecord.Launch.Reason)
	}

	artifactID := paneRecord.Terminal.Artifact
	if _, ok := observation.Artifacts[artifactID]; !ok {
		t.Fatalf("missing terminal artifact %s", artifactID)
	}
}

func TestCaptureRetriesAndPreservesPaneAcrossProcessReplacement(t *testing.T) {
	sleepPath := requireApplicationExecutable(t, "sleep")
	server := tmuxtest.NewServer(t)
	workingDir := t.TempDir()

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: "process-replacement", Dir: workingDir,
		Program: gotmux.Exec(sleepPath, "300"), Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatalf("read window: %v", err)
	}

	pane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatalf("read pane: %v", err)
	}

	waitForApplicationForeground(t, pane, "sleep")
	initialPID := panePID(t, pane)

	replaced := false

	beforeCompareGraphHook = func() {
		if !replaced {
			replaced = true

			if respawnErr := pane.Respawn(t.Context(), gotmux.RespawnOptions{
				KillRunning: true,
				Program:     gotmux.Exec("sh", "-c", `printf "REPLACEMENT_MARKER\n"; exec sleep 300`),
			}); respawnErr != nil {
				t.Errorf("respawn pane: %v", respawnErr)
			}

			time.Sleep(50 * time.Millisecond)
		}
	}
	defer func() { beforeCompareGraphHook = nil }()

	observation, err := ObserveWithApplicationRecovery(
		t.Context(), server, []gotmux.SessionID{session.ID()}, []string{"HOME"}, nil,
	)
	if err != nil {
		t.Fatalf("ObserveWithApplicationRecovery failed across replacement: %v", err)
	}

	if !replaced {
		t.Fatal("hook did not fire to replace process")
	}

	if len(observation.Manifest.Panes) != 1 {
		t.Fatalf("captured %d panes, want 1", len(observation.Manifest.Panes))
	}

	currentPID := panePID(t, pane)
	if currentPID == initialPID {
		t.Fatalf("PID did not change across replacement: %d", currentPID)
	}

	artifactID := observation.Manifest.Panes[0].Terminal.Artifact

	data, ok := observation.Artifacts[artifactID]
	if !ok {
		t.Fatalf("missing artifact %s", artifactID)
	}

	if !bytes.Contains(data, []byte("REPLACEMENT_MARKER")) {
		t.Fatalf("artifact missing replacement marker: %q", string(data))
	}
}

func TestCaptureFailsWhenProcessReplacementNeverSettles(t *testing.T) {
	sleepPath := requireApplicationExecutable(t, "sleep")
	server := tmuxtest.NewServer(t)
	workingDir := t.TempDir()

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: "continuous-churn", Dir: workingDir,
		Program: gotmux.Exec(sleepPath, "300"), Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatalf("read window: %v", err)
	}

	pane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatalf("read pane: %v", err)
	}

	waitForApplicationForeground(t, pane, "sleep")

	beforeCompareGraphHook = func() {
		_ = pane.Respawn(t.Context(), gotmux.RespawnOptions{
			KillRunning: true,
			Program:     gotmux.Exec(sleepPath, "300"),
		})

		time.Sleep(10 * time.Millisecond)
	}
	defer func() { beforeCompareGraphHook = nil }()

	_, err = ObserveWithApplicationRecovery(
		t.Context(), server, []gotmux.SessionID{session.ID()}, []string{"HOME"}, nil,
	)
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("expected ErrChanged for continuous churn, got: %v", err)
	}
}

func TestCaptureRestartsAShellWhoseExecutableWasReplacedInPlace(t *testing.T) {
	observation := observeShellAfterReplacingItsExecutable(t, 0o700)

	pane := observation.Manifest.Panes[0]
	if pane.Launch.Recovery != model.RecoveryRestart {
		t.Fatalf("recovery = %q (reason %q), want restart from the upgraded executable", pane.Launch.Recovery, pane.Launch.Reason)
	}
}

func TestCaptureKeepsAPaneNonrestartableWhenItsExecutablePathHoldsNoExecutable(t *testing.T) {
	observation := observeShellAfterReplacingItsExecutable(t, 0o600)

	assertDeletedExecutablePane(t, observation)
}

func observeShellAfterReplacingItsExecutable(t *testing.T, replacementMode os.FileMode) Observation {
	t.Helper()

	shellPath := requireApplicationExecutable(t, "sh")
	server := tmuxtest.NewServer(t)
	workingDir := t.TempDir()
	binDir := filepath.Join(workingDir, "bin")

	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}

	installed := filepath.Join(binDir, "sh")
	if err := exec.CommandContext(t.Context(), "cp", shellPath, installed).Run(); err != nil {
		t.Fatalf("install shell copy: %v", err)
	}

	session, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: "replaced-exe", Dir: workingDir,
		Program: gotmux.Exec(installed), Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	windows, _, err := session.WindowInfos(t.Context())
	if err != nil {
		t.Fatalf("read window: %v", err)
	}

	pane, err := windows[0].Handle().ActivePane(t.Context())
	if err != nil {
		t.Fatalf("read pane: %v", err)
	}

	waitForApplicationForeground(t, pane, "sh")

	replacement := filepath.Join(binDir, "sh.new")
	if err := exec.CommandContext(t.Context(), "cp", shellPath, replacement).Run(); err != nil {
		t.Fatalf("write replacement: %v", err)
	}

	if err := os.Chmod(replacement, replacementMode); err != nil {
		t.Fatalf("set replacement mode: %v", err)
	}

	if err := os.Rename(replacement, installed); err != nil {
		t.Fatalf("replace shell copy: %v", err)
	}

	observation, err := ObserveWithApplicationRecovery(
		t.Context(), server, []gotmux.SessionID{session.ID()}, []string{"HOME"}, nil,
	)
	if err != nil {
		t.Fatalf("ObserveWithApplicationRecovery failed: %v", err)
	}

	if len(observation.Manifest.Panes) != 1 {
		t.Fatalf("captured %d panes, want 1", len(observation.Manifest.Panes))
	}

	return observation
}

func TestCaptureResumesAnAgentWhoseRunningVersionWasRemovedByAnUpdate(t *testing.T) {
	launch, link := captureAgentAfterUpdate(t, true)

	if launch.Recovery != model.RecoveryResume || string(launch.Executable) != link ||
		!equalLaunchArguments(launch.Arguments, []string{"codex", "resume", "exact-session"}) {
		t.Fatalf("agent launch after its running version was removed = %#v, want a resume through %s", launch, link)
	}
}

func TestCaptureKeepsAnAgentNonrestartableWhenNoVersionIsInstalled(t *testing.T) {
	launch, _ := captureAgentAfterUpdate(t, false)

	if launch.Recovery != model.RecoveryNonrestartable || launch.Reason != string(model.ReasonExecutableDeleted) {
		t.Fatalf("agent launch with no installed version = %#v, want nonrestartable executable_deleted", launch)
	}
}

func captureAgentAfterUpdate(t *testing.T, installNewer bool) (model.Launch, string) {
	t.Helper()

	sleepPath := requireApplicationExecutable(t, "sleep")
	cpPath := requireApplicationExecutable(t, "cp")
	server, session, pane, _ := applicationCaptureFixture(t, "agent-update")
	fixtureDir := t.TempDir()
	versions := filepath.Join(fixtureDir, "versions")
	link := filepath.Join(fixtureDir, "codex")

	if err := os.Mkdir(versions, 0o700); err != nil {
		t.Fatal(err)
	}

	install := func(version string) {
		t.Helper()

		target := filepath.Join(versions, version)
		if err := exec.CommandContext(t.Context(), cpPath, sleepPath, target).Run(); err != nil {
			t.Fatalf("install version %s: %v", version, err)
		}

		if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}

		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("link version %s: %v", version, err)
		}
	}

	install("1.0")
	t.Setenv("PATH", fixtureDir)

	if err := pane.Submit(t.Context(), link+" 300"); err != nil {
		t.Fatalf("start agent fixture: %v", err)
	}

	waitForApplicationForeground(t, pane, "1.0")

	if err := os.Remove(filepath.Join(versions, "1.0")); err != nil {
		t.Fatal(err)
	}

	if installNewer {
		install("2.0")
	} else if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}

	info, err := pane.Info(t.Context())
	if err != nil {
		t.Fatalf("read pane identity: %v", err)
	}

	registrySession := aht.Session{
		SchemaVersion: 3,
		ID:            "updated-agent",
		Harness:       aht.HarnessCodex,
		Liveness:      aht.Live{Activity: aht.ActivityRunning},
		SessionID:     "exact-session",
		ResumeCommand: []string{"codex", "resume", "exact-session"},
		Location: aht.Location{
			Kind:      aht.MultiplexerTmux,
			ServerID:  server.Endpoint().SocketPath,
			SessionID: string(session.ID()),
			PaneID:    string(info.ID),
			PanePID:   info.PID,
		},
	}

	observation, err := ObserveWithApplicationRecovery(
		t.Context(), server, []gotmux.SessionID{session.ID()}, nil,
		applicationAgentSource{sessions: []aht.Session{registrySession}},
	)
	if err != nil {
		t.Fatalf("capture updated agent: %v", err)
	}

	return singleApplicationLaunch(t, observation), link
}
