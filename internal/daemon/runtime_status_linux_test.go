//go:build linux

package daemon

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/privatefs"
)

var runtimeTestDirectoryCounter atomic.Uint64

func TestRuntimeReporterReturnsVerifiedLiveState(t *testing.T) {
	runtimeDir := shortRuntimeDir(t)
	endpoint := filepath.Join(runtimeDir, "tmux.sock")

	var held atomic.Bool
	held.Store(true)

	reporter, err := StartRuntimeReporter(t.Context(), RuntimeStatusConfig{
		RuntimeDir:     runtimeDir,
		Endpoint:       endpoint,
		LogindDelay:    3 * time.Second,
		AttemptTimeout: 2 * time.Second,
		DrainGrace:     time.Second,
		LiveStatus: func(context.Context) (RuntimeLiveStatus, error) {
			return RuntimeLiveStatus{InhibitorsHeld: held.Load()}, nil
		},
	})
	if err != nil {
		t.Fatalf("StartRuntimeReporter failed: %v", err)
	}

	t.Cleanup(func() {
		if err := reporter.Close(); err != nil {
			t.Errorf("Close failed: %v", err)
		}
	})

	reporter.Update(RuntimePhaseRunning)

	path, err := RuntimeStatusPath(runtimeDir, endpoint)
	if err != nil {
		t.Fatal(err)
	}

	status, err := QueryRuntimeStatus(t.Context(), path)
	if err != nil {
		t.Fatalf("QueryRuntimeStatus failed: %v", err)
	}

	assertRunningRuntimeStatus(t, status)

	held.Store(false)
	reporter.Update(RuntimePhaseFinalSave)

	status, err = QueryRuntimeStatus(t.Context(), path)
	if err != nil {
		t.Fatalf("second QueryRuntimeStatus failed: %v", err)
	}

	if status.Phase != RuntimePhaseFinalSave || status.InhibitorsHeld {
		t.Fatalf("updated status = %+v", status)
	}
}

func assertRunningRuntimeStatus(t *testing.T, status RuntimeStatus) {
	t.Helper()

	if status.Phase != RuntimePhaseRunning || !status.InhibitorsHeld || status.LogindDelayMillis != 3000 {
		t.Fatalf("status = %+v", status)
	}

	if status.ServerManagement != ServerManagementUnknown || status.PID != os.Getpid() || status.BootID == "" || status.ProcessStartTicks == 0 {
		t.Fatalf("identity = %+v", status)
	}
}

func TestRuntimeReporterRejectsSecondActiveOwner(t *testing.T) {
	runtimeDir := shortRuntimeDir(t)
	config := RuntimeStatusConfig{
		RuntimeDir: runtimeDir,
		Endpoint:   filepath.Join(runtimeDir, "tmux.sock"),
		LiveStatus: func(context.Context) (RuntimeLiveStatus, error) {
			return RuntimeLiveStatus{InhibitorsHeld: true}, nil
		},
	}

	first, err := StartRuntimeReporter(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = first.Close() })

	if second, err := StartRuntimeReporter(t.Context(), config); !errors.Is(err, ErrRuntimeStatusActive) || second != nil {
		if second != nil {
			_ = second.Close()
		}

		t.Fatalf("second reporter = (%v, %v), want ErrRuntimeStatusActive", second, err)
	}
}

func TestRuntimeReporterFailsClosedOnOwnedStaleSocket(t *testing.T) {
	runtimeDir := shortRuntimeDir(t)
	endpoint := filepath.Join(runtimeDir, "tmux.sock")

	path, err := RuntimeStatusPath(runtimeDir, endpoint)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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

	reporter, err := StartRuntimeReporter(t.Context(), RuntimeStatusConfig{
		RuntimeDir: runtimeDir,
		Endpoint:   endpoint,
		LiveStatus: func(context.Context) (RuntimeLiveStatus, error) {
			return RuntimeLiveStatus{}, nil
		},
	})
	if !errors.Is(err, ErrRuntimeStatusStale) || reporter != nil {
		t.Fatalf("StartRuntimeReporter = (%v, %v), want stale refusal", reporter, err)
	}
}

