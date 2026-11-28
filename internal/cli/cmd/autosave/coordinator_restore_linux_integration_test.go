//go:build linux && integration

package autosave

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestCoordinatorDeferredRestoreRestoresOncePerServer(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "snapshots")
	leaseRoot := filepath.Join(root, "leases")
	historyPath := filepath.Join(root, "state", "history.json")

	source := tmuxtest.NewServer(t)

	work, err := source.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    "work",
		Program: gotmux.Exec("/bin/sh"),
		Start:   gotmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("create source session: %v", err)
	}

	repo, err := store.Open(t.Context(), repoPath)
	if err != nil {
		t.Fatalf("open snapshot repository: %v", err)
	}

	if _, err := engine.SaveObservation(
		t.Context(),
		repo,
		source,
		[]gotmux.SessionID{work.ID()},
		engine.SaveConfig{
			LeaseRoot:         leaseRoot,
			ProcessEnrichment: true,
			Profile:           engine.RecoveryPractical,
		},
	); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	if err := repo.Close(); err != nil {
		t.Fatalf("close snapshot repository: %v", err)
	}

	// tmux reports server start time in whole seconds.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))

	target := tmuxtest.NewServer(t)

	cmd := NewDaemonCoordinatorCommand()
	for flag, value := range map[string]string{
		"socket":     target.Endpoint().SocketPath,
		"repository": repoPath,
		"lease-root": leaseRoot,
		"history":    historyPath,
	} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			t.Fatalf("set %s flag: %v", flag, err)
		}
	}

	env, err := ResolveEnv(cmd)
	if err != nil {
		t.Fatalf("resolve autosave env: %v", err)
	}

	env.workspace.Config.Autosave.RestoreLastOnStart = true

	runCoordinatorDeferredRestore(t.Context(), env)

	restored, err := target.FindSession(t.Context(), "work")
	if err != nil {
		t.Fatalf("expected saved session to be restored on login: %v", err)
	}

	if err := restored.Kill(t.Context()); err != nil {
		t.Fatalf("kill restored session: %v", err)
	}

	// A later login event must not re-restore a server that was already targeted.
	runCoordinatorDeferredRestore(t.Context(), env)

	if _, err := target.FindSession(t.Context(), "work"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("FindSession after second login = %v, want ErrNotFound", err)
	}
}
