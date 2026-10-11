//go:build integration && linux

package engine_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zigai/aht/v2/pkg/aht"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const practicalApplicationTimeout = 10 * time.Second

func TestEnginePracticalRestoreRestartsForegroundMonitorOnce(t *testing.T) {
	topPath := requirePracticalTop(t)
	t.Setenv("HOME", t.TempDir())

	source := tmuxtest.NewServer(t)
	target := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	sourceSession, err := source.NewSession(ctx, gotmux.NewSessionOptions{
		Name: "practical-top", Program: gotmux.Exec(topPath, "-d", "100"), Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("start source top: %v", err)
	}

	disableSourceWindowRename(t, ctx, sourceSession)
	waitForPracticalTop(t, ctx, sourceSession)

	repo := openFixture(t, ctx, filepath.Join(t.TempDir(), "repo"))
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	saveConfig := engine.SaveConfig{
		LeaseRoot: leaseRoot, ProcessEnrichment: true, Profile: engine.RecoveryPractical,
	}

	saved := savePracticalObservation(t, ctx, repo, source, sourceSession, saveConfig)
	newer := savePracticalObservation(t, ctx, repo, source, sourceSession, saveConfig)
	assertDistinctPracticalGeneration(t, saved, newer)

	cfg := engine.RestoreConfig{
		LeaseRoot: leaseRoot, Generation: saved.Generation, LaunchProcesses: true,
		MissingWorkingDir: restore.MissingWorkingDirFail, Profile: engine.RecoveryPractical,
	}
	restored := restorePracticalSnapshot(t, ctx, repo, target, cfg)

	assertPracticalOperationSettled(t, ctx, repo, restored.OperationID)

	restoredSession, err := target.FindSession(ctx, "practical-top")
	if err != nil {
		t.Fatalf("find restored session: %v", err)
	}

	foregroundBefore := waitForPracticalTop(t, ctx, restoredSession)
	cfg.OperationID = restored.OperationID

	assertCompletedPracticalRetryRejected(t, ctx, repo, target, cfg)

	foregroundAfter := waitForPracticalTop(t, ctx, restoredSession)
	assertSamePracticalTop(t, foregroundBefore, foregroundAfter)

	assertSinglePracticalPane(t, ctx, target, restoredSession)

	assertPracticalOperationSettled(t, ctx, repo, restored.OperationID)
	assertSourcePinReleased(t, ctx, repo, saved.Generation)
}

func requireFixtureExecutable(t *testing.T, name string) string {
	t.Helper()

	path, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("SESH_APPLICATION_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s fixture required by SESH_APPLICATION_INTEGRATION_REQUIRED=1: %v", name, err)
		}

		t.Skipf("%s fixture unavailable: %v", name, err)
	}

	return path
}

func requirePracticalTop(t *testing.T) string {
	t.Helper()

	return requireFixtureExecutable(t, "top")
}

func savePracticalObservation(
	t *testing.T,
	ctx context.Context,
	repo *store.Repository,
	server *gotmux.Server,
	session gotmux.Session,
	cfg engine.SaveConfig,
) engine.SaveResult {
	t.Helper()

	result, err := engine.SaveObservation(ctx, repo, server, []gotmux.SessionID{session.ID()}, cfg)
	if err != nil {
		t.Fatalf("SaveObservation failed: %v", err)
	}

	if result.Generation == "" || result.Status != "best_effort" {
		t.Fatalf("save result = %+v, want explicit best-effort generation", result)
	}

	return result
}

func assertDistinctPracticalGeneration(t *testing.T, saved, newer engine.SaveResult) {
	t.Helper()

	if newer.Generation == saved.Generation {
		t.Fatalf("newer save result = %+v, want distinct current generation", newer)
	}
}

func restorePracticalSnapshot(
	t *testing.T,
	ctx context.Context,
	repo *store.Repository,
	server *gotmux.Server,
	cfg engine.RestoreConfig,
) engine.RestoreResult {
	t.Helper()

	result, err := engine.Restore(ctx, repo, server, cfg)
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	if result.Status != "best_effort" || !result.Verified {
		t.Fatalf("restore result = %+v, want verified best-effort", result)
	}

	return result
}

func assertCompletedPracticalRetryRejected(
	t *testing.T,
	ctx context.Context,
	repo *store.Repository,
	server *gotmux.Server,
	cfg engine.RestoreConfig,
) {
	t.Helper()

	_, err := engine.Restore(ctx, repo, server, cfg)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("completed operation retry error = %v, want repository conflict", err)
	}
}

