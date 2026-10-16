//go:build integration && (linux || darwin)

package cli_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/remote"
)

const remoteConfigReloadInterval = 250 * time.Millisecond

type remoteReloadProcess struct {
	command *exec.Cmd
	cancel  context.CancelFunc
	done    chan struct{}
	waitErr error
	stdout  bytes.Buffer
	stderr  bytes.Buffer
}

func TestRemoteMonitorBinaryReloadRemovesObsoleteConnection(t *testing.T) {
	root := t.TempDir()
	binary := buildRemoteReloadBinary(t, root)
	configPath := filepath.Join(root, "config.toml")
	cacheHome := filepath.Join(root, "cache")
	stateHome := filepath.Join(root, "state")

	logFile := filepath.Join(stateHome, "sesh.log")
	logHeader := fmt.Sprintf("[paths]\nlog_file = %q\n\n", logFile)

	writeTestConfig(t, configPath, logHeader+"[remote]\nenabled = true\nssh_path = \"/bin/false\"\n"+
		remoteConnectionsTOML(config.RemoteConnectionConfig{ID: "remove-me", Host: "example.invalid"}))

	process := startRemoteReloadProcess(t, binary, configPath, root, cacheHome, stateHome)
	catalogPath := filepath.Join(cacheHome, "sesh", "remote", "catalog.json")

	waitForRemoteReloadCatalog(t, catalogPath, process.done, func(catalog remote.Catalog) bool {
		_, exists := catalog.Connections["remove-me"]

		return exists
	})

	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}

	assertRemoteReloadCatalogRetained(t, catalogPath, process.done, "remove-me")

	writeTestConfig(t, configPath, logHeader+"[remote]\nenabled = false\nssh_path = \"/bin/false\"\n")

	waitForRemoteReloadCatalog(t, catalogPath, process.done, func(catalog remote.Catalog) bool {
		return len(catalog.Connections) == 0
	})

	process.stop(t)

	logData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}

	outcomes := 0

	for line := range strings.SplitSeq(strings.TrimSpace(string(logData)), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("monitor log is not JSON Lines: %v; line=%q", err, line)
		}

		if record["level"] == "error" {
			t.Fatalf("interrupted monitor logged an error: %s", line)
		}

		if record["component"] == "cli" && record["operation"] == "remote_monitor" && record["status"] != "started" {
			outcomes++

			if record["level"] != "info" || record["status"] != "ok" {
				t.Fatalf("interrupted monitor outcome = %s, want a finished record", line)
			}
		}
	}

	if outcomes != 1 {
		t.Fatalf("monitor logged %d outcomes, want 1", outcomes)
	}
}

func assertRemoteReloadCatalogRetained(t *testing.T, path string, done <-chan struct{}, connectionID string) {
	t.Helper()

	deadline := time.Now().Add(3 * remoteConfigReloadInterval)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatal("monitor exited while retaining last-known-good configuration")
		default:
		}

		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		var catalog remote.Catalog
		if err := json.Unmarshal(content, &catalog); err != nil {
			t.Fatal(err)
		}

		if _, exists := catalog.Connections[connectionID]; !exists {
			t.Fatal("missing reload removed a last-known-good connection")
		}

		time.Sleep(25 * time.Millisecond)
	}
}

func buildRemoteReloadBinary(t *testing.T, root string) string {
	t.Helper()

	binary := filepath.Join(root, "sesh")
	build := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-o", binary, "../../../cmd/sesh")
	build.Dir = "."

	build.Env = append(os.Environ(), "GOWORK=off")

	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building sesh: %v: %s", err, output)
	}

	return binary
}

func startRemoteReloadProcess(t *testing.T, binary, configPath, root, cacheHome, stateHome string) *remoteReloadProcess {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	process := &remoteReloadProcess{
		command: nil, cancel: cancel, done: make(chan struct{}), waitErr: nil,
		stdout: bytes.Buffer{}, stderr: bytes.Buffer{},
	}

	process.command = exec.CommandContext(ctx, binary, "--config", configPath, "daemon", "remote-monitor")

	process.command.Env = append(append(os.Environ(), "GOWORK=off"),
		"HOME="+filepath.Join(root, "home"),
		"XDG_CACHE_HOME="+cacheHome,
		"XDG_STATE_HOME="+stateHome,
		"XDG_CONFIG_HOME="+filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
	)
	process.command.Stdout = &process.stdout
	process.command.Stderr = &process.stderr

	if err := process.command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}

	go func() {
		process.waitErr = process.command.Wait()

		close(process.done)
	}()

	t.Cleanup(func() { process.join() })

	return process
}

func (process *remoteReloadProcess) join() {
	process.cancel()

	select {
	case <-process.done:
		return
	case <-time.After(3 * time.Second):
	}

	_ = process.command.Process.Kill()
	<-process.done
}

func (process *remoteReloadProcess) stop(t *testing.T) {
	t.Helper()

	if err := process.command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}

	select {
	case <-process.done:
	case <-time.After(5 * time.Second):
		process.join()
		t.Fatal("monitor did not stop after interrupt")
	}

	var exitErr *exec.ExitError
	if !errors.As(process.waitErr, &exitErr) || exitErr.ExitCode() != 130 {
		t.Fatalf("monitor exit = %v, want status 130", process.waitErr)
	}

	if process.stdout.Len() != 0 || process.stderr.Len() != 0 {
		t.Fatalf("monitor polluted command streams: stdout=%q stderr=%q", process.stdout.String(), process.stderr.String())
	}
}

func waitForRemoteReloadCatalog(t *testing.T, path string, done <-chan struct{}, accept func(remote.Catalog) bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatal("monitor exited before reload completed")
		default:
		}

		content, err := os.ReadFile(path)
		if err == nil {
			var catalog remote.Catalog
			if err := json.Unmarshal(content, &catalog); err != nil {
				t.Fatalf("decoding catalog: %v", err)
			}

			if accept(catalog) {
				return
			}
		}

		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("catalog %q did not reach expected state", path)
}