func TestRuntimeReporterDoesNotUnlinkReplacementSocket(t *testing.T) {
	runtimeDir := shortRuntimeDir(t)
	endpoint := filepath.Join(runtimeDir, "tmux.sock")
	config := RuntimeStatusConfig{
		RuntimeDir:     runtimeDir,
		Endpoint:       endpoint,
		LogindDelay:    time.Second,
		AttemptTimeout: time.Second,
		DrainGrace:     time.Second,
		LiveStatus: func(context.Context) (RuntimeLiveStatus, error) {
			return RuntimeLiveStatus{}, nil
		},
	}

	reporter, err := StartRuntimeReporter(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}

	path, err := RuntimeStatusPath(runtimeDir, endpoint)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}

	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = replacement.Close() })

	if err := reporter.Close(); !errors.Is(err, ErrRuntimeStatusIdentity) {
		t.Fatalf("Close error = %v, want identity refusal", err)
	}

	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("replacement socket was unlinked: %v", err)
	}
}

func TestRuntimeListenerUsesPinnedParentAfterPathReplacement(t *testing.T) {
	root := shortRuntimeDir(t)
	parent := filepath.Join(root, "sesh")

	dir, err := privatefs.Ensure(parent)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = dir.Close() })

	moved := parent + "-moved"
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}

	listener, err := listenRuntimeEndpoint(dir, "status.sock")
	if err != nil {
		t.Fatal(err)
	}

	listener.SetUnlinkOnClose(false)

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(moved, "status.sock")); err != nil {
		t.Fatalf("socket missing from pinned parent: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(parent, "status.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket created through replacement path: %v", err)
	}
}

func TestQueryRuntimeStatusRejectsReporterForAnotherEndpoint(t *testing.T) {
	runtimeDir := shortRuntimeDir(t)
	endpointB := filepath.Join(runtimeDir, "tmux-b.sock")

	reporter, err := StartRuntimeReporter(t.Context(), validRuntimeConfig(runtimeDir, endpointB, nil))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = reporter.Close() })

	pathB, _ := RuntimeStatusPath(runtimeDir, endpointB)
	endpointA := filepath.Join(runtimeDir, "tmux-a.sock")

	pathA, _ := RuntimeStatusPath(runtimeDir, endpointA)
	if err := os.Rename(pathB, pathA); err != nil {
		t.Fatal(err)
	}

	if _, err := QueryRuntimeStatus(t.Context(), pathA); !errors.Is(err, ErrRuntimeStatusIdentity) {
		t.Fatalf("misrouted QueryRuntimeStatus error = %v, want identity refusal", err)
	}
}

func TestRuntimeReporterRejectsTornPhaseResponse(t *testing.T) {
	runtimeDir := shortRuntimeDir(t)
	endpoint := filepath.Join(runtimeDir, "tmux.sock")
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})

	reporter, err := StartRuntimeReporter(t.Context(), validRuntimeConfig(runtimeDir, endpoint, func(context.Context) (RuntimeLiveStatus, error) {
		close(probeStarted)
		<-releaseProbe

		return RuntimeLiveStatus{InhibitorsHeld: true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = reporter.Close() })
	reporter.Update(RuntimePhaseRunning)

	path, _ := RuntimeStatusPath(runtimeDir, endpoint)
	result := make(chan error, 1)

	go func() {
		_, queryErr := QueryRuntimeStatus(t.Context(), path)
		result <- queryErr
	}()

	<-probeStarted
	reporter.Update(RuntimePhaseFinalSave)
	close(releaseProbe)

	if err := <-result; !errors.Is(err, ErrRuntimeStatusUnavailable) {
		t.Fatalf("torn QueryRuntimeStatus error = %v, want unavailable", err)
	}
}

func validRuntimeConfig(
	runtimeDir string,
	endpoint string,
	live func(context.Context) (RuntimeLiveStatus, error),
) RuntimeStatusConfig {
	if live == nil {
		live = func(context.Context) (RuntimeLiveStatus, error) {
			return RuntimeLiveStatus{InhibitorsHeld: true}, nil
		}
	}

	return RuntimeStatusConfig{
		RuntimeDir:     runtimeDir,
		Endpoint:       endpoint,
		LogindDelay:    time.Second,
		AttemptTimeout: time.Second,
		DrainGrace:     time.Second,
		LiveStatus:     live,
	}
}

func TestQueryRuntimeStatusHonorsReadDeadline(t *testing.T) {
	runtimeDir := shortRuntimeDir(t)
	path := filepath.Join(runtimeDir, "stall.sock")

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan *net.UnixConn, 1)

	go func() {
		connection, _ := listener.AcceptUnix()
		accepted <- connection
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	if _, err := QueryRuntimeStatus(ctx, path); !errors.Is(err, ErrRuntimeStatusUnavailable) {
		t.Fatalf("QueryRuntimeStatus error = %v, want unavailable", err)
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("QueryRuntimeStatus error = %v, want deadline cause", err)
	}

	connection := <-accepted
	if connection != nil {
		_ = connection.Close()
	}
}

func TestQueryRuntimeStatusRejectsInvalidBudget(t *testing.T) {
	runtimeDir := shortRuntimeDir(t)
	endpoint := filepath.Join(runtimeDir, "tmux.sock")

	path, err := RuntimeStatusPath(runtimeDir, endpoint)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	identity, err := currentRuntimeIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	response, err := json.Marshal(RuntimeStatus{
		Protocol:             runtimeStatusProtocol,
		Phase:                RuntimePhaseRunning,
		InhibitorsHeld:       true,
		LogindDelayMillis:    -1,
		AttemptTimeoutMillis: 1000,
		DrainGraceMillis:     1000,
		ServerManagement:     ServerManagementUnknown,
		EndpointID:           runtimeEndpointID(endpoint),
		PID:                  identity.pid,
		BootID:               identity.bootID,
		ProcessStartTicks:    identity.startTicks,
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)

		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			return
		}

		_, _ = connection.Write(response)
		_ = connection.Close()
	}()

	if _, err := QueryRuntimeStatus(t.Context(), path); !errors.Is(err, ErrRuntimeStatusIdentity) {
		t.Fatalf("QueryRuntimeStatus error = %v, want identity refusal", err)
	}

	<-done
}

