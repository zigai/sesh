//go:build linux || darwin

package remote

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/work"
)

func TestLoadOrCreatePeerIDPersistsIdentity(t *testing.T) {
	dir, path := openTestCatalogDir(t)

	first, err := LoadOrCreatePeerID(dir)
	if err != nil {
		t.Fatalf("LoadOrCreatePeerID() error = %v", err)
	}

	if !ValidIdentifier(first) || len(first) != 32 {
		t.Fatalf("peer ID = %q, want a 32-character identifier", first)
	}

	info, err := os.Stat(filepath.Join(path, peerIDName))
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("peer ID mode = %o, want 0600", info.Mode().Perm())
	}

	second, err := LoadOrCreatePeerID(dir)
	if err != nil {
		t.Fatal(err)
	}

	if second != first {
		t.Fatalf("reloaded peer ID = %q, want %q", second, first)
	}
}

func TestLoadOrCreatePeerIDRejectsCorruptIdentity(t *testing.T) {
	dir, path := openTestCatalogDir(t)

	if err := os.WriteFile(filepath.Join(path, peerIDName), []byte("bad\x1bidentity"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOrCreatePeerID(dir); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("LoadOrCreatePeerID() error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestPeerStatePathsArePrivate(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")

	bridgeDir, err := BridgeDir(stateHome)
	if err != nil {
		t.Fatalf("BridgeDir() error = %v", err)
	}

	want := filepath.Join(stateHome, "sesh", "remote", "bridge")
	if bridgeDir != want {
		t.Fatalf("BridgeDir() = %q, want %q", bridgeDir, want)
	}

	info, err := os.Stat(bridgeDir)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o700 {
		t.Fatalf("bridge directory mode = %o, want 0700", info.Mode().Perm())
	}

	receipts, err := InvocationDir(stateHome)
	if err != nil {
		t.Fatalf("InvocationDir() error = %v", err)
	}

	if receipts != filepath.Join(stateHome, "sesh", "remote", "receipts") {
		t.Fatalf("InvocationDir() = %q, want the receipts directory", receipts)
	}

	if _, err := PeerStateDir(""); !errors.Is(err, ErrIncompleteDependencies) {
		t.Fatalf("PeerStateDir(empty) error = %v, want %v", err, ErrIncompleteDependencies)
	}
}

var errTestRegistry = errors.New("registry failed")

type inventoryHarness struct {
	frames chan Frame
	done   chan error
	cancel context.CancelFunc
}

func startInventoryStream(t *testing.T, opts InventoryOptions) *inventoryHarness {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())

	reader, writer := io.Pipe()

	harness := &inventoryHarness{
		frames: make(chan Frame, 64),
		done:   make(chan error, 1),
		cancel: cancel,
	}

	go func() {
		defer close(harness.done)

		err := RunInventoryStream(ctx, opts, writer)
		_ = writer.Close()

		harness.done <- err
	}()

	go func() {
		defer close(harness.frames)

		frameReader := NewFrameReader(reader)

		for {
			frame, err := frameReader.Read()
			if err != nil {
				return
			}

			harness.frames <- frame
		}
	}()

	t.Cleanup(func() {
		cancel()

		_ = reader.Close()
	})

	return harness
}

func (harness *inventoryHarness) next(t *testing.T) Frame {
	t.Helper()

	timeout := 5 * time.Second

	select {
	case frame, ok := <-harness.frames:
		if !ok {
			t.Fatal("inventory stream closed before the expected frame")

			return Frame{}
		}

		return frame
	case <-time.After(timeout):
		t.Fatal("inventory stream produced no frame")

		return Frame{}
	}
}

func TestInventoryStreamPublishesHelloAndInventory(t *testing.T) {
	live := []session.Session{{
		ID: "$1", Name: testSessionName, Kind: session.KindLive, TmuxServerSocket: "/tmp/tmux-work", WindowCount: 2,
		RuntimeIdentity: session.RuntimeIdentity{TmuxServerPID: 42, TmuxServerStartedAt: time.Unix(1, 0).UTC(), TmuxSessionCreatedAt: time.Unix(2, 0).UTC()},
	}}

	saved := []session.Session{{ID: "$9", Name: "archive", Kind: session.KindSaved}}
	summaries := []session.AgentSummary{{
		TmuxServerSocket: "/tmp/tmux-work", TmuxSessionID: "$1", TmuxSessionName: testSessionName,
		Total: 2, Active: 1, Waiting: 1, Agents: []session.AgentSession{{PaneID: "%0", Status: session.AgentStatusWaiting}},
	}}

	harness := startInventoryStream(t, InventoryOptions{
		PeerID:            testPeerID,
		Hostname:          "remote-host",
		BridgeDir:         "/home/remote/.local/state/sesh/remote/bridge",
		TmuxSocket:        "/tmp/tmux-work",
		PollInterval:      10 * time.Millisecond,
		HeartbeatInterval: time.Hour,
		Sessions:          func(context.Context) ([]session.Session, error) { return live, nil },
		Agents:            func(context.Context) ([]session.AgentSummary, error) { return summaries, nil },
		SavedSessions:     func(context.Context) ([]session.Session, error) { return saved, nil },
	})

	hello := harness.next(t)
	assertInventoryHello(t, hello)

	snapshot := harness.next(t)
	assertInventorySnapshot(t, snapshot)

	harness.cancel()

	if err := <-harness.done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunInventoryStream() error = %v, want cancellation", err)
	}
}