func assertSamePracticalTop(t *testing.T, before, after *process.Observation) {
	t.Helper()
	assertSamePracticalProcess(t, before, after)
}

func assertSamePracticalProcess(t *testing.T, before, after *process.Observation) {
	t.Helper()

	if after.PID != before.PID || after.Birth != before.Birth {
		t.Fatalf("completed retry relaunched process: before pid=%d birth=%+v, after pid=%d birth=%+v",
			before.PID, before.Birth, after.PID, after.Birth)
	}
}

func assertSinglePracticalPane(t *testing.T, ctx context.Context, server *gotmux.Server, session gotmux.Session) {
	t.Helper()

	sessions, err := server.Sessions(ctx)
	if err != nil {
		t.Fatalf("list restored sessions: %v", err)
	}

	restoredCount := 0

	for _, candidate := range sessions {
		if candidate.Name != "fixture" {
			restoredCount++
		}
	}

	if restoredCount != 1 {
		t.Fatalf("retry left %d restored sessions, want 1", restoredCount)
	}

	windows, _, err := session.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("read restored windows: %v", err)
	}

	if len(windows) != 1 {
		t.Fatalf("retry left %d windows, want 1", len(windows))
	}

	panes, err := windows[0].Handle().Panes(ctx)
	if err != nil {
		t.Fatalf("read restored panes: %v", err)
	}

	if len(panes) != 1 {
		t.Fatalf("retry left %d panes, want 1", len(panes))
	}
}

func waitForPracticalTop(t *testing.T, ctx context.Context, session gotmux.Session) *process.Observation {
	t.Helper()
	return waitForPracticalForeground(t, ctx, session, "top")
}

func waitForPracticalForeground(t *testing.T, ctx context.Context, session gotmux.Session, name string) *process.Observation {
	t.Helper()

	windows, _, err := session.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("read session windows: %v", err)
	}

	if len(windows) != 1 {
		t.Fatalf("session has %d windows, want 1", len(windows))
	}

	pane, err := windows[0].Handle().ActivePane(ctx)
	if err != nil {
		t.Fatalf("read active pane: %v", err)
	}

	info, err := pane.Info(ctx)
	if err != nil {
		t.Fatalf("read pane process: %v", err)
	}

	birth, err := process.ReadIdentity(ctx, info.PID)
	if err != nil {
		t.Fatalf("read pane identity: %v", err)
	}

	observeCtx, cancel := context.WithTimeout(ctx, practicalApplicationTimeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		observation, observeErr := process.ObserveForeground(observeCtx, process.ForegroundRequest{
			PanePID: info.PID, ExpectedPaneBirth: birth,
		})
		if observeErr == nil && filepath.Base(string(observation.Executable)) == name {
			return observation
		}

		select {
		case <-observeCtx.Done():
			t.Fatalf("foreground %s not observed: %v", name, errors.Join(observeErr, observeCtx.Err()))
		case <-ticker.C:
		}
	}
}

func assertPracticalOperationSettled(t *testing.T, ctx context.Context, repo *store.Repository, operationID string) {
	t.Helper()

	status, err := engine.ReconcileRestore(ctx, repo, operationID)
	if err != nil {
		t.Fatalf("ReconcileRestore failed: %v", err)
	}

	if status.State != "complete" || !status.Settled || len(status.Uncertain) != 0 {
		t.Fatalf("operation status = %+v, want settled complete operation", status)
	}
}

type syntheticAgentSource struct {
	sessions []aht.Session
}

func (s syntheticAgentSource) List(_ context.Context, _ aht.Filter) ([]aht.Session, error) {
	return s.sessions, nil
}

func waitForFile(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("file %s not created within deadline", path)
}

