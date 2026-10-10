package restore

import (
	"context"
	"os"
	"testing"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func TestDefaultExecuteConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultExecuteConfig()

	if cfg.LaunchProcesses {
		t.Error("expected process launches to be disabled by default")
	}

	if cfg.MissingWorkingDir != MissingWorkingDirFail {
		t.Errorf("default missing working directory policy = %q, want %q", cfg.MissingWorkingDir, MissingWorkingDirFail)
	}
}

func TestPreflightCheckCollisions_NilServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	plan := Plan{
		RequestedSessions:   nil,
		AddedSessions:       nil,
		ClosureSessions:     nil,
		SourceGraph:         model.Manifest{},
		Steps:               nil,
		MissingRequirements: nil,
		Summary:             PlanSummary{},
	}

	err := PreflightCheckCollisions(ctx, nil, plan)
	if err == nil {
		t.Fatal("expected error on nil server, got nil")
	}
}

func TestPaneOutcomeClassification(t *testing.T) {
	t.Parallel()

	execPath := "/bin/sh"
	if _, err := os.Stat(execPath); err != nil {
		execPath = "/usr/bin/sh"
	}

	manifest := model.Manifest{
		Panes: []model.Pane{
			{
				ID:        "p-shell",
				Directory: model.Bytes(t.TempDir()),
				Launch:    model.Launch{Recovery: "unverified"},
			},
			{
				ID:        "p-unsupported",
				Directory: model.Bytes(t.TempDir()),
				Launch: model.Launch{
					Executable: model.Bytes(execPath),
					Arguments:  []model.Bytes{model.Bytes("sh"), model.Bytes("-c"), model.Bytes("echo 1")},
					Recovery:   "unverified",
				},
			},
			{
				ID:        "p-restart",
				Directory: model.Bytes(t.TempDir()),
				Launch: model.Launch{
					Executable: model.Bytes(execPath),
					Arguments:  []model.Bytes{model.Bytes("sh")},
					Recovery:   "restart",
				},
			},
			{
				ID:        "p-resume",
				Directory: model.Bytes(t.TempDir()),
				Launch: model.Launch{
					Executable: model.Bytes(execPath),
					Arguments:  []model.Bytes{model.Bytes("sh")},
					Recovery:   "resume",
				},
			},
			{
				ID:        "p-missing-exec",
				Directory: model.Bytes(t.TempDir()),
				Launch: model.Launch{
					Executable: model.Bytes("/nonexistent/binary/path"),
					Arguments:  []model.Bytes{model.Bytes("missing")},
					Recovery:   "restart",
				},
			},
			{
				ID:        "p-cwd-fallback",
				Directory: model.Bytes("/nonexistent/dir/path"),
				Launch:    model.Launch{Recovery: "unverified"},
			},
		},
	}

	executor := NewExecutor(nil, manifest, ExecuteConfig{
		LaunchProcesses:   true,
		MissingWorkingDir: MissingWorkingDirLaunch,
	})

	for _, p := range manifest.Panes {
		launch, _ := executor.preparePaneLaunch(p, "")
		executor.recordPaneResult(p.ID, "%"+p.ID, p, launch)
	}

	plan := Plan{SourceGraph: manifest}

	results := executor.collectedPaneResults(plan)
	if len(results) != 6 {
		t.Fatalf("expected 6 results, got %d", len(results))
	}

	expected := map[string]PaneResult{
		"p-shell":        {Outcome: PaneOutcomeDefaultShell},
		"p-unsupported":  {Outcome: PaneOutcomeUnsupported, Reason: PaneReasonUnsupportedLaunch},
		"p-restart":      {Outcome: PaneOutcomeRestartDispatch},
		"p-resume":       {Outcome: PaneOutcomeNativeResumeDispatch},
		"p-missing-exec": {Outcome: PaneOutcomeMissingExecutable, Reason: PaneReasonExecutableUnavailable},
		"p-cwd-fallback": {Outcome: PaneOutcomeAllowedCWDFallback, Reason: PaneReasonWorkingDirFallback, CWDFallback: true},
	}

	for _, r := range results {
		want, ok := expected[r.PaneID]
		if !ok {
			t.Errorf("unexpected pane ID %s", r.PaneID)

			continue
		}

		if r.Outcome != want.Outcome || r.Reason != want.Reason || r.CWDFallback != want.CWDFallback {
			t.Errorf("pane %s result = %+v, want outcome=%q reason=%q cwdFallback=%t", r.PaneID, r, want.Outcome, want.Reason, want.CWDFallback)
		}

		if r.AppVerified {
			t.Errorf("pane %s unexpectedly marked AppVerified", r.PaneID)
		}
	}
}

