//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

var errTestConnectionFailed = errors.New("connection failed")

func TestReconcileCatalogHidesNeverConnectedFailureDespiteRetries(t *testing.T) {
	failedAt := time.Unix(100, 0).UTC()
	catalog := EmptyCatalog()
	catalog.Connections["host"] = ConnectionState{
		ID:          "host",
		LastAttempt: failedAt.Add(10 * time.Minute),
		LastErrorAt: failedAt,
		LastError:   ConnectionErrorUnavailable,
		Snapshot:    Snapshot{Health: UnavailableHealth()},
	}

	changed, hidden := ReconcileCatalog(&catalog, failedAt.Add(6*time.Minute), time.Minute, 5*time.Minute)
	if !changed || len(hidden) != 1 {
		t.Fatalf("ReconcileCatalog() = (%t, %#v), want the failure hidden", changed, hidden)
	}

	if _, exists := catalog.Connections["host"]; exists {
		t.Fatal("never-connected failure remained visible after the hide interval")
	}
}

func TestStoreAcceptedFramePreservesHiddenErrorAfterRejectedFrame(t *testing.T) {
	const connectionID = "host"

	failedAt := time.Unix(100, 0).UTC()
	state := &monitorState{
		hidden: map[string]ConnectionState{
			connectionID: {ID: connectionID, LastError: ConnectionErrorStale, LastErrorAt: failedAt},
		},
	}

	state.storeAcceptedFrame(t.Context(), connectionID, ConnectionState{ID: connectionID}, true, FrameHealth, ErrInvalidFrame)

	got := state.hidden[connectionID]
	if got.LastError != ConnectionErrorStale || !got.LastErrorAt.Equal(failedAt) {
		t.Fatalf("hidden connection state = %#v, want the preserved error state", got)
	}
}

func TestAcceptFrameRejectsSecondHelloForStream(t *testing.T) {
	observedAt := time.Unix(100, 0).UTC()

	var state ConnectionState

	first := helloFrame(testPeerID, "stream", 1)

	if err := acceptFrame(&state, first, observedAt); err != nil {
		t.Fatalf("acceptFrame(first hello) error = %v", err)
	}

	second := first
	second.Sequence = 2

	if err := acceptFrame(&state, second, observedAt); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("acceptFrame(second hello) error = %v, want %v", err, ErrInvalidFrame)
	}

	if err := acceptFrame(&state, helloFrame("other-peer", "other-stream", 3), observedAt); !errors.Is(err, ErrPeerMismatch) {
		t.Fatalf("acceptFrame(foreign peer) error = %v, want %v", err, ErrPeerMismatch)
	}

	if err := acceptFrame(&state, snapshotFrame("other-peer", "stream", 2, nil), observedAt); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("acceptFrame(foreign incarnation) error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestMonitorStateTransitionLogging(t *testing.T) {
	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())

	now := time.Unix(1000, 0).UTC()
	state := &monitorState{
		now:        func() time.Time { return now },
		catalog:    EmptyCatalog(),
		configured: make(map[string]ConnectionState),
		hidden:     make(map[string]ConnectionState),
	}

	const connectionID = "private-host"

	connected := ConnectionState{ID: connectionID, Connected: true, Snapshot: Snapshot{Health: UnavailableHealth()}}
	state.catalog.Connections[connectionID] = connected

	state.handleConnectionError(ctx, connectionID, connected, false, errTestConnectionFailed)

	records := decodeLogRecords(t, logs.String())
	if len(records) != 1 {
		t.Fatalf("degraded records = %#v, want one boundary", records)
	}

	if records[0]["status"] != "degraded" || records[0]["operation"] != "connection" ||
		records[0]["component"] != "remote_monitor" || records[0]["error_code"] != ConnectionErrorUnavailable {
		t.Errorf("degraded record = %#v", records[0])
	}

	if strings.Contains(logs.String(), connectionID) {
		t.Fatalf("logs leaked the connection identity: %s", logs.String())
	}

	logs.Reset()

	state.storeAcceptedFrame(ctx, connectionID, ConnectionState{ID: connectionID, Connected: true, Snapshot: Snapshot{Health: healthyHealth()}}, false, FrameSnapshot, nil)

	records = decodeLogRecords(t, logs.String())
	if len(records) != 1 || records[0]["status"] != "recovered" || records[0]["reason"] != "connection_available" || records[0]["level"] != "info" {
		t.Fatalf("recovered records = %#v, want one recovery boundary", records)
	}

	if strings.Contains(logs.String(), connectionID) {
		t.Fatalf("logs leaked the connection identity: %s", logs.String())
	}
}