func TestEnginePracticalRestoreNvimReopensFilesAfterSourceDestruction(t *testing.T) {
	nvimPath := requireFixtureExecutable(t, "nvim")
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	file1 := filepath.Join(tempDir, "first_document.txt")
	file2 := filepath.Join(tempDir, "second_document.txt")

	if err := os.WriteFile(file1, []byte("content one\n"), 0o600); err != nil {
		t.Fatalf("write file1: %v", err)
	}

	if err := os.WriteFile(file2, []byte("content two\n"), 0o600); err != nil {
		t.Fatalf("write file2: %v", err)
	}

	runtimeDir := filepath.Join(tempDir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}

	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	source := tmuxtest.NewServer(t)
	target := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	sourceSession, err := source.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "practical-nvim",
		Program: gotmux.Exec(nvimPath, file1, file2),
		TmuxEnv: map[string]string{"XDG_RUNTIME_DIR": runtimeDir},
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("start source nvim: %v", err)
	}

	disableSourceWindowRename(t, ctx, sourceSession)
	waitForPracticalForeground(t, ctx, sourceSession, "nvim")

	repo := openFixture(t, ctx, filepath.Join(t.TempDir(), "repo"))
	leaseRoot := filepath.Join(t.TempDir(), "leases")
	saveConfig := engine.SaveConfig{
		LeaseRoot:              leaseRoot,
		ProcessEnrichment:      true,
		ProcessEnvironmentKeys: []string{"XDG_RUNTIME_DIR"},
		Profile:                engine.RecoveryPractical,
	}

	saved := savePracticalObservation(t, ctx, repo, source, sourceSession, saveConfig)
	newer := savePracticalObservation(t, ctx, repo, source, sourceSession, saveConfig)
	assertDistinctPracticalGeneration(t, saved, newer)

	if err := sourceSession.Kill(ctx); err != nil {
		t.Fatalf("kill source session: %v", err)
	}

	cfg := engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        saved.Generation,
		LaunchProcesses:   true,
		MissingWorkingDir: restore.MissingWorkingDirFail,
		Profile:           engine.RecoveryPractical,
	}
	restored := restorePracticalSnapshot(t, ctx, repo, target, cfg)

	assertPracticalOperationSettled(t, ctx, repo, restored.OperationID)

	if len(restored.Panes) != 1 {
		t.Fatalf("restored panes count = %d, want 1", len(restored.Panes))
	}

	paneRes := restored.Panes[0]
	if paneRes.Outcome != restore.PaneOutcomeRestartDispatch {
		t.Fatalf("pane outcome = %q, want %q", paneRes.Outcome, restore.PaneOutcomeRestartDispatch)
	}

	if paneRes.AppVerified {
		t.Fatalf("dispatch unexpectedly marked app-verified; dispatch is not app-verified recovery")
	}

	restoredSession, err := target.FindSession(ctx, "practical-nvim")
	if err != nil {
		t.Fatalf("find restored session: %v", err)
	}

	foregroundBefore := waitForPracticalForeground(t, ctx, restoredSession, "nvim")
	assertNvimReopenedFiles(t, ctx, restoredSession, foregroundBefore, file1, file2)

	cfg.OperationID = restored.OperationID

	assertCompletedPracticalRetryRejected(t, ctx, repo, target, cfg)
	foregroundAfter := waitForPracticalForeground(t, ctx, restoredSession, "nvim")
	assertSamePracticalProcess(t, foregroundBefore, foregroundAfter)
	assertNvimReopenedFiles(t, ctx, restoredSession, foregroundAfter, file1, file2)
	assertSinglePracticalPane(t, ctx, target, restoredSession)
	assertPracticalOperationSettled(t, ctx, repo, restored.OperationID)
	assertSourcePinReleased(t, ctx, repo, saved.Generation)
}

