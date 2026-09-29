//go:build linux

package cmdautosave_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/daemon"
	"github.com/zigai/sesh/internal/testsupport/clitest"
)

func autosaveRuntimeFixture(t *testing.T) (string, string) {
	t.Helper()
	isolatedAutosaveHome(t)

	runtimeDir, err := os.MkdirTemp("/tmp", "sesh-runtime-") //nolint:usetesting // reason: Unix socket paths need a short name; t.Cleanup removes this private fixture.
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := os.RemoveAll(runtimeDir); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	return runtimeDir, filepath.Join(runtimeDir, "private-endpoint.sock")
}

func assertRuntimeJSONPrivacy(t *testing.T, execution commandExecution, endpoint string) {
	t.Helper()

	if execution.Stderr != "" {
		t.Fatalf("runtime JSON leaked to stderr: %q", execution.Stderr)
	}

	for _, private := range []string{endpoint, `"pid"`, `"bootId"`, `"processStartTicks"`, `"endpointId"`} {
		if strings.Contains(execution.Stdout, private) {
			t.Fatalf("runtime JSON exposed private identity %q", private)
		}
	}
}

func TestAutosaveRuntimeStatusTracksReadinessAndLiveProbeFailure(t *testing.T) {
	runtimeDir, endpoint := autosaveRuntimeFixture(t)

	var held, unavailable atomic.Bool
	held.Store(true)

	reporter, err := daemon.StartRuntimeReporter(t.Context(), daemon.RuntimeStatusConfig{
		RuntimeDir: runtimeDir, Endpoint: endpoint,
		LogindDelay: 5 * time.Second, AttemptTimeout: 3 * time.Second, DrainGrace: time.Second,
		LiveStatus: func(context.Context) (daemon.RuntimeLiveStatus, error) {
			if unavailable.Load() {
				return daemon.RuntimeLiveStatus{}, daemon.ErrRuntimeStatusUnavailable
			}

			return daemon.RuntimeLiveStatus{InhibitorsHeld: held.Load()}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := reporter.Close(); err != nil {
			t.Error(err)
		}
	})

	cases := []struct {
		name        string
		phase       daemon.RuntimePhase
		held        bool
		unavailable bool
		state       string
	}{
		{"initializing", daemon.RuntimePhaseInitializing, true, false, "not_ready"},
		{"running", daemon.RuntimePhaseRunning, true, false, "ready"},
		{"draining", daemon.RuntimePhaseDraining, true, false, "not_ready"},
		{"released", daemon.RuntimePhaseFinalSave, false, false, "not_ready"},
		{"owner_lost", daemon.RuntimePhaseRunning, true, true, "unverifiable"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			held.Store(test.held)
			unavailable.Store(test.unavailable)
			reporter.Update(test.phase)

			execution := executeJSON(t, "autosave", "status", testNoConfigFlag, testJSONFlag, testSocketFlag, endpoint)
			if execution.Err != nil || !execution.Envelope.OK {
				t.Fatalf("status inspection failed: %v", execution.Err)
			}

			assertRuntimeJSONPrivacy(t, execution, endpoint)

			result := clitest.DecodePayload[autosaveResultJSON](t, execution.Envelope.Result)
			assertAutosaveRuntimeFacts(t, result, test.state, test.phase, test.held)
		})
	}
}

func assertAutosaveRuntimeFacts(t *testing.T, result autosaveResultJSON, state string, phase daemon.RuntimePhase, held bool) {
	t.Helper()

	if result.DaemonState != state {
		t.Fatalf("daemonState = %q, want %q", result.DaemonState, state)
	}

	if state == "unverifiable" {
		if result.InhibitorsHeld || result.DaemonPhase != "" || result.LogindDelayMS != 0 {
			t.Fatalf("unverifiable daemon retained prior live facts: %+v", result)
		}

		return
	}

	if result.DaemonPhase != string(phase) || result.InhibitorsHeld != held ||
		result.LogindDelayMS != 5000 || result.AttemptTimeoutMS != 3000 || result.DrainGraceMS != 1000 ||
		result.ServerManagement != "unknown" {
		t.Fatalf("unexpected runtime result: %+v", result)
	}
}

func TestAutosaveRuntimeStatusDistinguishesAbsentAndStaleEndpoints(t *testing.T) {
	runtimeDir, endpoint := autosaveRuntimeFixture(t)

	path, err := daemon.RuntimeStatusPath(runtimeDir, endpoint)
	if err != nil {
		t.Fatal(err)
	}

	query := func(want string) {
		t.Helper()
		execution := executeJSON(t, "autosave", "status", testNoConfigFlag, testJSONFlag, testSocketFlag, endpoint)
		assertRuntimeJSONPrivacy(t, execution, endpoint)

		result := clitest.DecodePayload[autosaveResultJSON](t, execution.Envelope.Result)
		if result.DaemonState != want || result.InhibitorsHeld || result.DaemonPhase != "" {
			t.Fatalf("runtime result = %+v, want %s without live facts", result, want)
		}
	}
	query("absent")

	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only status created runtime state: %v", err)
	}

	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}

	stale.SetUnlinkOnClose(false)

	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}

	query("stale")

	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("status removed stale endpoint: %v", err)
	}

	t.Setenv("XDG_RUNTIME_DIR", "relative-runtime")
	query("unverifiable")
}
