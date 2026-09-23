//go:build integration && linux

package tmux

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
)

func TestObserveDaemonIdentityRejectsReplacementAtSameSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	dir := t.TempDir()
	socket := filepath.Join(dir, "daemon.sock")
	config := gotmux.Config{
		SocketPath: socket,
		ConfigFile: "/dev/null",
		Env:        []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "SHELL=/bin/sh", "TERM=xterm-256color", "LC_ALL=C.UTF-8"},
		Dir:        dir,
	}

	first := newStartedDaemon(t, ctx, config, "first")
	t.Cleanup(func() { _ = first.Kill(context.Background()) })

	firstIdentity, err := ObserveDaemonIdentity(ctx, first)
	if err != nil {
		t.Fatalf("observe first daemon identity: %v", err)
	}

	repeatedIdentity, err := ObserveDaemonIdentity(ctx, first)
	if err != nil {
		t.Fatalf("repeat first daemon identity: %v", err)
	}

	if !firstIdentity.Equal(repeatedIdentity) {
		t.Fatalf("stable daemon identity changed: first=%+v repeated=%+v", firstIdentity, repeatedIdentity)
	}

	if err := first.Kill(ctx); err != nil {
		t.Fatalf("kill first daemon: %v", err)
	}

	second := newStartedDaemon(t, ctx, config, "second")
	t.Cleanup(func() { _ = second.Kill(context.Background()) })

	secondIdentity, err := ObserveDaemonIdentity(ctx, second)
	if err != nil {
		t.Fatalf("observe replacement daemon identity: %v", err)
	}

	if firstIdentity.Equal(secondIdentity) {
		t.Fatalf("replacement at same socket retained old identity: first=%+v second=%+v", firstIdentity, secondIdentity)
	}
}

func newStartedDaemon(t *testing.T, ctx context.Context, config gotmux.Config, sessionName string) *gotmux.Server {
	t.Helper()

	server, err := gotmux.New(config)
	if err != nil {
		t.Fatalf("create gotmux server: %v", err)
	}

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    sessionName,
		Program: gotmux.Exec("sleep", "300"),
		Start:   gotmux.AllowStart,
	}); err != nil {
		t.Fatalf("start disposable tmux daemon: %v", err)
	}

	return server
}