func TestRuntimeStatusPathRejectsUnboundedOrRelativeInput(t *testing.T) {
	if _, err := RuntimeStatusPath("relative", "/tmp/tmux.sock"); !errors.Is(err, ErrRuntimeStatusPath) {
		t.Fatalf("relative runtime path error = %v", err)
	}

	longRuntime := "/tmp/" + strings.Repeat("x", linuxUnixPathMaximum)
	if _, err := RuntimeStatusPath(longRuntime, "/tmp/tmux.sock"); !errors.Is(err, ErrRuntimeStatusPath) {
		t.Fatalf("long runtime path error = %v", err)
	}
}

func TestRuntimeReporterRetainsUnknownServerManagement(t *testing.T) {
	runtimeDir := shortRuntimeDir(t)
	config := RuntimeStatusConfig{
		RuntimeDir:     runtimeDir,
		Endpoint:       filepath.Join(runtimeDir, "tmux.sock"),
		LogindDelay:    3 * time.Second,
		AttemptTimeout: time.Second,
		DrainGrace:     500 * time.Millisecond,
		LiveStatus: func(context.Context) (RuntimeLiveStatus, error) {
			return RuntimeLiveStatus{InhibitorsHeld: true}, nil
		},
	}

	reporter, err := StartRuntimeReporter(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = reporter.Close() })

	path, err := RuntimeStatusPath(runtimeDir, config.Endpoint)
	if err != nil {
		t.Fatal(err)
	}

	status, err := QueryRuntimeStatus(t.Context(), path)
	if err != nil {
		t.Fatalf("QueryRuntimeStatus failed: %v", err)
	}

	if status.ServerManagement != ServerManagementUnknown {
		t.Fatalf("serverManagement = %q, want %q", status.ServerManagement, ServerManagementUnknown)
	}
}

func TestValidateRuntimeStatusRejectsNonUnknownServerManagement(t *testing.T) {
	t.Parallel()

	validStatus := RuntimeStatus{
		Protocol:             runtimeStatusProtocol,
		Phase:                RuntimePhaseRunning,
		InhibitorsHeld:       true,
		LogindDelayMillis:    1000,
		AttemptTimeoutMillis: 1000,
		DrainGraceMillis:     1000,
		ServerManagement:     ServerManagementUnknown,
		EndpointID:           strings.Repeat("a", runtimeStatusSocketBytes*2),
		PID:                  1234,
	}

	if err := validateRuntimeStatus(validStatus, 1234, validStatus.EndpointID); err != nil {
		t.Fatalf("validate valid status failed: %v", err)
	}

	for _, invalidManagement := range []string{"systemd", "sesh-tmux.service", "sesh-tmux-foreign.service", ""} {
		status := validStatus
		status.ServerManagement = invalidManagement

		if err := validateRuntimeStatus(status, 1234, status.EndpointID); !errors.Is(err, ErrRuntimeStatusIdentity) {
			t.Errorf("validateRuntimeStatus with management %q = %v, want ErrRuntimeStatusIdentity", invalidManagement, err)
		}
	}
}

func shortRuntimeDir(t *testing.T) string {
	t.Helper()

	directory := filepath.Join(
		"/tmp",
		fmt.Sprintf("sesh-runtime-%d-%d", os.Getpid(), runtimeTestDirectoryCounter.Add(1)),
	)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})

	return directory
}
