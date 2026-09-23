//go:build (linux || darwin) && integration

package restore

import (
	"testing"

	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func BenchmarkRestoreSessionWithPaneTitle(b *testing.B) {
	server := tmuxtest.NewServer(b)
	directory := b.TempDir()
	manifest := newIntegrationManifest()
	manifest.Sessions[0].Directory = model.Bytes(directory)
	manifest.Panes[0].Directory = model.Bytes(directory)
	manifest.Panes[0].Title = model.Bytes("restored editor")
	manifest.Panes[0].Launch = model.Launch{
		Executable: model.Bytes("/bin/sh"), Arguments: []model.Bytes{model.Bytes("sh")},
		Directory: model.Bytes(directory), Recovery: model.RecoveryRestart,
		Environment: []model.Variable{{Name: model.Bytes("HOME"), State: "set", Value: model.Bytes(directory)}},
	}

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()

	for b.Loop() {
		executor := NewExecutor(server, manifest, ExecuteConfig{LaunchProcesses: true})

		report, err := executor.Execute(b.Context(), plan, newMemoryJournal())
		if err != nil {
			b.Fatal(err)
		}

		b.StopTimer()
		checkRestoredBenchmarkPane(b, server, report)
		b.StartTimer()
	}
}

func checkRestoredBenchmarkPane(b *testing.B, server *tmux.Server, report ExecutionReport) {
	b.Helper()

	if len(report.SessionsCreated) != 1 || len(report.Panes) != 1 || report.Panes[0].Outcome != PaneOutcomeRestartDispatch {
		b.Fatalf("restore did not dispatch one pane: %+v", report)
	}

	pane, err := server.Pane(b.Context(), tmux.PaneID(report.Panes[0].RuntimeID))
	if err != nil {
		b.Fatal(err)
	}

	info, err := pane.Info(b.Context())
	if err != nil {
		b.Fatal(err)
	}

	if info.Title != "restored editor" {
		b.Fatalf("pane title = %q, want restored editor", info.Title)
	}

	restored, err := server.Session(b.Context(), tmux.SessionID(report.SessionsCreated[0]))
	if err != nil {
		b.Fatal(err)
	}

	if err := restored.Kill(b.Context()); err != nil {
		b.Fatal(err)
	}
}
