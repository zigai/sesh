//go:build integration

package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type configInitResultJSON struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

type configInitBinaryFixture struct {
	binaryPath string
	root       string
	home       string
	dataHome   string
	stateHome  string
	cacheHome  string
	target     string
	env        []string
}

func TestConfigInitBinaryUsesIsolatedXDGAndRepairsOnlyWithForce(t *testing.T) {
	fixture := newConfigInitBinaryFixture(t)

	verifyConfigInitCreatesNativeXDGFile(t, fixture)
	verifyConfigInitRepairsOnlyWithForce(t, fixture)
	verifyConfigInitRejectsSymlink(t, fixture)
	verifyConfigInitInvalidFlagDoesNotMutate(t, fixture)
}

func newConfigInitBinaryFixture(t *testing.T) configInitBinaryFixture {
	t.Helper()

	binaryDir := t.TempDir()
	binaryPath := filepath.Join(binaryDir, "sesh")

	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolving repository root: %v", err)
	}

	buildCmd := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-o", binaryPath, "./cmd/sesh")

	buildCmd.Dir = repoRoot
	if output, buildErr := buildCmd.CombinedOutput(); buildErr != nil {
		t.Fatalf("go build failed: %v\noutput: %s", buildErr, output)
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	configHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "data")
	stateHome := filepath.Join(root, "state")
	cacheHome := filepath.Join(root, "cache")
	target := filepath.Join(configHome, "sesh", "config.toml")

	env := isolatedConfigInitEnvironment(home, configHome, dataHome, stateHome, cacheHome)

	return configInitBinaryFixture{
		binaryPath: binaryPath,
		root:       root,
		home:       home,
		dataHome:   dataHome,
		stateHome:  stateHome,
		cacheHome:  cacheHome,
		target:     target,
		env:        env,
	}
}

func (f configInitBinaryFixture) run(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()

	return f.runWithEnvironment(t, f.env, args...)
}

func (f configInitBinaryFixture) runWithEnvironment(t *testing.T, env []string, args ...string) ([]byte, error) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), f.binaryPath, args...)
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("run config init binary: %w", err)
	}

	return output, nil
}

func verifyConfigInitCreatesNativeXDGFile(t *testing.T, fixture configInitBinaryFixture) {
	t.Helper()

	output, err := fixture.run(t, "config", "init", "--json")
	if err != nil {
		t.Fatalf("first config init failed: %v\noutput: %s", err, output)
	}

	var envelope struct {
		OK      bool                 `json:"ok"`
		Command string               `json:"command"`
		Result  configInitResultJSON `json:"result"`
	}
	if decodeErr := json.Unmarshal(output, &envelope); decodeErr != nil {
		t.Fatalf("decoding JSON output: %v\noutput: %s", decodeErr, output)
	}

	if !envelope.OK || envelope.Command != "config.init" || envelope.Result.Path != fixture.target || envelope.Result.Status != "initialized" {
		t.Fatalf("unexpected init envelope: %+v", envelope)
	}
}

