package cli

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestSessionLoggingOutcomeOwnership(t *testing.T) {
	cfg, logPath := runtimeConfig(t, "info")

	var stdout, stderr bytes.Buffer

	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")

	err := ExecuteApplication(t.Context(), []string{"session", "list", testSocketFlag, sockPath, testConfigFlag, cfg, testJSONFlag}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("list on empty server returned error: %v", err)
	}

	assertRuntimeLog(t, logPath, "cli", "list", "info", "ok")
}