func TestVerifiedRuntimePaneResultsDoNotInventLaunchEvidence(t *testing.T) {
	t.Parallel()

	plan := Plan{SourceGraph: model.Manifest{Panes: []model.Pane{
		{ID: "p-restart", Launch: model.Launch{Recovery: model.RecoveryRestart}},
		{ID: "p-dead", Launch: model.Launch{Recovery: model.RecoveryNonrestartable, Reason: string(model.ReasonPaneDead)}},
		{ID: "p-executable", Launch: model.Launch{Recovery: model.RecoveryNonrestartable, Reason: string(model.ReasonExecutableDeleted)}},
		{ID: "p-directory", Launch: model.Launch{Recovery: model.RecoveryNonrestartable, Reason: string(model.ReasonWorkingDirectoryDeleted)}},
	}}}
	runtimePanes := map[string]string{"p-restart": "%10", "p-dead": "%11", "p-executable": "%12", "p-directory": "%13"}

	launched := PanesFromVerifiedRuntime(plan, runtimePanes, true)
	if got := launched[0]; got.RuntimeID != "%10" || got.Outcome != PaneOutcomeEvidenceUnavailable || got.Reason != PaneReasonEvidenceUnavailable {
		t.Fatalf("reinspected restart result = %+v", got)
	}

	if got := launched[1]; got.RuntimeID != "%11" || got.Outcome != PaneOutcomeUnsupported || got.Reason != string(model.ReasonPaneDead) {
		t.Fatalf("reinspected dead-pane result = %+v", got)
	}

	for index, reason := range []string{string(model.ReasonExecutableDeleted), string(model.ReasonWorkingDirectoryDeleted)} {
		if got := launched[index+2]; got.Outcome != PaneOutcomeUnsupported || got.Reason != reason {
			t.Fatalf("reinspected nonrestartable result = %+v, want reason %q", got, reason)
		}
	}

	disabled := PanesFromVerifiedRuntime(plan, runtimePanes, false)
	if got := disabled[0]; got.Outcome != PaneOutcomeDefaultShell || got.Reason != PaneReasonProcessLaunchDisabled {
		t.Fatalf("no-processes result = %+v", got)
	}
}

func TestMissingPaneResultDoesNotRerunLaunchPreflight(t *testing.T) {
	t.Parallel()

	pane := model.Pane{
		ID: "p-restart",
		Launch: model.Launch{
			Executable: model.Bytes("/bin/sh"),
			Arguments:  []model.Bytes{model.Bytes("sh")},
			Recovery:   model.RecoveryRestart,
		},
	}
	manifest := model.Manifest{Panes: []model.Pane{pane}}
	executor := NewExecutor(nil, manifest, ExecuteConfig{LaunchProcesses: true})

	executor.ensureAllPaneResults(Plan{SourceGraph: manifest})

	got := executor.paneResults[pane.ID]
	if got.Outcome != PaneOutcomeEvidenceUnavailable || got.Reason != PaneReasonEvidenceUnavailable {
		t.Fatalf("missing pane result = %+v, want conservative unavailable evidence", got)
	}
}

func TestResolveWorkingDirPreservesAbsoluteSymlink(t *testing.T) {
	t.Parallel()

	target := t.TempDir()

	link := t.TempDir() + "/working-directory"
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create working-directory symlink: %v", err)
	}

	executor := NewExecutor(nil, model.Manifest{}, ExecuteConfig{})

	directory, fallback, err := executor.resolveWorkingDir(model.Pane{Directory: model.Bytes(link)})
	if err != nil {
		t.Fatalf("resolve symlink working directory: %v", err)
	}

	if directory != link || fallback {
		t.Fatalf("resolved working directory = %q, fallback=%t; want symlink %q without fallback", directory, fallback, link)
	}
}
