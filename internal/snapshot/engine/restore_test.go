//go:build linux || darwin

package engine_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestRestoreAdmissionChecks(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	repoDir := filepath.Join(t.TempDir(), "repo")

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = repo.Close()
	}()

	dummySocket := filepath.Join(t.TempDir(), "dummy.sock")

	server, err := gotmux.New(gotmux.Config{
		SocketPath: dummySocket,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = engine.Restore(ctx, repo, server, engine.RestoreConfig{
		LeaseRoot:              "",
		Generation:             "",
		Sessions:               nil,
		LaunchProcesses:        false,
		AllowMissingWorkingDir: false,
	})
	if err == nil {
		t.Fatal("expected error on missing lease root, got nil")
	}

	if !errors.Is(err, engine.ErrLeaseRootRequired) {
		t.Fatalf("expected ErrLeaseRootRequired, got %v", err)
	}

	_, err = engine.Restore(ctx, repo, nil, engine.RestoreConfig{
		LeaseRoot:              t.TempDir(),
		Generation:             "",
		Sessions:               nil,
		LaunchProcesses:        false,
		AllowMissingWorkingDir: false,
	})
	if err == nil {
		t.Fatal("expected error on nil server, got nil")
	}

	if !errors.Is(err, integration.ErrEndpoint) {
		t.Fatalf("expected ErrEndpoint, got %v", err)
	}

	_, err = engine.Restore(ctx, nil, server, engine.RestoreConfig{
		LeaseRoot:              t.TempDir(),
		Generation:             "",
		Sessions:               nil,
		LaunchProcesses:        false,
		AllowMissingWorkingDir: false,
	})
	if err == nil {
		t.Fatal("expected error on nil repository, got nil")
	}

	if !errors.Is(err, engine.ErrRepositoryRequired) {
		t.Fatalf("expected ErrRepositoryRequired, got %v", err)
	}
}

func TestRestoreLoggingPrivacy(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer

	logger := zerolog.New(&logBuf)
	ctx := logger.WithContext(t.Context())

	_, err := engine.Restore(ctx, nil, nil, engine.RestoreConfig{
		LeaseRoot:              "",
		Generation:             "",
		Sessions:               nil,
		LaunchProcesses:        false,
		AllowMissingWorkingDir: false,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	logStr := logBuf.String()
	if logStr == "" {
		t.Fatal("expected log record, got empty buffer")
	}

	secretPath := "/secret/path/to/socket"
	if bytes.Contains(logBuf.Bytes(), []byte(secretPath)) {
		t.Fatal("log leaked sensitive path")
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatal("expected oops error")
	}

	if oopsErr.Code() != "endpoint_required" {
		t.Fatalf("expected code endpoint_required, got %q", oopsErr.Code())
	}
}