func TestMonitorReconcileLogsStaleCountWithoutIdentities(t *testing.T) {
	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())

	now := time.Unix(2000, 0).UTC()
	catalog := EmptyCatalog()
	catalog.Connections["private-host"] = ConnectionState{
		ID: "private-host", Connected: true, LastSeen: now.Add(-time.Hour), Snapshot: Snapshot{Health: UnavailableHealth()},
	}

	state := &monitorState{
		now:        func() time.Time { return now },
		catalog:    catalog,
		hidden:     make(map[string]ConnectionState),
		staleAfter: time.Second,
		hideAfter:  time.Minute,
	}

	state.reconcile(ctx)

	records := decodeLogRecords(t, logs.String())
	if len(records) != 1 {
		t.Fatalf("stale records = %#v, want one boundary", records)
	}

	if records[0]["level"] != "warn" || records[0]["component"] != "remote_monitor" ||
		records[0]["operation"] != "reconcile" || records[0]["status"] != "degraded" ||
		records[0]["reason"] != "connections_stale" || records[0]["stale_count"] != float64(1) {
		t.Errorf("stale record = %#v", records[0])
	}

	if strings.Contains(logs.String(), "private-host") {
		t.Fatalf("logs leaked the connection identity: %s", logs.String())
	}
}

func decodeLogRecords(t *testing.T, output string) []map[string]any {
	t.Helper()

	records := make([]map[string]any, 0, 1)

	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}

		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}

		records = append(records, record)
	}

	return records
}

func TestStreamSupervisorRestartsOnlyAffectedConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	updates := make(chan workerUpdate, 8)

	supervisor := newStreamSupervisor(updates, nil)

	defer supervisor.stop()

	remote := testRemoteConfig()
	remote.Connections = []config.RemoteConnectionConfig{
		{ID: "one", Label: "One", Host: "one.example"},
		{ID: "two", Label: "Two", Host: "two.example"},
	}

	initial := supervisor.apply(ctx, remote, remote.Connections)

	remote.Connections[0].Label = "Renamed"

	labelOnly := supervisor.apply(ctx, remote, remote.Connections)
	if labelOnly["one"] != initial["one"] || labelOnly["two"] != initial["two"] {
		t.Fatalf("label-only reload generations = %#v, want %#v", labelOnly, initial)
	}

	remote.Connections[0].Host = "new-one.example"

	hostChanged := supervisor.apply(ctx, remote, remote.Connections)
	if hostChanged["one"] == labelOnly["one"] {
		t.Fatal("changed connection retained its worker generation")
	}

	if hostChanged["two"] != labelOnly["two"] {
		t.Fatal("unchanged connection was restarted")
	}

	remote.StaleAfter = "1m"

	staleAfterChanged := supervisor.apply(ctx, remote, remote.Connections)
	if staleAfterChanged["one"] == hostChanged["one"] || staleAfterChanged["two"] == hostChanged["two"] {
		t.Fatalf("stale_after reload retained worker generations: before=%#v after=%#v", hostChanged, staleAfterChanged)
	}
}

func TestStreamSupervisorBoundsLimiterForUnvalidatedConfiguration(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	supervisor := newStreamSupervisor(make(chan workerUpdate), nil)
	defer supervisor.stop()

	remoteConfig := testRemoteConfig()
	remoteConfig.MaxParallelConnects = int(^uint(0) >> 1)
	remoteConfig.Connections = nil

	supervisor.apply(ctx, remoteConfig, nil)

	if supervisor.limiterSize != 1 || cap(supervisor.limiter) != 1 {
		t.Fatalf("limiter = size %d capacity %d, want one", supervisor.limiterSize, cap(supervisor.limiter))
	}
}

