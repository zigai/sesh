//go:build (linux || darwin) && integration

package doctor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/doctor"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestTmuxLiveProbe_HealthyServer(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	results, err := doctor.Run(ctx, doctor.Options{
		Server:      server,
		SnapshotDir: "",
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected doctor run error: %v", err)
	}

	tmuxRes := findCheck(results, doctor.CheckTmux)
	if tmuxRes.Status != doctor.StatusOK || tmuxRes.Reason != "" {
		t.Fatalf("expected ok status for healthy live tmux probe, got %#v", tmuxRes)
	}

	if !strings.Contains(tmuxRes.Message, "tmux ") || !strings.Contains(tmuxRes.Message, "server pid") {
		t.Fatalf("expected version and pid in message, got %q", tmuxRes.Message)
	}
}

func TestTmuxProbe_NonRunningServerEndpoint(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "nonexistent.sock")

	server, err := gotmux.New(gotmux.Config{
		SocketPath: socketPath,
	})
	if err != nil {
		t.Fatalf("failed to create gotmux server: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	results, err := doctor.Run(ctx, doctor.Options{
		Server:      server,
		SnapshotDir: "",
		HistoryPath: "",
	})
	if err != nil {
		t.Fatalf("unexpected doctor run error: %v", err)
	}

	tmuxRes := findCheck(results, doctor.CheckTmux)
	if tmuxRes.Status != doctor.StatusError || tmuxRes.Reason != doctor.ReasonUnavailable {
		t.Fatalf("expected error/unavailable for non-running server, got %#v", tmuxRes)
	}

	if tmuxRes.Message != "no running tmux daemon found on selected endpoint" {
		t.Fatalf("expected no running tmux daemon message, got %q", tmuxRes.Message)
	}

	if tmuxRes.Fix != "Start tmux on the target endpoint or specify an active socket" {
		t.Fatalf("expected start tmux fix, got %q", tmuxRes.Fix)
	}

	if _, statErr := os.Stat(socketPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("health probe spawned a daemon or created socket at %s: %v", socketPath, statErr)
	}
}

func TestTmuxLiveProbe_FullSystemIntegration(t *testing.T) {
	server := tmuxtest.NewServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	repoDir := filepath.Join(t.TempDir(), "repo")

	r, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}

	commitTestFixture(t, r, true)

	if err := r.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	historyDir := testDir(t, "history")
	historyPath := filepath.Join(historyDir, "history.json")

	st := &session.State{
		Version: session.CurrentHistoryVersion,
		SwitchHistory: []session.SwitchHistoryEntry{
			{Session: session.Identity{Name: "live-session"}, LastSwitch: time.Now().Unix()},
		},
		RemoteSwitchHistory:   nil,
		AgentViewHistory:      nil,
		LastAutosaveTimestamp: 0,
		PopupDisplayMode:      "",
	}
	if err := session.SaveState(ctx, historyPath, st); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	results, err := doctor.Run(ctx, doctor.Options{
		Server:      server,
		SnapshotDir: repoDir,
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatalf("unexpected doctor run error: %v", err)
	}

	for _, checkName := range []string{
		doctor.CheckTmux,
		doctor.CheckSnapshotRepository,
		doctor.CheckSnapshotCurrent,
		doctor.CheckHistoryState,
	} {
		res := findCheck(results, checkName)
		if res.Status != doctor.StatusOK {
			t.Fatalf("expected check %s to be ok, got %#v", checkName, res)
		}
	}

	for _, checkName := range []string{
		doctor.CheckCaptureGuarantee,
		doctor.CheckRestoreCapability,
		doctor.CheckAutosave,
	} {
		res := findCheck(results, checkName)
		if res.Status != doctor.StatusWarning {
			t.Fatalf("expected capability check %s to be warning, got %#v", checkName, res)
		}
	}

	if doctor.HasErrors(results) {
		t.Fatal("expected no errors for full healthy system")
	}

	if !doctor.HasWarnings(results) {
		t.Fatal("expected warnings for capabilities in full system")
	}
}
