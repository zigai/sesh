//go:build linux || darwin

package cmdremote

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/samber/oops"
)

func TestRunRemoteLookupMissingTargetErrorWrapping(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "config")
	cacheHome := filepath.Join(t.TempDir(), "cache")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	configDir := filepath.Join(configHome, "sesh")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}

	configContent := `[[remote.connections]]
id = "workstation"
host = "10.0.0.1"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newRemoteLookupCommand()
	cmd.SetContext(t.Context())

	err := runRemoteLookup(cmd, []string{"workstation", "inv-12345"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, errRemoteTargetMissing) {
		t.Fatalf("expected errRemoteTargetMissing, got %v", err)
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected oops error, got %T", err)
	}

	if oopsErr.Domain() != "cli" {
		t.Fatalf("domain = %q, want cli", oopsErr.Domain())
	}

	if oopsErr.Context()["operation"] != "resolve_remote_target" {
		t.Fatalf("operation = %v, want resolve_remote_target", oopsErr.Context()["operation"])
	}
}