func TestEnginePracticalRestoreSyntheticAgentResumeExactArgv(t *testing.T) {
	nodePath := requireFixtureExecutable(t, "node")
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	fixtureDir := filepath.Join(tempDir, "fixtures")
	if err := os.MkdirAll(fixtureDir, 0o700); err != nil {
		t.Fatalf("create fixture dir: %v", err)
	}

	recordOut := filepath.Join(fixtureDir, "synthetic_agent_argv.txt")
	resumeScript := filepath.Join(fixtureDir, "codex")
	scriptContent := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nexec sleep 300\n", recordOut)

	if err := os.WriteFile(resumeScript, []byte(scriptContent), 0o600); err != nil {
		t.Fatalf("write resume script: %v", err)
	}

	if err := os.Chmod(resumeScript, 0o700); err != nil {
		t.Fatalf("chmod resume script: %v", err)
	}

	t.Setenv("PATH", fixtureDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	scriptPath := filepath.Join(fixtureDir, "codex.js")
	if err := os.WriteFile(scriptPath, []byte("setInterval(() => {}, 1000);\n"), 0o600); err != nil {
		t.Fatalf("write agent script: %v", err)
	}

	source := tmuxtest.NewServer(t)
	target := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	sourceSession, paneInfo := startSyntheticAgentSession(t, ctx, source, nodePath, scriptPath)

	registrySession := aht.Session{
		SchemaVersion: 3,
		ID:            "synthetic-agent-test",
		Harness:       aht.HarnessCodex,
		Liveness:      aht.Live{Activity: aht.ActivityRunning},
		SessionID:     "synthetic-session-987",
		ResumeCommand: []string{"codex", "resume", "synthetic-session-987"},
		Location: aht.Location{
			Kind:      aht.MultiplexerTmux,
			ServerID:  source.Endpoint().SocketPath,
			SessionID: string(sourceSession.ID()),
			PaneID:    string(paneInfo.ID),
			PanePID:   paneInfo.PID,
		},
	}

	repo := openFixture(t, ctx, filepath.Join(tempDir, "repo"))
	leaseRoot := filepath.Join(tempDir, "leases")
	saveConfig := engine.SaveConfig{
		LeaseRoot:         leaseRoot,
		ProcessEnrichment: true,
		Profile:           engine.RecoveryPractical,
		AgentSource:       syntheticAgentSource{sessions: []aht.Session{registrySession}},
	}

	saved := savePracticalObservation(t, ctx, repo, source, sourceSession, saveConfig)
	newer := savePracticalObservation(t, ctx, repo, source, sourceSession, saveConfig)
	assertDistinctPracticalGeneration(t, saved, newer)

	cfg := engine.RestoreConfig{
		LeaseRoot:       leaseRoot,
		Generation:      saved.Generation,
		LaunchProcesses: true,
		Profile:         engine.RecoveryPractical,
	}
	restored := restorePracticalSnapshot(t, ctx, repo, target, cfg)

	assertPracticalOperationSettled(t, ctx, repo, restored.OperationID)

	if len(restored.Panes) != 1 {
		t.Fatalf("restored panes count = %d, want 1", len(restored.Panes))
	}

	paneRes := restored.Panes[0]
	if paneRes.Outcome != restore.PaneOutcomeNativeResumeDispatch {
		t.Fatalf("pane outcome = %q, want %q", paneRes.Outcome, restore.PaneOutcomeNativeResumeDispatch)
	}

	if paneRes.AppVerified {
		t.Fatalf("synthetic agent dispatch unexpectedly marked app-verified; dispatch is not app-verified recovery")
	}

	assertRecordedSyntheticArgv(t, recordOut, []string{"resume", "synthetic-session-987"})

	restoredSession, err := target.FindSession(ctx, "synthetic-agent-source")
	if err != nil {
		t.Fatalf("find restored session: %v", err)
	}

	cfg.OperationID = restored.OperationID

	assertCompletedPracticalRetryRejected(t, ctx, repo, target, cfg)
	assertSinglePracticalPane(t, ctx, target, restoredSession)
	assertPracticalOperationSettled(t, ctx, repo, restored.OperationID)
	assertSourcePinReleased(t, ctx, repo, saved.Generation)
}

func startSyntheticAgentSession(
	t *testing.T,
	ctx context.Context,
	source *gotmux.Server,
	nodePath, scriptPath string,
) (gotmux.Session, gotmux.PaneInfo) {
	t.Helper()

	sourceSession, err := source.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "synthetic-agent-source",
		Program: gotmux.Exec(nodePath, scriptPath),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("start source session: %v", err)
	}

	disableSourceWindowRename(t, ctx, sourceSession)
	waitForPracticalForeground(t, ctx, sourceSession, "node")

	windows, _, err := sourceSession.WindowInfos(ctx)
	if err != nil || len(windows) != 1 {
		t.Fatalf("read source windows: %v", err)
	}

	activePane, err := windows[0].Handle().ActivePane(ctx)
	if err != nil {
		t.Fatalf("read active pane: %v", err)
	}

	paneInfo, err := activePane.Info(ctx)
	if err != nil {
		t.Fatalf("read pane info: %v", err)
	}

	return sourceSession, paneInfo
}

func assertRecordedSyntheticArgv(t *testing.T, path string, want []string) {
	t.Helper()

	waitForFile(t, path)

	recordedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}

	recordedLines := strings.Split(strings.TrimSpace(string(recordedBytes)), "\n")
	if !slices.Equal(recordedLines, want) {
		t.Fatalf("dispatched exact argv = %#v, want %#v", recordedLines, want)
	}
}

