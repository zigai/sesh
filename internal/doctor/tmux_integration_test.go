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

	results, err := doctor.Run(ctx, doctor.Options{PathEnv: os.Getenv("PATH"), Server: server})
	if err != nil {
		t.Fatalf("unexpected doctor run error: %v", err)
	}

	tmuxRes := findCheck(results, doctor.CheckTmuxServer)
	if tmuxRes.Status != doctor.StatusOK || tmuxRes.Reason != "" {
		t.Fatalf("expected ok status for healthy live tmux probe, got %#v", tmuxRes)
	}

	if !strings.HasPrefix(tmuxRes.Message, "tmux ") || !strings.Contains(tmuxRes.Message, ", pid ") {
		t.Fatalf("expected version and pid in message, got %q", tmuxRes.Message)
	}

	if smoke := findCheck(results, doctor.CheckSmokeSessions); smoke.Status != doctor.StatusOK {
		t.Fatalf("expected the live sessions to list, got %#v", smoke)
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

	results, err := doctor.Run(ctx, doctor.Options{PathEnv: os.Getenv("PATH"), Socket: socketPath, Server: server})
	if err != nil {
		t.Fatalf("unexpected doctor run error: %v", err)
	}

	tmuxRes := findCheck(results, doctor.CheckTmuxServer)
	if tmuxRes.Status != doctor.StatusFail || tmuxRes.Reason != doctor.ReasonNotRunning {
		t.Fatalf("expected a not-running failure, got %#v", tmuxRes)
	}

	if want := "tmux -S " + socketPath + " new-session -d"; tmuxRes.Fix == nil || tmuxRes.Fix.Command != want {
		t.Fatalf("expected fix %q, got %#v", want, tmuxRes.Fix)
	}

	if smoke := findCheck(results, doctor.CheckSmokeSessions); smoke.Status != doctor.StatusSkip || smoke.Needs != doctor.CheckTmuxServer {
		t.Fatalf("the smoke test needs the server, got %#v", smoke)
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
			{Session: session.Identity{Name: "live-session"}, LastSwitchUnix: time.Now().Unix()},
		},
		RemoteSwitchHistory: nil,
		AgentViewHistory:    nil,
		LastAutosaveUnix:    0,
		PopupDisplayMode:    "",
	}
	if err := session.SaveState(ctx, historyPath, st); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	results, err := doctor.Run(ctx, doctor.Options{
		PathEnv:     os.Getenv("PATH"),
		Server:      server,
		SnapshotDir: repoDir,
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatalf("unexpected doctor run error: %v", err)
	}

	for _, checkName := range []string{
		doctor.CheckTmuxClient,
		doctor.CheckTmuxServer,
		doctor.CheckSnapshotRepository,
		doctor.CheckSnapshotCurrent,
		doctor.CheckActiveRestores,
		doctor.CheckHistoryFile,
		doctor.CheckSmokeSessions,
	} {
		res := findCheck(results, checkName)
		if res.Status != doctor.StatusOK {
			t.Fatalf("expected check %s to be ok, got %#v", checkName, res)
		}
	}

	if doctor.HasFailures(results) {
		t.Fatalf("expected no failures for a healthy system: %#v", results)
	}
}
