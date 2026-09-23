package cmdconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
)

func TestConfigInitCommandDeclaresExplicitSafeRuntimeContract(t *testing.T) {
	cmd := newConfigInitCommand()

	if cmd.Annotations[clibase.AnnotationOperation] != "config_init" {
		t.Fatalf("operation annotation = %q, want config_init", cmd.Annotations[clibase.AnnotationOperation])
	}

	if cmd.Annotations[clibase.AnnotationSkipConfigLoad] != "true" {
		t.Fatalf("skip-config-load annotation = %q, want true", cmd.Annotations[clibase.AnnotationSkipConfigLoad])
	}

	for _, flag := range []string{"path", "force", clibase.JSONFlagName} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Fatalf("missing --%s flag", flag)
		}
	}

	if !strings.Contains(cmd.Long, "inherited --config flag is not read") {
		t.Fatalf("long help does not explain --config behavior: %q", cmd.Long)
	}
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

	var stdout, stderr bytes.Buffer

	err := executeApplication(
		t.Context(),
		[]string{"config", "init", "--force"},
		nil,
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatalf("config init --force error = %v; stderr = %q", err, stderr.String())
	}

	wantOutput := "Initialized configuration at " + target + ".\n"
	if stdout.String() != wantOutput || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q, want %q and empty", stdout.String(), stderr.String(), wantOutput)
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

	var stdout, stderr bytes.Buffer

	err := executeApplication(
		t.Context(),
		[]string{"config", "init", "--path", target},
		nil,
		&stdout,
		&stderr,
	)
	if !errors.Is(err, config.ErrConfigExists) {
		t.Fatalf("config init error = %v, want ErrConfigExists", err)
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if strings.Contains(stderr.String(), target) || strings.Contains(stderr.String(), string(original)) {
		t.Fatalf("stderr leaked private path or contents: %q", stderr.String())
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

	var stdout, stderr bytes.Buffer

	err := executeApplication(
		t.Context(),
		[]string{"config", "init", "--path", symlinkPath, "--force"},
		nil,
		&stdout,
		&stderr,
	)
	if !errors.Is(err, config.ErrConfigSymlink) {
		t.Fatalf("config init error = %v, want ErrConfigSymlink", err)
	}

	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	if strings.Contains(stderr.String(), symlinkPath) || strings.Contains(stderr.String(), string(privateContent)) {
		t.Fatalf("stderr leaked private path or contents: %q", stderr.String())
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

	exec := executeJSON(t, "config", "init", "--path", target, testJSONFlag)
	if exec.err != nil {
		t.Fatalf("config init JSON error = %v; stderr = %q", exec.err, exec.stderr)
	}

	if exec.stderr != "" || !exec.envelope.OK || exec.envelope.Command != "config.init" {
		t.Fatalf("unexpected JSON envelope/output: %+v; stderr = %q", exec.envelope, exec.stderr)
	}

	result := decodeJSONPayload[configInitResultJSON](t, exec.envelope.Result)
	if result.Path != target || result.Status != "initialized" {
		t.Fatalf("result = %+v, want path %q and initialized", result, target)
	}

	var raw map[string]any
	if err := json.Unmarshal(exec.envelope.Result, &raw); err != nil {
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

			var stdout, stderr bytes.Buffer

			_ = executeApplication(t.Context(), test.args, nil, &stdout, &stderr)

			target := filepath.Join(configHome, "sesh", "config.toml")
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("parse/help path created configuration: %v", err)
			}
		})
	}
}