func TestEnginePracticalRestorePaneOutcomesEvidence(t *testing.T) {
	shPath := requireFixtureExecutable(t, "bash")
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	source := tmuxtest.NewServer(t)
	target := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	sourceSession := setupOutcomesSourceSession(t, ctx, source, shPath)
	repo := openFixture(t, ctx, filepath.Join(tempDir, "repo"))
	leaseRoot := filepath.Join(tempDir, "leases")

	saved := savePracticalObservation(t, ctx, repo, source, sourceSession, engine.SaveConfig{
		LeaseRoot:         leaseRoot,
		ProcessEnrichment: true,
		Profile:           engine.RecoveryPractical,
	})

	manifest, err := repo.Read(ctx, saved.Generation)
	if err != nil {
		t.Fatalf("read saved manifest: %v", err)
	}

	if len(manifest.Panes) != 3 {
		t.Fatalf("panes count = %d, want 3", len(manifest.Panes))
	}

	missingDir := filepath.Join(tempDir, "nonexistent_dir")
	manifest.Panes[0].Directory = model.Bytes(missingDir)
	manifest.Panes[0].Launch.Directory = model.Bytes(missingDir)
	manifest.Panes[0].Launch.Recovery = "unverified"
	manifest.Panes[0].Launch.Executable = nil
	manifest.Panes[0].Launch.Arguments = nil

	manifest.Panes[1].Launch.Recovery = "unverified"
	manifest.Panes[1].Launch.Executable = model.Bytes(shPath)
	manifest.Panes[1].Launch.Arguments = []model.Bytes{model.Bytes("bash"), model.Bytes("-c"), model.Bytes("echo 1 | cat")}

	manifest.Panes[2].Launch.Recovery = "restart"
	manifest.Panes[2].Launch.Directory = model.Bytes(tempDir)
	manifest.Panes[2].Launch.Executable = model.Bytes("/nonexistent/bin/missing_binary")
	manifest.Panes[2].Launch.Arguments = []model.Bytes{model.Bytes("missing_binary")}

	attempt, err := repo.Begin(ctx)
	if err != nil {
		t.Fatalf("register attempt: %v", err)
	}

	manifest.Artifacts = nil
	for i := range manifest.Panes {
		manifest.Panes[i].Terminal.Artifact = ""
	}

	gen, err := repo.Commit(ctx, attempt.ID, manifest, nil)
	if err != nil {
		t.Fatalf("commit mutated manifest: %v", err)
	}

	cfg := engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        gen.ID,
		LaunchProcesses:   true,
		MissingWorkingDir: restore.MissingWorkingDirLaunch,
		Profile:           engine.RecoveryPractical,
	}
	restored := restorePracticalSnapshot(t, ctx, repo, target, cfg)

	assertPracticalOperationSettled(t, ctx, repo, restored.OperationID)

	if len(restored.Panes) != 3 {
		t.Fatalf("restored panes count = %d, want 3", len(restored.Panes))
	}

	assertExpectedPaneOutcomes(t, restored.Panes, manifest.Panes[0].ID, manifest.Panes[1].ID, manifest.Panes[2].ID)
}

func setupOutcomesSourceSession(
	t *testing.T,
	ctx context.Context,
	source *gotmux.Server,
	shPath string,
) gotmux.Session {
	t.Helper()

	sourceSession, err := source.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "outcomes-source",
		Program: gotmux.Exec(shPath),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	disableSourceWindowRename(t, ctx, sourceSession)

	windows, _, err := sourceSession.WindowInfos(ctx)
	if err != nil || len(windows) != 1 {
		t.Fatalf("read windows: %v", err)
	}

	win := windows[0].Handle()

	p0, err := win.ActivePane(ctx)
	if err != nil {
		t.Fatalf("active pane: %v", err)
	}

	p1, err := p0.Split(ctx, gotmux.SplitOptions{
		Direction: gotmux.DirectionHorizontal,
		Program:   gotmux.Exec(shPath, "-c", "sleep 300 | cat"),
	})
	if err != nil {
		t.Fatalf("split p1: %v", err)
	}

	_, err = p1.Split(ctx, gotmux.SplitOptions{
		Direction: gotmux.DirectionVertical,
		Program:   gotmux.Exec(shPath, "-c", "sleep 300"),
	})
	if err != nil {
		t.Fatalf("split p2: %v", err)
	}

	return sourceSession
}

func assertExpectedPaneOutcomes(
	t *testing.T,
	panes []restore.PaneResult,
	p0ID, p1ID, p2ID string,
) {
	t.Helper()

	outcomes := map[string]restore.PaneResult{}
	for _, p := range panes {
		outcomes[p.PaneID] = p
	}

	if p, ok := outcomes[p0ID]; !ok || p.Outcome != restore.PaneOutcomeAllowedCWDFallback || !p.CWDFallback || p.AppVerified {
		t.Errorf("pane 0 = %+v, want allowed_cwd_fallback with CWDFallback true", p)
	}

	if p, ok := outcomes[p1ID]; !ok || p.Outcome != restore.PaneOutcomeUnsupported || p.AppVerified {
		t.Errorf("pane 1 = %+v, want unsupported", p)
	}

	if p, ok := outcomes[p2ID]; !ok || p.Outcome != restore.PaneOutcomeMissingExecutable || p.AppVerified {
		t.Errorf("pane 2 = %+v, want missing_executable", p)
	}
}

