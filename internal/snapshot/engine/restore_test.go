//go:build linux || darwin

package engine_test

import (
	"bytes"
	"testing"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/snapshot/restore"
)

func TestRestoreLoggingPrivacy(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer

	logger := zerolog.New(&logBuf)
	ctx := logger.WithContext(t.Context())

	_, err := engine.Restore(ctx, nil, nil, engine.RestoreConfig{
		LeaseRoot:         "",
		Generation:        "",
		Sessions:          nil,
		LaunchProcesses:   false,
		MissingWorkingDir: restore.MissingWorkingDirFail,
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
