//go:build (linux || darwin) && integration

package restore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func newIntegrationManifest() model.Manifest {
	return model.Manifest{
		Format:   "sesh",
		Version:  model.Schema,
		Started:  1000,
		Finished: 1001,
		Source: model.Source{
			TmuxVersion:    "3.6",
			LibraryVersion: "0.3.0",
			Consistency:    "checkpoint",
			Options:        nil,
			Environment:    nil,
		},
		Evidence: []model.Evidence{
			{Domain: "S01", Status: "verified", Reason: ""},
			{Domain: "S02", Status: "verified", Reason: ""},
			{Domain: "S03", Status: "verified", Reason: ""},
			{Domain: "S04", Status: "verified", Reason: ""},
			{Domain: "S05", Status: "verified", Reason: ""},
			{Domain: "S06", Status: "verified", Reason: ""},
			{Domain: "S07", Status: "verified", Reason: ""},
			{Domain: "S08", Status: "verified", Reason: ""},
			{Domain: "S09", Status: "verified", Reason: ""},
			{Domain: "S10", Status: "verified", Reason: ""},
		},
		Sessions: []model.Session{
			{
				ID:          "s0",
				Name:        model.Bytes("alpha"),
				Directory:   model.Bytes("/tmp"),
				Group:       "",
				Options:     nil,
				Environment: nil,
			},
		},
		Groups: nil,
		Windows: []model.Window{
			{
				ID:      "w0",
				Name:    model.Bytes("win-main"),
				Width:   80,
				Height:  24,
				Layout:  model.Layout{Width: 80, Height: 24, Left: 0, Top: 0, Pane: "p0", Axis: "", Children: nil},
				Zoomed:  false,
				Options: nil,
			},
		},
		Links: []model.Link{
			{Session: "s0", Window: "w0", Index: 1, Active: true, Last: false, Flags: nil},
		},
		Panes: []model.Pane{
			{
				ID:        "p0",
				Window:    "w0",
				Index:     0,
				Title:     model.Bytes("shell"),
				Directory: model.Bytes("/tmp"),
				Width:     80,
				Height:    24,
				Left:      0,
				Top:       0,
				Active:    true,
				Dead:      false,
				Options:   nil,
				Terminal:  model.Terminal{Artifact: "", History: 0, Alternate: false, CursorX: 0, CursorY: 0, Mode: nil},
				Launch:    model.Launch{Executable: nil, Arguments: nil, Directory: nil, Environment: nil, Birth: nil, Recovery: "unverified"},
			},
		},
		Artifacts: nil,
	}
}

func TestExecutor_ExecuteCompleteSession(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	manifest := newIntegrationManifest()

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		oopsErr, _ := oops.AsOops(err)
		t.Fatalf("BuildPlan failed: %+v, context: %v", err, oopsErr.Context())
	}

	cfg := DefaultExecuteConfig()
	cfg.LaunchProcesses = false

	executor := NewExecutor(server, manifest, cfg)

	report, err := executor.Execute(ctx, plan, newMemoryJournal())
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(report.SessionsCreated) != 1 {
		t.Fatalf("expected 1 session created, got %d", len(report.SessionsCreated))
	}

	sess, err := server.FindSession(ctx, "alpha")
	if err != nil {
		t.Fatalf("FindSession alpha failed: %v", err)
	}

	info, err := sess.Info(ctx)
	if err != nil {
		t.Fatalf("sess.Info failed: %v", err)
	}

	if info.Name != "alpha" {
		t.Fatalf("expected session name alpha, got %q", info.Name)
	}
}

func TestExecutor_PreflightSessionCollision(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	preExisting, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:  "alpha",
		Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("pre-creating session failed: %v", err)
	}

	manifest := newIntegrationManifest()

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	executor := NewExecutor(server, manifest, DefaultExecuteConfig())

	_, err = executor.Execute(ctx, plan, newMemoryJournal())
	if err == nil {
		t.Fatal("expected session collision error, got nil")
	}

	if !errors.Is(err, ErrSessionCollision) {
		t.Fatalf("expected ErrSessionCollision, got %v", err)
	}

	found, err := server.FindSession(ctx, "alpha")
	if err != nil {
		t.Fatalf("pre-existing session should still exist: %v", err)
	}

	if found.ID() != preExisting.ID() {
		t.Fatalf("session ID changed: %s vs %s", found.ID(), preExisting.ID())
	}
}