func assertNvimReopenedFiles(t *testing.T, ctx context.Context, session gotmux.Session, observed *process.Observation, expectedFiles ...string) {
	t.Helper()

	restoredArgs := make([]string, 0, len(observed.Arguments))
	for _, raw := range observed.Arguments {
		restoredArgs = append(restoredArgs, string(raw))
	}

	for _, expected := range expectedFiles {
		cleanExp := filepath.Clean(expected)
		found := false

		for _, arg := range restoredArgs {
			if filepath.Clean(arg) == cleanExp {
				found = true

				break
			}
		}

		if !found {
			t.Fatalf("restored nvim argv = %v, missing expected file %s", restoredArgs, cleanExp)
		}
	}

	windows, _, err := session.WindowInfos(ctx)
	if err != nil || len(windows) == 0 {
		t.Fatalf("read session windows: %v", err)
	}

	activePane, err := windows[0].Handle().ActivePane(ctx)
	if err != nil {
		t.Fatalf("read active pane: %v", err)
	}

	captured, err := activePane.Capture(ctx, gotmux.CaptureOptions{})
	if err != nil {
		t.Fatalf("capture restored pane text: %v", err)
	}

	firstBase := filepath.Base(expectedFiles[0])
	if !strings.Contains(string(captured), firstBase) {
		t.Fatalf("restored pane screen text does not contain %s:\n%s", firstBase, string(captured))
	}
}

func TestEnginePracticalRestoreMixedHealthyAndNonrestartablePanes(t *testing.T) {
	shPath := requireFixtureExecutable(t, "bash")
	topPath := requirePracticalTop(t)
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	source := tmuxtest.NewServer(t)
	target := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	sourceSession := setupMixedSourceSession(t, ctx, source, topPath, shPath)
	repo := openFixture(t, ctx, filepath.Join(tempDir, "repo"))
	leaseRoot := filepath.Join(tempDir, "leases")

	saved := savePracticalObservation(t, ctx, repo, source, sourceSession, engine.SaveConfig{
		LeaseRoot:         leaseRoot,
		ProcessEnrichment: true,
		Profile:           engine.RecoveryPractical,
	})

	manifest, err := repo.Read(ctx, saved.Generation)
	if err != nil {
		t.Fatalf("read saved manifest: %v", err)
	}

	if len(manifest.Panes) != 3 {
		t.Fatalf("panes count = %d, want 3", len(manifest.Panes))
	}

	manifest.Panes[1].Dead = true
	manifest.Panes[1].Directory = nil
	manifest.Panes[1].Launch.Recovery = model.RecoveryNonrestartable
	manifest.Panes[1].Launch.Reason = string(model.ReasonPaneDead)
	manifest.Panes[1].Launch.Executable = nil
	manifest.Panes[1].Launch.Arguments = nil

	missingDir := filepath.Join(tempDir, "deleted_working_dir")
	manifest.Panes[2].Dead = false
	manifest.Panes[2].Directory = model.Bytes(missingDir)
	manifest.Panes[2].Launch.Directory = model.Bytes(missingDir)
	manifest.Panes[2].Launch.Recovery = model.RecoveryNonrestartable
	manifest.Panes[2].Launch.Reason = string(model.ReasonWorkingDirectoryDeleted)
	manifest.Panes[2].Launch.Executable = model.Bytes(shPath)
	manifest.Panes[2].Launch.Arguments = []model.Bytes{model.Bytes("sh")}

	manifest.Artifacts = nil
	for i := range manifest.Panes {
		manifest.Panes[i].Terminal.Artifact = ""
	}

	attempt, err := repo.Begin(ctx)
	if err != nil {
		t.Fatalf("register attempt: %v", err)
	}

	gen, err := repo.Commit(ctx, attempt.ID, manifest, nil)
	if err != nil {
		t.Fatalf("commit mutated manifest: %v", err)
	}

	cfg := engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        gen.ID,
		LaunchProcesses:   true,
		MissingWorkingDir: restore.MissingWorkingDirFail,
		Profile:           engine.RecoveryPractical,
	}
	restored := restorePracticalSnapshot(t, ctx, repo, target, cfg)

	assertPracticalOperationSettled(t, ctx, repo, restored.OperationID)

	if len(restored.Panes) != 3 {
		t.Fatalf("restored panes count = %d, want 3", len(restored.Panes))
	}

	assertMixedPanesOutcomes(t, restored.Panes, manifest.Panes[0].ID, manifest.Panes[1].ID, manifest.Panes[2].ID)

	restoredSession, err := target.FindSession(ctx, "mixed-source")
	if err != nil {
		t.Fatalf("find restored session: %v", err)
	}

	waitForPracticalTop(t, ctx, restoredSession)

	assertLiveMissingDirectoryFails(t, ctx, repo, manifest, missingDir, leaseRoot)
}