func TestInventoryStreamPublishesTasksWithoutTheirResumeCommand(t *testing.T) {
	record := task.EmptyRecord()
	record.ID, record.Kind, record.State, record.Harness = "t3", task.KindProject, task.StateSleeping, "pi"
	record.Resume = task.Resume{Harness: "pi", Session: "s1", Command: []string{"pi", "--session", "s1"}}
	live := task.Live{
		Record: record, Present: true, SessionID: "$1", SessionName: testSessionName, WindowID: "@2", WindowIndex: 2,
		WindowName: "3", AgentPane: "%4", AgentPID: 0,
	}

	harness := startInventoryStream(t, InventoryOptions{
		PeerID: testPeerID, Hostname: "remote-host", BridgeDir: "", TmuxSocket: "/tmp/tmux-work",
		PollInterval: 10 * time.Millisecond, HeartbeatInterval: time.Hour,
		Sessions: func(context.Context) ([]session.Session, error) { return nil, nil },
		Tasks:    func(context.Context) ([]TaskRow, error) { return []TaskRow{NewTaskRow(live)}, nil },
	})

	harness.next(t)
	snapshot := harness.next(t).Snapshot

	if snapshot == nil || len(snapshot.Tasks) != 1 {
		t.Fatalf("snapshot = %+v, want the peer's task", snapshot)
	}

	row := snapshot.Tasks[0]
	if row.Record.ID != "t3" || row.Record.State != task.StateSleeping || row.AgentPane != "%4" || row.WindowID != "@2" {
		t.Fatalf("task row = %+v, want t3 asleep in pane %%4", row)
	}

	if len(row.Record.Resume.Command) != 0 {
		t.Fatalf("task row carries the resume command %q", row.Record.Resume.Command)
	}
}