func TestMonitorDropsUpdatesFromReplacedWorkerGeneration(t *testing.T) {
	original := newConnectionState(config.RemoteConnectionConfig{ID: testConnectionID, Host: "work.example"}, testConfigDigest)
	original.PeerID = "peer-new"

	catalog := EmptyCatalog()
	catalog.Connections[testConnectionID] = original

	state := &monitorState{
		now:               time.Now,
		catalog:           catalog,
		configured:        map[string]ConnectionState{testConnectionID: original},
		hidden:            make(map[string]ConnectionState),
		activeGenerations: map[string]uint64{testConnectionID: 2},
	}

	result := make(chan error, 1)

	state.handleUpdate(t.Context(), workerUpdate{
		connectionID: testConnectionID,
		generation:   1,
		frame:        helloFrame("peer-old", "old-stream", 1),
		result:       result,
	})

	if err := <-result; !errors.Is(err, ErrConnectionChanged) {
		t.Fatalf("stale worker result = %v, want %v", err, ErrConnectionChanged)
	}

	if got := state.catalog.Connections[testConnectionID].PeerID; got != "peer-new" {
		t.Fatalf("catalog peer = %q, want peer-new", got)
	}
}

func TestMonitorReloadResetsHiddenStateAfterTransportChange(t *testing.T) {
	oldConnection := config.RemoteConnectionConfig{ID: testConnectionID, Host: "old.example"}
	hidden := newConnectionState(oldConnection, ConnectionDigest(oldConnection))
	hidden.PeerID = "old-peer"

	state := &monitorState{
		now:               time.Now,
		catalog:           EmptyCatalog(),
		configured:        map[string]ConnectionState{oldConnection.ID: hidden},
		hidden:            map[string]ConnectionState{oldConnection.ID: hidden},
		activeGenerations: make(map[string]uint64),
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	supervisor := newStreamSupervisor(make(chan workerUpdate, 4), nil)

	defer supervisor.stop()

	remote := testRemoteConfig()
	remote.Connections = []config.RemoteConnectionConfig{{ID: testConnectionID, Host: "new.example"}}

	state.applyConfig(ctx, remote, supervisor)

	if _, exists := state.hidden[testConnectionID]; exists {
		t.Fatal("transport change retained hidden state")
	}

	current := state.catalog.Connections[testConnectionID]

	wantDigest := ConnectionDigest(remote.Connections[0])
	if current.ConfigDigest != wantDigest || current.PeerID != "" {
		t.Fatalf("reloaded state = %#v, want fresh state with digest %q", current, wantDigest)
	}

	generation := state.activeGenerations[testConnectionID]

	helloResult := make(chan error, 1)

	state.handleUpdate(t.Context(), workerUpdate{
		connectionID: testConnectionID,
		generation:   generation,
		frame:        helloFrame("new-peer", "new-stream", 1),
		result:       helloResult,
	})

	if err := <-helloResult; err != nil {
		t.Fatalf("accepting replacement hello: %v", err)
	}

	snapshotResult := make(chan error, 1)

	state.handleUpdate(t.Context(), workerUpdate{
		connectionID: testConnectionID,
		generation:   generation,
		frame:        snapshotFrame("new-peer", "new-stream", 2, []session.Session{{ID: "$1", Name: testSessionName, Kind: session.KindLive}}),
		result:       snapshotResult,
	})

	if err := <-snapshotResult; err != nil {
		t.Fatalf("accepting replacement snapshot: %v", err)
	}

	replacement := state.catalog.Connections[testConnectionID]
	if replacement.ConfigDigest != wantDigest || replacement.PeerID != "new-peer" || !replacement.Connected {
		t.Fatalf("replacement state = %#v, want an attachable replacement generation", replacement)
	}
}

func TestMonitorRejectsInvalidReloadThenRemovesObsoleteConnection(t *testing.T) {
	dir, path := openTestCatalogDir(t)

	initial := testRemoteConfig()
	reloads := make(chan config.RemoteConfig, 2)

	var logs bytes.Buffer

	ctx, cancel := context.WithCancel(zerolog.New(&logs).WithContext(t.Context()))
	done := make(chan error, 1)
	command := scriptSSH(t, "exit 1")

	go func() {
		done <- RunMonitor(ctx, MonitorOptions{
			CatalogDir: dir,
			Remote:     initial,
			Command:    command,
			Reloads:    reloads,
		})
	}()

	catalogPath := filepath.Join(path, catalogName)
	waitForCatalog(t, catalogPath, 5*time.Second, done, func(catalog Catalog) bool {
		_, exists := catalog.Connections[testConnectionID]

		return exists
	})

	invalid := initial
	invalid.Enabled = false

	invalid.StaleAfter = "private-invalid-value"
	reloads <- invalid

	time.Sleep(2 * monitorFlushInterval)

	waitForCatalog(t, catalogPath, time.Second, done, func(catalog Catalog) bool {
		_, exists := catalog.Connections[testConnectionID]

		return exists
	})

	removed := initial
	removed.Enabled = false

	removed.Connections = nil
	reloads <- removed

	waitForCatalog(t, catalogPath, 5*time.Second, done, func(catalog Catalog) bool {
		return len(catalog.Connections) == 0
	})

	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunMonitor() error = %v, want cancellation", err)
	}

	records := decodeLogRecords(t, logs.String())
	invalidRecords := 0

	for _, record := range records {
		if record["operation"] != "reload_config" {
			continue
		}

		invalidRecords++

		if record["level"] != "warn" || record["component"] != "remote_monitor" ||
			record["status"] != "degraded" || record["reason"] != "invalid_configuration" {
			t.Errorf("reload rejection record = %#v", record)
		}
	}

	if invalidRecords != 1 {
		t.Fatalf("reload rejection records = %d, want one; records = %#v", invalidRecords, records)
	}

	if strings.Contains(logs.String(), "private-invalid-value") || strings.Contains(logs.String(), testConnectionID) {
		t.Fatalf("reload logs leaked configuration values or identities: %s", logs.String())
	}
}