func setupMixedSourceSession(
	t *testing.T,
	ctx context.Context,
	source *gotmux.Server,
	topPath, shPath string,
) gotmux.Session {
	t.Helper()

	sourceSession, err := source.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "mixed-source",
		Program: gotmux.Exec(topPath, "-d", "100"),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	disableSourceWindowRename(t, ctx, sourceSession)

	windows, _, err := sourceSession.WindowInfos(ctx)
	if err != nil || len(windows) != 1 {
		t.Fatalf("read windows: %v", err)
	}

	win := windows[0].Handle()

	p0, err := win.ActivePane(ctx)
	if err != nil {
		t.Fatalf("active pane: %v", err)
	}

	p1, err := p0.Split(ctx, gotmux.SplitOptions{
		Direction: gotmux.DirectionHorizontal,
		Program:   gotmux.Exec(shPath),
	})
	if err != nil {
		t.Fatalf("split p1: %v", err)
	}

	_, err = p1.Split(ctx, gotmux.SplitOptions{
		Direction: gotmux.DirectionVertical,
		Program:   gotmux.Exec(shPath, "-c", "sleep 300"),
	})
	if err != nil {
		t.Fatalf("split p2: %v", err)
	}

	return sourceSession
}

func assertMixedPanesOutcomes(
	t *testing.T,
	panes []restore.PaneResult,
	p0ID, p1ID, p2ID string,
) {
	t.Helper()

	outcomes := map[string]restore.PaneResult{}
	for _, p := range panes {
		outcomes[p.PaneID] = p
	}

	if p, ok := outcomes[p0ID]; !ok || p.Outcome != restore.PaneOutcomeRestartDispatch {
		t.Errorf("healthy pane outcome = %+v, want restart_dispatch", p)
	}

	if p, ok := outcomes[p1ID]; !ok || p.Outcome != restore.PaneOutcomeUnsupported || p.Reason != string(model.ReasonPaneDead) {
		t.Errorf("dead pane outcome = %+v, want unsupported/%s", p, string(model.ReasonPaneDead))
	}

	if p, ok := outcomes[p2ID]; !ok || p.Outcome != restore.PaneOutcomeUnsupported || p.Reason != string(model.ReasonWorkingDirectoryDeleted) {
		t.Errorf("deleted cwd pane outcome = %+v, want unsupported/%s", p, string(model.ReasonWorkingDirectoryDeleted))
	}
}

func assertLiveMissingDirectoryFails(
	t *testing.T,
	ctx context.Context,
	repo *store.Repository,
	manifest model.Manifest,
	missingDir, leaseRoot string,
) {
	t.Helper()

	manifest.Panes[0].Directory = model.Bytes(missingDir)
	manifest.Panes[0].Launch.Directory = model.Bytes(missingDir)
	manifest.Panes[0].Launch.Recovery = model.RecoveryRestart

	attempt2, err := repo.Begin(ctx)
	if err != nil {
		t.Fatalf("register attempt 2: %v", err)
	}

	genLiveMissingDir, err := repo.Commit(ctx, attempt2.ID, manifest, nil)
	if err != nil {
		t.Fatalf("commit live missing dir manifest: %v", err)
	}

	cfgLiveMissing := engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        genLiveMissingDir.ID,
		LaunchProcesses:   true,
		MissingWorkingDir: restore.MissingWorkingDirFail,
		Profile:           engine.RecoveryPractical,
	}

	target2 := tmuxtest.NewServer(t)

	_, err = engine.Restore(ctx, repo, target2, cfgLiveMissing)
	if err == nil {
		t.Fatal("expected live pane with missing directory to fail restore under the fail policy")
	}

	if !errors.Is(err, restore.ErrWorkingDirNotFound) {
		t.Fatalf("expected ErrWorkingDirNotFound for live pane, got %v", err)
	}
}