func TestExecutor_PreflightGroupCollision(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	leader, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:  "grp-shared",
		Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("pre-creating group leader failed: %v", err)
	}

	follower, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:  "live-follower",
		Group: "grp-shared",
		Start: gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("pre-creating group follower failed: %v", err)
	}

	if err := leader.Rename(ctx, "live-leader"); err != nil {
		t.Fatalf("renaming group leader failed: %v", err)
	}

	manifest := newIntegrationManifest()
	manifest.Groups = []model.Group{
		{
			ID:      "g0",
			Name:    model.Bytes("grp-shared"),
			Members: []string{"s0"},
		},
	}
	manifest.Sessions[0].Group = "g0"

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	executor := NewExecutor(server, manifest, DefaultExecuteConfig())

	_, err = executor.Execute(ctx, plan, newMemoryJournal())
	if err == nil {
		t.Fatal("expected group collision error, got nil")
	}

	if !errors.Is(err, ErrGroupCollision) {
		t.Fatalf("expected ErrGroupCollision, got %v", err)
	}

	foundLeader, err := server.FindSession(ctx, "live-leader")
	if err != nil {
		t.Fatalf("pre-existing leader session should still exist: %v", err)
	}

	if foundLeader.ID() != leader.ID() {
		t.Fatalf("leader session ID changed: %s vs %s", foundLeader.ID(), leader.ID())
	}

	foundFollower, err := server.FindSession(ctx, "live-follower")
	if err != nil {
		t.Fatalf("pre-existing follower session should still exist: %v", err)
	}

	if foundFollower.ID() != follower.ID() {
		t.Fatalf("follower session ID changed: %s vs %s", foundFollower.ID(), follower.ID())
	}
}

func TestExecutor_MissingWorkingDirPolicy(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	manifest := newIntegrationManifest()
	nonExistentDir := filepath.Join(t.TempDir(), "does-not-exist")
	manifest.Panes[0].Directory = model.Bytes(nonExistentDir)

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	cfgFailClosed := DefaultExecuteConfig()
	cfgFailClosed.AllowMissingWorkingDir = false
	cfgFailClosed.LaunchProcesses = false

	executor := NewExecutor(server, manifest, cfgFailClosed)

	_, err = executor.Execute(ctx, plan, newMemoryJournal())
	if err == nil {
		t.Fatal("expected ErrWorkingDirNotFound, got nil")
	}

	if !errors.Is(err, ErrWorkingDirNotFound) {
		t.Fatalf("expected ErrWorkingDirNotFound, got %v", err)
	}

	cfgAllow := DefaultExecuteConfig()
	cfgAllow.AllowMissingWorkingDir = true
	cfgAllow.LaunchProcesses = false

	allowServer := tmuxtest.NewServer(t)
	executorAllow := NewExecutor(allowServer, manifest, cfgAllow)

	report, err := executorAllow.Execute(ctx, plan, newMemoryJournal())
	if err != nil {
		t.Fatalf("expected success with AllowMissingWorkingDir=true, got %v", err)
	}

	if len(report.SessionsCreated) != 1 {
		t.Fatalf("expected 1 session created, got %d", len(report.SessionsCreated))
	}
}

func TestExecutor_FailureRetainsCreatedSession(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	manifest := newIntegrationManifest()

	plan, err := BuildPlan(manifest, []string{"s0"})
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	plan.Steps = append(plan.Steps, &ApplyLayoutStep{
		stepBase: newStep("invalid-step", []string{plan.Steps[0].ID()}),
		WindowID: "nonexistent-window-id",
		Layout:   model.Layout{Width: 10, Height: 10, Pane: "p0"},
	})

	cfg := DefaultExecuteConfig()
	cfg.LaunchProcesses = false

	executor := NewExecutor(server, manifest, cfg)

	_, err = executor.Execute(ctx, plan, newMemoryJournal())
	if err == nil {
		t.Fatal("expected execution failure, got nil")
	}

	_, findErr := server.FindSession(ctx, "alpha")
	if findErr != nil {
		t.Fatalf("expected session alpha to be retained, got %v", findErr)
	}
}