func TestStreamSupervisorLimitsConcurrentConnects(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	updates := make(chan workerUpdate, 16)
	started := make(chan struct{}, 2)

	var starts atomic.Int32

	command := func(commandContext context.Context, _ string, _ ...string) *exec.Cmd {
		starts.Add(1)

		started <- struct{}{}

		return exec.CommandContext(commandContext, "sh", "-c", "sleep 60")
	}

	supervisor := newStreamSupervisor(updates, command)

	defer supervisor.stop()

	remote := testRemoteConfig()
	remote.MaxParallelConnects = 1
	remote.Connections = []config.RemoteConnectionConfig{
		{ID: "one", Host: "one.example"},
		{ID: "two", Host: "two.example"},
	}

	supervisor.apply(ctx, remote, remote.Connections)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first SSH connection did not start")
	}

	select {
	case <-started:
		t.Fatal("second SSH connection started before the first completed its handshake")
	case <-time.After(100 * time.Millisecond):
	}

	if got := starts.Load(); got != 1 {
		t.Fatalf("concurrent SSH starts = %d, want 1", got)
	}
}

func monitorScript(t *testing.T, markerPath string, frames ...Frame) string {
	t.Helper()

	health := `{"version":` + strconv.Itoa(ProtocolVersion) + `,"type":"health","peerId":"peer","streamInstance":"stream","sequence":%d,` +
		`"health":{"observedAt":"1970-01-01T00:00:01Z","sources":{` +
		`"tmux":{"status":"ok","reason":"none","checkedAt":"1970-01-01T00:00:00Z"},` +
		`"agents":{"status":"ok","reason":"none","checkedAt":"1970-01-01T00:00:00Z"},` +
		`"snapshots":{"status":"ok","reason":"none","checkedAt":"1970-01-01T00:00:00Z"},` +
		`"tasks":{"status":"ok","reason":"none","checkedAt":"1970-01-01T00:00:00Z"},` +
		`"work":{"status":"ok","reason":"none","checkedAt":"1970-01-01T00:00:00Z"}}}}`

	var script strings.Builder

	script.WriteString("cat > /dev/null\n")
	script.WriteString("if [ -e '" + markerPath + "' ]; then echo 'Permission denied (publickey).' >&2; exit 255; fi\n")
	script.WriteString(": > '" + markerPath + "'\n")
	script.WriteString("printf '%s\\n'")

	for _, frame := range frames {
		script.WriteString(" '" + encodeFrame(t, frame) + "'")
	}

	script.WriteString("\n")
	script.WriteString("sequence=3\n")
	script.WriteString("while [ \"$sequence\" -lt 100 ]; do\n")
	script.WriteString("  printf '" + health + "\\n' \"$sequence\"\n")
	script.WriteString("  sequence=$((sequence + 1))\n")
	script.WriteString("  sleep 0.02\n")
	script.WriteString("done\n")

	return script.String()
}