func verifyConfigInitRepairsOnlyWithForce(t *testing.T, fixture configInitBinaryFixture) {
	t.Helper()

	malformed := []byte("[malformed\n")
	if writeErr := os.WriteFile(fixture.target, malformed, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	output, err := fixture.run(t, "config", "init")
	if err == nil {
		t.Fatalf("init without --force succeeded: %s", output)
	}

	content, readErr := os.ReadFile(fixture.target)
	if readErr != nil {
		t.Fatal(readErr)
	}

	if !bytes.Equal(content, malformed) {
		t.Fatalf("failed init changed existing file: %q", content)
	}

	output, err = fixture.run(t, "config", "init", "--force")
	if err != nil {
		t.Fatalf("forced repair failed: %v\noutput: %s", err, output)
	}

	content, readErr = os.ReadFile(fixture.target)
	if readErr != nil {
		t.Fatal(readErr)
	}

	if bytes.Equal(content, malformed) || !bytes.Contains(content, []byte("[settings]")) {
		t.Fatal("forced binary initialization did not repair malformed config")
	}
}

func verifyConfigInitRejectsSymlink(t *testing.T, fixture configInitBinaryFixture) {
	t.Helper()

	privateTarget := filepath.Join(fixture.root, "private-target.toml")

	privateContent := []byte("private-binary-canary")
	if writeErr := os.WriteFile(privateTarget, privateContent, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	symlinkPath := filepath.Join(fixture.root, "symlink-config.toml")
	if linkErr := os.Symlink(privateTarget, symlinkPath); linkErr != nil {
		t.Fatal(linkErr)
	}

	output, err := fixture.run(t, "config", "init", "--path", symlinkPath, "--force", "--json")
	if err == nil {
		t.Fatalf("symlink init succeeded: %s", output)
	}

	verifyConfigInitSymlinkError(t, output, symlinkPath, privateContent)
	verifyConfigInitSymlinkTarget(t, privateTarget, privateContent)
}

func verifyConfigInitSymlinkError(t *testing.T, output []byte, symlinkPath string, privateContent []byte) {
	t.Helper()

	if bytes.Contains(output, []byte(symlinkPath)) || bytes.Contains(output, privateContent) {
		t.Fatalf("symlink failure leaked private data: %s", output)
	}

	var failureEnvelope struct {
		OK    bool              `json:"ok"`
		Error *jsonErrorPayload `json:"error"`
	}
	if decodeErr := json.Unmarshal(output, &failureEnvelope); decodeErr != nil {
		t.Fatalf("decoding symlink error envelope: %v\noutput: %s", decodeErr, output)
	}

	if failureEnvelope.OK || failureEnvelope.Error == nil || failureEnvelope.Error.Code != "configSymlink" ||
		failureEnvelope.Error.Message != "configuration target must not be a symbolic link" {
		t.Fatalf("unexpected symlink error envelope: %+v", failureEnvelope)
	}
}

func verifyConfigInitSymlinkTarget(t *testing.T, privateTarget string, privateContent []byte) {
	t.Helper()

	content, readErr := os.ReadFile(privateTarget)
	if readErr != nil || !bytes.Equal(content, privateContent) {
		t.Fatalf("symlink target changed: %q, error: %v", content, readErr)
	}
}

func verifyConfigInitInvalidFlagDoesNotMutate(t *testing.T, fixture configInitBinaryFixture) {
	t.Helper()

	invalidHome := filepath.Join(fixture.root, "invalid-config")
	invalidTarget := filepath.Join(invalidHome, "sesh", "config.toml")
	invalidEnv := isolatedConfigInitEnvironment(
		fixture.home,
		invalidHome,
		fixture.dataHome,
		fixture.stateHome,
		fixture.cacheHome,
	)

	_, err := fixture.runWithEnvironment(t, invalidEnv, "config", "init", "--invalid")
	if err == nil {
		t.Fatal("invalid flag unexpectedly succeeded")
	}

	if _, statErr := os.Lstat(invalidTarget); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid flag created configuration: %v", statErr)
	}
}

func isolatedConfigInitEnvironment(home, configHome, dataHome, stateHome, cacheHome string) []string {
	prefixes := []string{
		"HOME=",
		"XDG_CONFIG_HOME=",
		"XDG_DATA_HOME=",
		"XDG_STATE_HOME=",
		"XDG_CACHE_HOME=",
	}

	env := make([]string, 0, len(os.Environ())+len(prefixes))
	for _, entry := range os.Environ() {
		drop := false

		for _, prefix := range prefixes {
			if strings.HasPrefix(entry, prefix) {
				drop = true
				break
			}
		}

		if !drop {
			env = append(env, entry)
		}
	}

	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+configHome,
		"XDG_DATA_HOME="+dataHome,
		"XDG_STATE_HOME="+stateHome,
		"XDG_CACHE_HOME="+cacheHome,
	)
}