func TestEnginePracticalRestoreStartsAShellForAPaneWhoseDirectoryIsGone(t *testing.T) {
	shPath := requireFixtureExecutable(t, "sh")
	topPath := requirePracticalTop(t)
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	source := tmuxtest.NewServer(t)
	target := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()

	sourceSession := startTopAndShellSession(t, ctx, source, topPath, shPath)
	repo := openFixture(t, ctx, filepath.Join(tempDir, "repo"))
	leaseRoot := filepath.Join(tempDir, "leases")

	saved := savePracticalObservation(t, ctx, repo, source, sourceSession, engine.SaveConfig{
		LeaseRoot:         leaseRoot,
		ProcessEnrichment: true,
		Profile:           engine.RecoveryPractical,
	})

	generation, manifest := commitWithRemovedDirectory(t, ctx, repo, saved.Generation, filepath.Join(tempDir, "removed-worktree"))

	restored := restorePracticalSnapshot(t, ctx, repo, target, engine.RestoreConfig{
		LeaseRoot:         leaseRoot,
		Generation:        generation,
		LaunchProcesses:   true,
		MissingWorkingDir: restore.MissingWorkingDirShell,
		Profile:           engine.RecoveryPractical,
	})

	assertPracticalOperationSettled(t, ctx, repo, restored.OperationID)
	assertRemovedDirectoryOutcomes(t, restored, manifest.Panes[0].ID, manifest.Panes[1].ID)
}

func startTopAndShellSession(t *testing.T, ctx context.Context, source *gotmux.Server, topPath, shPath string) gotmux.Session {
	t.Helper()

	session, err := source.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "missing-dir-source",
		Program: gotmux.Exec(topPath, "-d", "100"),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	disableSourceWindowRename(t, ctx, session)

	windows, _, err := session.WindowInfos(ctx)
	if err != nil || len(windows) != 1 {
		t.Fatalf("read windows: %v", err)
	}

	topPane, err := windows[0].Handle().ActivePane(ctx)
	if err != nil {
		t.Fatalf("active pane: %v", err)
	}

	if _, err := topPane.Split(ctx, gotmux.SplitOptions{Direction: gotmux.DirectionHorizontal, Program: gotmux.Exec(shPath)}); err != nil {
		t.Fatalf("split shell pane: %v", err)
	}

	waitForPracticalTop(t, ctx, session)

	return session
}

// commitWithRemovedDirectory commits a copy of a saved generation whose first pane works in a
// directory that no longer exists.
func commitWithRemovedDirectory(t *testing.T, ctx context.Context, repo *store.Repository, saved, removed string) (string, model.Manifest) {
	t.Helper()

	manifest, err := repo.Read(ctx, saved)
	if err != nil {
		t.Fatalf("read saved manifest: %v", err)
	}

	if len(manifest.Panes) != 2 || manifest.Panes[0].Launch.Recovery != model.RecoveryRestart || manifest.Panes[1].Launch.Recovery != model.RecoveryRestart {
		t.Fatalf("saved panes = %+v, want two restartable panes", manifest.Panes)
	}

	manifest.Panes[0].Directory = model.Bytes(removed)
	manifest.Panes[0].Launch.Directory = model.Bytes(removed)

	manifest.Artifacts = nil
	for i := range manifest.Panes {
		manifest.Panes[i].Terminal.Artifact = ""
	}

	attempt, err := repo.Begin(ctx)
	if err != nil {
		t.Fatalf("register attempt: %v", err)
	}

	generation, err := repo.Commit(ctx, attempt.ID, manifest, nil)
	if err != nil {
		t.Fatalf("commit manifest with a removed directory: %v", err)
	}

	return generation.ID, manifest
}

func assertRemovedDirectoryOutcomes(t *testing.T, restored engine.RestoreResult, removedPaneID, healthyPaneID string) {
	t.Helper()

	outcomes := map[string]restore.PaneResult{}
	for _, pane := range restored.Panes {
		outcomes[pane.PaneID] = pane
	}

	gone := outcomes[removedPaneID]
	if gone.Outcome != restore.PaneOutcomeAllowedCWDFallback || gone.Reason != restore.PaneReasonWorkingDirFallback || !gone.CWDFallback {
		t.Errorf("pane with the removed directory = %+v, want a shell in the fallback directory", gone)
	}

	if healthy := outcomes[healthyPaneID]; healthy.Outcome != restore.PaneOutcomeRestartDispatch {
		t.Errorf("pane with its directory = %+v, want restart_dispatch", healthy)
	}

	note := restore.MissingRequirement{Domain: "directory", Status: "partial", Reason: model.ReasonSavedWorkingDirectoryMissing}
	if !slices.Contains(restored.MissingRequirements, note) {
		t.Errorf("missing requirements %v lack the removed directory note", restored.MissingRequirements)
	}
}