func TestMonitorPublishesInventoryAndAppliesStaleLifecycle(t *testing.T) {
	dir, path := openTestCatalogDir(t)

	remote := testRemoteConfig()
	remote.ConnectTimeout = "200ms"
	remote.HeartbeatInterval = "20ms"
	remote.StaleAfter = "60ms"
	remote.HideAfter = "300ms"
	remote.TmuxPollInterval = "20ms"
	remote.Connections = []config.RemoteConnectionConfig{{ID: testConnectionID, Label: "Work", Host: "work.example"}}

	marker := filepath.Join(t.TempDir(), "attempted")
	script := monitorScript(
		t, marker,
		helloFrame(testPeerID, "stream", 1),
		snapshotFrame(testPeerID, "stream", 2, []session.Session{{ID: "$1", Name: testSessionName, Kind: session.KindLive, WindowCount: 2}}),
	)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- RunMonitor(ctx, MonitorOptions{
			CatalogDir: dir,
			Remote:     remote,
			Command:    scriptSSH(t, script),
		})
	}()

	catalogPath := filepath.Join(path, catalogName)

	snapshot := waitForCatalog(t, catalogPath, 10*time.Second, done, func(catalog Catalog) bool {
		state, ok := catalog.Connections[testConnectionID]

		return ok && state.Connected && len(state.Snapshot.Sessions) == 1
	})

	state := snapshot.Connections[testConnectionID]
	if state.PeerID != testPeerID || state.StreamInstance != "stream" || state.Label != "Work" {
		t.Fatalf("cached connection = %#v, want the observed peer identity", state)
	}

	if state.Snapshot.Sessions[0].Name != testSessionName || state.Snapshot.Sessions[0].WindowCount != 2 {
		t.Fatalf("cached sessions = %#v, want the observed inventory", state.Snapshot.Sessions)
	}

	waitForCatalog(t, catalogPath, 20*time.Second, done, func(catalog Catalog) bool {
		state, ok := catalog.Connections[testConnectionID]

		return ok && !state.Connected && state.LastError != "" && !state.LastErrorAt.IsZero()
	})

	waitForCatalog(t, catalogPath, 20*time.Second, done, func(catalog Catalog) bool {
		_, ok := catalog.Connections[testConnectionID]

		return !ok
	})

	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunMonitor() error = %v, want cancellation", err)
	}
}

