package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

type initResultJSON struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

func TestConfigInitUsesNativeXDGPathWithoutLoadingMalformedConfig(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "config-home")
	t.Setenv("XDG_CONFIG_HOME", configHome)

	target := filepath.Join(configHome, "sesh", "config.toml")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}

	malformed := []byte("[broken\n")
	if err := os.WriteFile(target, malformed, 0o600); err != nil {
		t.Fatal(err)
	}

	exec := clitest.Execute(t, "config", "init", "--force")
	if exec.Err != nil {
		t.Fatalf("config init --force error = %v; stderr = %q", exec.Err, exec.Stderr)
	}

	wantOutput := "Initialized configuration at " + target + ".\n"
	if exec.Stdout != wantOutput || exec.Stderr != "" {
		t.Fatalf("stdout = %q, stderr = %q, want %q and empty", exec.Stdout, exec.Stderr, wantOutput)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(content, malformed) || !bytes.Contains(content, []byte("[settings]")) {
		t.Fatal("forced initialization did not install the documented template")
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("configuration mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestConfigInitDoesNotOverwriteWithoutForce(t *testing.T) {
	target := filepath.Join(t.TempDir(), "config.toml")

	original := []byte("private existing configuration")
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}

	exec := clitest.Execute(t, "config", "init", "--path", target)
	if !errors.Is(exec.Err, config.ErrConfigExists) {
		t.Fatalf("config init error = %v, want ErrConfigExists", exec.Err)
	}

	if exec.Stdout != "" {
		t.Fatalf("stdout = %q, want empty", exec.Stdout)
	}

	if strings.Contains(exec.Stderr, target) || strings.Contains(exec.Stderr, string(original)) {
		t.Fatalf("stderr leaked private path or contents: %q", exec.Stderr)
	}

	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}

	if !bytes.Equal(content, original) {
		t.Fatalf("existing configuration changed: %q", content)
	}
}

func TestConfigInitRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	root := t.TempDir()
	realPath := filepath.Join(root, "real.toml")

	privateContent := []byte("private-target-canary")
	if err := os.WriteFile(realPath, privateContent, 0o600); err != nil {
		t.Fatal(err)
	}

	symlinkPath := filepath.Join(root, "config.toml")
	if err := os.Symlink(realPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	exec := clitest.Execute(t, "config", "init", "--path", symlinkPath, "--force")
	if !errors.Is(exec.Err, config.ErrConfigSymlink) {
		t.Fatalf("config init error = %v, want ErrConfigSymlink", exec.Err)
	}

	if exec.Stdout != "" {
		t.Fatalf("stdout = %q, want empty", exec.Stdout)
	}

	if strings.Contains(exec.Stderr, symlinkPath) || strings.Contains(exec.Stderr, string(privateContent)) {
		t.Fatalf("stderr leaked private path or contents: %q", exec.Stderr)
	}

	content, readErr := os.ReadFile(realPath)
	if readErr != nil {
		t.Fatal(readErr)
	}

	if !bytes.Equal(content, privateContent) {
		t.Fatalf("symlink target changed: %q", content)
	}
}

func TestConfigInitJSONReportsResolvedPathAndStatus(t *testing.T) {
	target := filepath.Join(t.TempDir(), "nested", "config.toml")

	exec := clitest.ExecuteJSON(t, "config", "init", "--path", target, "--json")
	if exec.Err != nil {
		t.Fatalf("config init JSON error = %v; stderr = %q", exec.Err, exec.Stderr)
	}

	if exec.Stderr != "" || !exec.Envelope.OK || exec.Envelope.Command != "config.init" {
		t.Fatalf("unexpected JSON envelope/output: %+v; stderr = %q", exec.Envelope, exec.Stderr)
	}

	result := clitest.DecodePayload[initResultJSON](t, exec.Envelope.Result)
	if result.Path != target || result.Status != "initialized" {
		t.Fatalf("result = %+v, want path %q and initialized", result, target)
	}

	var raw map[string]any
	if err := json.Unmarshal(exec.Envelope.Result, &raw); err != nil {
		t.Fatal(err)
	}

	if _, present := raw["replaced"]; present {
		t.Fatal("JSON result makes a racy replacement claim")
	}
}

func TestConfigInitInvalidFlagAndHelpDoNotMutateDisk(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "invalid flag", args: []string{"config", "init", "--unknown"}},
		{name: "help", args: []string{"config", "init", "--help"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			configHome := filepath.Join(t.TempDir(), "config-home")
			t.Setenv("XDG_CONFIG_HOME", configHome)

			_ = clitest.Execute(t, test.args...)

			target := filepath.Join(configHome, "sesh", "config.toml")
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("parse/help path created configuration: %v", err)
			}
		})
	}
}
