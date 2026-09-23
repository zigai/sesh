package cli

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestSessionLoggingOutcomeOwnership(t *testing.T) {
	cfg, logPath := runtimeConfig(t, "info")

	var stdout, stderr bytes.Buffer

	err := executeApplication(t.Context(), []string{"list", testConfigFlag, cfg, testJSONFlag}, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")

	stdout.Reset()
	stderr.Reset()

	err = executeApplication(t.Context(), []string{"list", testSocketFlag, sockPath, testConfigFlag, cfg, testJSONFlag}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("list on empty server returned error: %v", err)
	}

	assertRuntimeLog(t, logPath, "cli", "list", "info", "ok", sockPath, cfg)
}