func TestInventoryStreamMergesAgentsByNameAndSkipsLiveSavedDuplicates(t *testing.T) {
	live := []session.Session{{ID: "$1", Name: testSessionName, Kind: session.KindLive, TmuxServerSocket: "/tmp/tmux-work"}}
	saved := []session.Session{
		{ID: "$9", Name: testSessionName, Kind: session.KindSaved},
		{ID: "$10", Name: "archive", Kind: session.KindSaved},
	}

	summaries := []session.AgentSummary{{
		TmuxServerSocket: "/tmp/tmux-work", TmuxSessionName: testSessionName,
		Total: 1, Agents: []session.AgentSession{{PaneID: "%0", Status: session.AgentStatusIdle}},
	}}

	harness := startInventoryStream(t, InventoryOptions{
		PeerID:            testPeerID,
		TmuxSocket:        "/tmp/tmux-work",
		PollInterval:      10 * time.Millisecond,
		HeartbeatInterval: time.Hour,
		Sessions:          func(context.Context) ([]session.Session, error) { return live, nil },
		Agents:            func(context.Context) ([]session.AgentSummary, error) { return summaries, nil },
		SavedSessions:     func(context.Context) ([]session.Session, error) { return saved, nil },
	})

	_ = harness.next(t)

	snapshot := harness.next(t)

	sessions := snapshot.Snapshot.Sessions
	if len(sessions) != 2 {
		t.Fatalf("snapshot sessions = %#v, want the live session and the saved archive", sessions)
	}

	if sessions[0].Name != testSessionName || sessions[0].Kind != session.KindLive || sessions[0].AgentTotal != 1 {
		t.Fatalf("live session = %#v, want agents merged by session name", sessions[0])
	}

	if sessions[1].Name != "archive" {
		t.Fatalf("saved sessions = %#v, want the saved name that is not live", sessions[1])
	}
}

func TestInventoryStreamReportsIndependentSourceHealth(t *testing.T) {
	failures := atomic.Int32{}

	harness := startInventoryStream(t, InventoryOptions{
		PeerID:            testPeerID,
		PollInterval:      10 * time.Millisecond,
		HeartbeatInterval: time.Hour,
		Sessions:          func(context.Context) ([]session.Session, error) { return nil, nil },
		Agents:            func(context.Context) ([]session.AgentSummary, error) { return nil, errTestRegistry },
		SavedSessions: func(context.Context) ([]session.Session, error) {
			failures.Add(1)
			return nil, context.DeadlineExceeded
		},
		Tasks: func(context.Context) ([]TaskRow, error) { return make([]TaskRow, MaxTasks+1), nil },
		Work:  func(context.Context) ([]work.Item, error) { return nil, errTestRegistry },
	})

	_ = harness.next(t)

	snapshot := harness.next(t)
	health := snapshot.Snapshot.Health

	if health.Tasks.Status != HealthDegraded || health.Tasks.Reason != HealthReasonInvalidData {
		t.Fatalf("task health = %#v, want degraded/invalid data", health.Tasks)
	}

	if health.Work.Status != HealthDegraded || health.Work.Reason != HealthReasonUnavailable {
		t.Fatalf("work health = %#v, want degraded/unavailable", health.Work)
	}

	if health.Tmux.Status != HealthOK || health.Tmux.Reason != HealthReasonNone {
		t.Fatalf("tmux health = %#v, want ok", health.Tmux)
	}

	if health.Agents.Status != HealthDegraded || health.Agents.Reason != HealthReasonUnavailable {
		t.Fatalf("agent health = %#v, want degraded/unavailable", health.Agents)
	}

	if health.Snapshots.Status != HealthDegraded || health.Snapshots.Reason != HealthReasonTimeout {
		t.Fatalf("snapshot health = %#v, want degraded/timeout", health.Snapshots)
	}
}

func TestInventoryStreamSkipsUnchangedInventoryAndEmitsHeartbeats(t *testing.T) {
	harness := startInventoryStream(t, InventoryOptions{
		PeerID:            testPeerID,
		PollInterval:      10 * time.Millisecond,
		HeartbeatInterval: 25 * time.Millisecond,
		Sessions:          func(context.Context) ([]session.Session, error) { return nil, nil },
	})

	_ = harness.next(t)
	_ = harness.next(t)

	frame := harness.next(t)
	if frame.Type != FrameHealth || frame.Health == nil {
		t.Fatalf("frame = %#v, want a health heartbeat instead of a repeated snapshot", frame)
	}

	if frame.Sequence <= 2 {
		t.Fatalf("heartbeat sequence = %d, want a monotonic sequence", frame.Sequence)
	}
}

