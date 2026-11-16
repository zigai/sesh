//go:build integration

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"
)

func TestApplicationSaveOwnsOneOutcome(t *testing.T) {
	server := newCaptureCLIServer(t)

	sessions, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, failed := range []bool{false, true} {
		t.Run(strconv.FormatBool(failed), func(t *testing.T) {
			cfg, logPath := runtimeConfig(t, "info")

			socket := server.Endpoint().SocketPath
			if failed {
				socket = filepath.Join(t.TempDir(), "missing.sock")
			}

			repoPath := filepath.Join(t.TempDir(), "private-repository-canary")
			args := []string{
				snapshotCmd,
				"save",
				testRepositoryFlag,
				repoPath,
				testSocketFlag,
				socket,
				testLeaseRootFlag,
				filepath.Join(t.TempDir(), "leases"),
				string(sessions[0].ID),
				testConfigFlag,
				cfg,
				testJSONFlag,
			}

			var stdout, stderr bytes.Buffer

			err := ExecuteApplication(t.Context(), args, nil, &stdout, &stderr)

			payload := stdout.Bytes()
			if len(payload) == 0 {
				payload = stderr.Bytes()
			} else if stderr.Len() != 0 {
				t.Fatal("two output streams")
			}

			env := singleRuntimeEnvelope(t, payload)
			level, status := assertApplicationSaveOutcome(t, failed, err, env)

			if failed {
				assertSingleRuntimeRecord(t, logPath, "cli", "save", level, status)

				return
			}

			assertRuntimeLog(t, logPath, "snapshot", "save", level, status)
		})
	}
}

func assertApplicationSaveOutcome(
	t *testing.T,
	failed bool,
	err error,
	env testJSONEnvelope,
) (string, string) {
	t.Helper()

	if failed {
		if err == nil || env.OK || env.Error == nil {
			t.Fatalf("invalid failed save outcome: err=%v envelope=%+v", err, env)
		}

		return "error", "failed"
	}

	if err != nil || !env.OK || env.Error != nil {
		t.Fatalf("invalid practical save outcome: err=%v envelope=%+v", err, env)
	}

	return "info", "best_effort"
}

func assertSingleRuntimeRecord(
	t *testing.T,
	path string,
	component string,
	operation string,
	level string,
	status string,
) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("expected one outcome: %s", data)
	}

	assertRuntimeRecord(t, lines[0], component, operation, level, status)
}

func newCaptureCLIServer(t *testing.T) *gotmux.Server {
	t.Helper()
	server := tmuxtest.NewServer(t)

	windows, err := server.Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, window := range windows {
		options := window.Handle().Options()
		if err := options.SetAutomaticRename(t.Context(), false); err != nil {
			t.Fatal(err)
		}

		if err := options.SetAllowRename(t.Context(), false); err != nil {
			t.Fatal(err)
		}
	}

	return server
}