func waitForCatalog(t *testing.T, path string, timeout time.Duration, done <-chan error, accept func(Catalog) bool) Catalog {
	t.Helper()

	deadline := time.Now().Add(timeout)

	var last Catalog

	for time.Now().Before(deadline) {
		if done != nil {
			select {
			case monitorErr := <-done:
				t.Fatalf("remote monitor exited before the catalog reached the expected state: %v", monitorErr)
			default:
			}
		}

		content, err := os.ReadFile(path)
		if err == nil && len(content) > 0 {
			var current Catalog

			if err := json.Unmarshal(content, &current); err != nil {
				t.Fatalf("catalog %q is not JSON: %v", path, err)
			}

			last = current

			if accept(current) {
				return current
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("catalog %q did not reach the expected state; last = %#v", path, last)

	return Catalog{}
}

func TestRunMonitorRequiresConfigurationAndOwner(t *testing.T) {
	dir, _ := openTestCatalogDir(t)

	if err := RunMonitor(t.Context(), MonitorOptions{CatalogDir: dir}); err == nil {
		t.Fatal("RunMonitor() accepted an invalid remote configuration")
	}

	owner, err := AcquireCatalogOwner(dir)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = owner.Close() }()

	remote := testRemoteConfig()
	remote.Enabled = false
	remote.Connections = nil

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	err = RunMonitor(ctx, MonitorOptions{CatalogDir: dir, Remote: remote})
	if !errors.Is(err, ErrCatalogOwnerRunning) {
		t.Fatalf("RunMonitor() error = %v, want %v", err, ErrCatalogOwnerRunning)
	}
}

func TestWatchRemoteConfigReloadsAtomicReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")

	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	remote := testRemoteConfig()

	reloads, err := WatchRemoteConfig(ctx, path, 20*time.Millisecond, func(_ []byte) (config.RemoteConfig, error) {
		return remote, nil
	})
	if err != nil {
		t.Fatalf("WatchRemoteConfig() error = %v", err)
	}

	first := <-reloads
	if first.Err != nil || !first.Remote.Enabled {
		t.Fatalf("first reload = %#v, want the loaded configuration", first)
	}

	temporary := filepath.Join(directory, "config.tmp")
	if err := os.WriteFile(temporary, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}

	select {
	case reload := <-reloads:
		if reload.Err != nil || !reload.Remote.Enabled {
			t.Fatalf("reload = %#v, want the replaced configuration", reload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("config watcher did not observe the atomic replacement")
	}
}

func TestWatchRemoteConfigRequiresLoader(t *testing.T) {
	if _, err := WatchRemoteConfig(t.Context(), "", time.Second, nil); !errors.Is(err, errNilConfigLoader) {
		t.Fatalf("WatchRemoteConfig() error = %v, want %v", err, errNilConfigLoader)
	}
}

func TestWatchRemoteConfigWaitsForInitiallyMissingDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reloads, err := WatchRemoteConfig(ctx, path, configWatchMinInterval, func(content []byte) (config.RemoteConfig, error) {
		remote := testRemoteConfig()
		remote.SSHPath = string(content)

		return remote, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case reload := <-reloads:
		t.Fatalf("initially missing optional config emitted a reload: %#v", reload)
	case <-time.After(2 * configWatchMinInterval):
	}

	if err := os.WriteFile(path, []byte("created"), 0o600); err != nil {
		t.Fatal(err)
	}

	assertConfigReload(t, receiveConfigReload(t, reloads, "initial creation"), "created")
}

func TestWatchRemoteConfigParsesTheObservedBytes(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")

	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reloads, err := WatchRemoteConfig(ctx, path, configWatchMinInterval, func(content []byte) (config.RemoteConfig, error) {
		if string(content) == "second" {
			if writeErr := os.WriteFile(path, []byte("third"), 0o600); writeErr != nil {
				return config.RemoteConfig{}, fmt.Errorf("replacing test config: %w", writeErr)
			}
		}

		remote := testRemoteConfig()
		remote.SSHPath = string(content)

		return remote, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	assertConfigReload(t, <-reloads, "first")

	if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	assertConfigReload(t, receiveConfigReload(t, reloads, "second generation"), "second")
	assertConfigReload(t, receiveConfigReload(t, reloads, "third generation"), "third")
}

func TestWatchRemoteConfigDeletionAndRecreationAreDistinctGenerations(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")

	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reloads, err := WatchRemoteConfig(ctx, path, configWatchMinInterval, func(content []byte) (config.RemoteConfig, error) {
		remote := testRemoteConfig()
		remote.SSHPath = string(content)

		return remote, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	assertConfigReload(t, <-reloads, "first")

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	deleted := receiveConfigReload(t, reloads, "deletion")
	if !errors.Is(deleted.Err, fs.ErrNotExist) {
		t.Fatalf("deleted reload error = %v, want not found", deleted.Err)
	}

	select {
	case repeated := <-reloads:
		t.Fatalf("unchanged deletion emitted repeatedly: %#v", repeated)
	case <-time.After(2 * configWatchMinInterval):
	}

	if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	recreated := receiveConfigReload(t, reloads, "recreation")
	if recreated.Err != nil || recreated.Remote.SSHPath != "second" {
		t.Fatalf("recreated reload = %#v", recreated)
	}
}

func assertConfigReload(t *testing.T, reload ConfigReload, sshPath string) {
	t.Helper()

	if reload.Err != nil || reload.Remote.SSHPath != sshPath {
		t.Fatalf("config reload = %#v, want SSH path %q", reload, sshPath)
	}
}

func TestWatchRemoteConfigRejectsFIFOWithoutBlockingShutdownOrRecovery(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")

	if err := os.WriteFile(path, []byte("regular"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())

	reloads, err := WatchRemoteConfig(ctx, path, configWatchMinInterval, func(_ []byte) (config.RemoteConfig, error) {
		return testRemoteConfig(), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if first := <-reloads; first.Err != nil {
		t.Fatalf("initial reload error = %v", first.Err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case special := <-reloads:
		if special.Err == nil {
			t.Fatalf("FIFO reload = %#v, want rejection", special)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher blocked while inspecting FIFO")
	}

	cancel()

	select {
	case _, ok := <-reloads:
		if ok {
			t.Fatal("watcher emitted a reload after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop after FIFO rejection")
	}
}

func receiveConfigReload(t *testing.T, reloads <-chan ConfigReload, description string) ConfigReload {
	t.Helper()

	select {
	case reload := <-reloads:
		return reload
	case <-time.After(2 * time.Second):
		t.Fatalf("watcher did not observe %s", description)

		return ConfigReload{}
	}
}