func TestInventoryStreamRequiresNativeSources(t *testing.T) {
	if err := RunInventoryStream(t.Context(), InventoryOptions{PeerID: testPeerID}, io.Discard); !errors.Is(err, ErrIncompleteDependencies) {
		t.Fatalf("RunInventoryStream() error = %v, want %v", err, ErrIncompleteDependencies)
	}

	if err := RunInventoryStream(t.Context(), InventoryOptions{PeerID: testPeerID, Sessions: func(context.Context) ([]session.Session, error) { return nil, nil }}, nil); !errors.Is(err, errNilStreamOutput) {
		t.Fatalf("RunInventoryStream() error = %v, want %v", err, errNilStreamOutput)
	}
}

func TestInventoryStreamBoundsInventory(t *testing.T) {
	oversized := make([]session.Session, MaxSessions+1)
	for index := range oversized {
		oversized[index] = session.Session{ID: "s", Name: "s", Kind: session.KindSaved}
	}

	err := RunInventoryStream(t.Context(), InventoryOptions{
		PeerID:   testPeerID,
		Sessions: func(context.Context) ([]session.Session, error) { return oversized, nil },
	}, io.Discard)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("RunInventoryStream() error = %v, want %v", err, ErrFrameTooLarge)
	}
}

func TestInventoryStreamUsesConfiguredRemoteEntryPoint(t *testing.T) {
	connection := config.RemoteConnectionConfig{ID: testConnectionID, Host: "work.example", SeshPath: "/opt/sesh"}

	if entry := PeerEntryPoint(connection, ChannelStream); entry[0] != "/opt/sesh" || entry[3] != ChannelStream {
		t.Fatalf("stream entry point = %#v", entry)
	}

	if entry := PeerEntryPoint(connection, ChannelAttachPrepare); entry[3] != ChannelAttachPrepare {
		t.Fatalf("prepare entry point = %#v", entry)
	}
}

func assertInventoryHello(t *testing.T, hello Frame) {
	t.Helper()

	if hello.Type != FrameHello || hello.PeerID != testPeerID || hello.Hello == nil ||
		hello.Hello.Hostname != "remote-host" || hello.Hello.BridgeDir != "/home/remote/.local/state/sesh/remote/bridge" {
		t.Fatalf("hello frame = %#v, want the peer identity and bridge directory", hello)
	}

	if hello.Sequence != 1 {
		t.Fatalf("hello sequence = %d, want 1", hello.Sequence)
	}
}

func assertInventorySnapshot(t *testing.T, snapshot Frame) {
	t.Helper()

	if snapshot.Type != FrameSnapshot || snapshot.Snapshot == nil {
		t.Fatalf("snapshot frame = %#v, want an inventory", snapshot)
	}

	if snapshot.Sequence != 2 {
		t.Fatalf("snapshot sequence = %d, want 2", snapshot.Sequence)
	}

	assertSnapshotSessions(t, snapshot.Snapshot.Sessions)

	if !ValidSourceHealth(snapshot.Snapshot.Health) || snapshot.Snapshot.Health.Tmux.Status != HealthOK {
		t.Fatalf("snapshot health = %#v, want a healthy tmux source", snapshot.Snapshot.Health)
	}
}

func assertSnapshotSessions(t *testing.T, sessions []session.Session) {
	t.Helper()

	if len(sessions) != 2 {
		t.Fatalf("snapshot sessions = %#v, want the live and saved inventory", sessions)
	}

	if sessions[0].AgentTotal != 2 || sessions[0].AgentWaiting != 1 || len(sessions[0].Agents) != 1 {
		t.Fatalf("live session agents = %#v, want the merged agent summary", sessions[0])
	}

	if sessions[1].Name != "archive" || sessions[1].Kind != session.KindSaved || sessions[1].TmuxServerSocket != "/tmp/tmux-work" {
		t.Fatalf("saved session = %#v, want the saved inventory bound to the socket", sessions[1])
	}
}
