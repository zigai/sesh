//go:build linux || darwin

package engine

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestSaveOutputIsolation(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, scenario := range []string{"partial", "failure", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), program, "-test.run=^TestSaveOutputWorker$")
			leaseDir := filepath.Join(t.TempDir(), "leases")
			socketPath := filepath.Join(t.TempDir(), "test.sock")
			cmd.Env = []string{
				"SESH_OUTPUT_WORKER=" + scenario,
				"SESH_OUTPUT_ROOT=" + filepath.Join(t.TempDir(), "repository"),
				"SESH_OUTPUT_LEASE_ROOT=" + leaseDir,
				"SESH_OUTPUT_SOCKET=" + socketPath,
			}

			var stdout, stderr bytes.Buffer

			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("worker: %v\n%s\n%s", err, stdout.String(), stderr.String())
			}

			if stderr.Len() != 0 {
				t.Fatalf("logs leaked to stderr: %s", stderr.String())
			}

			lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte{'\n'})
			if len(lines) != 2 || string(lines[1]) != "PASS" {
				t.Fatalf("logs leaked to stdout: %s", stdout.String())
			}

			var result SaveResult
			if err := json.Unmarshal(lines[0], &result); err != nil {
				t.Fatal("result JSON contaminated", err)
			}
		})
	}
}

func TestSaveOutputWorker(t *testing.T) {
	scenario := os.Getenv("SESH_OUTPUT_WORKER")
	if scenario == "" {
		t.Skip("subprocess entry point")
	}

	r, err := store.Open(t.Context(), os.Getenv("SESH_OUTPUT_ROOT"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})

	var logs bytes.Buffer

	logger := zerolog.New(&logs)
	if scenario == "disabled" {
		logger = logger.Level(zerolog.Disabled)
	}

	leaseRoot := os.Getenv("SESH_OUTPUT_LEASE_ROOT")
	socketPath := os.Getenv("SESH_OUTPUT_SOCKET")

	result, saveErr := save(logger.WithContext(t.Context()), r, leaseRoot, socketPath, func(context.Context) (capture.Observation, error) {
		if scenario == "failure" {
			return capture.Observation{}, fmt.Errorf("secret error: %w", os.ErrPermission)
		}

		return observationFixture(), nil
	})
	if (saveErr != nil) != (scenario == "failure") {
		t.Fatal("unexpected save result", saveErr)
	}

	if scenario == "disabled" {
		if logs.Len() != 0 {
			t.Fatal("disabled branch emitted logs")
		}
	} else if !bytes.Contains(logs.Bytes(), []byte(`"component":"snapshot"`)) {
		t.Fatal("logger context was lost")
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fmt.Fprintln(os.Stdout, string(data)); err != nil {
		t.Fatal(err)
	}
}
