//go:build linux || darwin

package engine

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestRestoreIntentConflictsWhenKernelBirthChanges(t *testing.T) {
	endpoint := "/tmp/restore.sock"
	serverIdentity := gotmux.ServerIdentity{
		Endpoint:       gotmux.Endpoint{SocketPath: endpoint},
		ReportedSocket: endpoint,
		PID:            42,
		Started:        time.Unix(100, 0),
	}
	firstDaemon := restoreDaemonIdentity{
		Server: serverIdentity,
		Birth:  "11111111-2222-3333-4444-555555555555:1000",
	}
	secondDaemon := firstDaemon
	secondDaemon.Birth = "11111111-2222-3333-4444-555555555555:1001"

	firstTarget, err := restoreTarget(endpoint, firstDaemon, RestoreConfig{})
	if err != nil {
		t.Fatalf("encode first restore target: %v", err)
	}

	secondTarget, err := restoreTarget(endpoint, secondDaemon, RestoreConfig{})
	if err != nil {
		t.Fatalf("encode second restore target: %v", err)
	}

	repository, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "repository"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}

	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close repository: %v", err)
		}
	})

	attempt, err := repository.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin snapshot: %v", err)
	}

	generation, err := repository.Commit(t.Context(), attempt.ID, observationFixture().Manifest, nil)
	if err != nil {
		t.Fatalf("commit snapshot: %v", err)
	}

	actions := []store.Action{
		{ID: restore.ImportStateActionID, Kind: restore.KindConfigure, State: "planned"},
		{ID: restore.VerifyActionID, Kind: restore.KindVerify, State: "planned"},
	}
	operationID := store.NewOperationID()

	if _, err := repository.RegisterOperation(t.Context(), operationID, generation.ID, firstTarget, actions); err != nil {
		t.Fatalf("register first restore intent: %v", err)
	}

	if _, err := repository.RegisterOperation(t.Context(), operationID, generation.ID, secondTarget, actions); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed kernel birth error = %v, want repository conflict", err)
	}
}

func TestRestoreTargetRejectsMalformedKernelBirth(t *testing.T) {
	endpoint := "/tmp/restore.sock"
	daemon := restoreDaemonIdentity{
		Server: gotmux.ServerIdentity{
			Endpoint:       gotmux.Endpoint{SocketPath: endpoint},
			ReportedSocket: endpoint,
			PID:            42,
			Started:        time.Unix(100, 0),
		},
		Birth: "11111111-2222-3333-4444-555555555555:1\ninjected=true",
	}

	if _, err := restoreTarget(endpoint, daemon, RestoreConfig{}); !errors.Is(err, tmuxserver.ErrDaemonIdentityUnavailable) {
		t.Fatalf("malformed kernel birth error = %v, want daemon identity unavailable", err)
	}
}

func TestApplyAndVerifyRejectsZeroDaemonIdentity(t *testing.T) {
	_, _, err := applyAndVerify(
		t.Context(),
		nil,
		restoreDaemonIdentity{},
		restore.Plan{},
		nil,
		nil,
		false,
	)
	if !errors.Is(err, tmuxserver.ErrDaemonIdentityUnavailable) {
		t.Fatalf("zero daemon identity error = %v, want daemon identity unavailable", err)
	}
}
