//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func healthyHealth() SourceHealth {
	status := SourceStatus{Status: HealthOK, Reason: HealthReasonNone}

	return SourceHealth{Tmux: status, Agents: status, Snapshots: status}
}

func helloFrame(peerID, streamInstance string, sequence uint64) Frame {
	return Frame{
		Version: ProtocolVersion, Type: FrameHello, PeerID: peerID, StreamInstance: streamInstance, Sequence: sequence,
		Hello: &Hello{PeerID: peerID, StreamInstance: streamInstance, Hostname: "remote", StartedAt: time.Unix(1, 0).UTC()},
	}
}

func snapshotFrame(peerID, streamInstance string, sequence uint64, sessions []session.Session) Frame {
	return Frame{
		Version: ProtocolVersion, Type: FrameSnapshot, PeerID: peerID, StreamInstance: streamInstance, Sequence: sequence,
		Snapshot: &Snapshot{GeneratedAt: time.Unix(2, 0).UTC(), Sessions: sessions, Health: healthyHealth()},
	}
}

func encodeFrame(t *testing.T, frame Frame) string {
	t.Helper()

	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.ContainsAny(encoded, "'\n") {
		t.Fatalf("frame fixture needs quoting support: %s", encoded)
	}

	return string(encoded)
}

func frameScript(t *testing.T, frames ...Frame) string {
	t.Helper()

	var script strings.Builder

	script.WriteString("printf '%s\\n'")

	for _, frame := range frames {
		script.WriteString(" '" + encodeFrame(t, frame) + "'")
	}

	script.WriteString("\nexec sleep 60\n")

	return script.String()
}

func streamTestConfig() config.RemoteConfig {
	remote := testRemoteConfig()
	remote.ConnectTimeout = "200ms"

	return remote
}

func TestStreamTimesOutBeforeInitialHello(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	remote := streamTestConfig()

	var (
		mu       sync.Mutex
		failures []error
	)

	yield := func(frame Frame, streamErr error, attempted bool) error {
		if attempted || streamErr == nil {
			return nil
		}

		mu.Lock()

		failures = append(failures, streamErr)
		mu.Unlock()

		cancel()

		return nil
	}

	startedAt := time.Now()

	err := Stream(ctx, StreamOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		Bootstrap:    StreamBootstrapFor(remote, remote.Connections[0]),
		Command:      scriptSSH(t, "exec sleep 60"),
		Yield:        yield,
		HelloTimeout: 20 * time.Millisecond,
		FrameTimeout: 20 * time.Millisecond,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream() error = %v, want cancellation", err)
	}

	if time.Since(startedAt) > 5*time.Second {
		t.Fatal("silent stream exceeded its bounded hello deadline")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(failures) == 0 || !errors.Is(failures[0], context.DeadlineExceeded) {
		t.Fatalf("stream failures = %v, want a hello deadline", failures)
	}
}

func TestStreamDeliversFramesAndReconnectsAfterSilence(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	remote := streamTestConfig()

	script := frameScript(
		t,
		helloFrame("peer", "stream", 1),
		snapshotFrame("peer", "stream", 2, []session.Session{{ID: "$1", Name: "dev", Kind: session.KindLive}}),
	)

	var (
		mu        sync.Mutex
		attempts  int
		frames    []Frame
		failures  []error
		snapshots int
	)

	yield := func(frame Frame, streamErr error, attempted bool) error {
		mu.Lock()
		defer mu.Unlock()

		if attempted {
			attempts++
			if attempts >= 2 {
				cancel()
			}

			return nil
		}

		if streamErr != nil {
			failures = append(failures, streamErr)

			return nil
		}

		frames = append(frames, frame)

		if frame.Type == FrameSnapshot {
			snapshots++
		}

		return nil
	}

	_ = Stream(ctx, StreamOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		Bootstrap:    StreamBootstrapFor(remote, remote.Connections[0]),
		Command:      scriptSSH(t, script),
		Yield:        yield,
		HelloTimeout: 200 * time.Millisecond,
		FrameTimeout: 50 * time.Millisecond,
	})

	mu.Lock()
	defer mu.Unlock()

	if attempts < 2 {
		t.Fatalf("stream attempts = %d, want a reconnect after the silent stream", attempts)
	}

	if len(frames) != 2 || frames[0].Type != FrameHello || frames[1].Type != FrameSnapshot {
		t.Fatalf("stream frames = %#v, want a hello and one snapshot", frames)
	}

	if snapshots != 1 {
		t.Fatalf("accepted snapshots = %d, want 1", snapshots)
	}

	if len(failures) == 0 || !errors.Is(failures[0], context.DeadlineExceeded) {
		t.Fatalf("stream failures = %v, want the silent stream deadline", failures)
	}
}

func TestStreamRejectsSecondHelloForOneIncarnation(t *testing.T) {
	remote := streamTestConfig()

	script := frameScript(
		t,
		helloFrame("peer", "stream", 1),
		helloFrame("peer", "stream", 2),
	)

	var (
		frames   []Frame
		failures []error
	)

	yield := func(frame Frame, streamErr error, attempted bool) error {
		if attempted {
			return nil
		}

		if streamErr != nil {
			failures = append(failures, streamErr)

			return context.Canceled
		}

		frames = append(frames, frame)

		return nil
	}

	err := Stream(t.Context(), StreamOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		Bootstrap:    StreamBootstrapFor(remote, remote.Connections[0]),
		Command:      scriptSSH(t, script),
		Yield:        yield,
		HelloTimeout: time.Second,
		FrameTimeout: time.Second,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream() error = %v, want the caller's stop", err)
	}

	if len(frames) != 1 || frames[0].Type != FrameHello {
		t.Fatalf("forwarded frames = %#v, want only the initial hello", frames)
	}

	if len(failures) == 0 || !errors.Is(failures[0], ErrInvalidFrame) {
		t.Fatalf("stream failures = %v, want %v", failures, ErrInvalidFrame)
	}
}

func TestStreamRejectsFramesFromAnotherIncarnation(t *testing.T) {
	remote := streamTestConfig()

	script := frameScript(
		t,
		helloFrame("peer", "stream", 1),
		snapshotFrame("peer", "other-stream", 2, nil),
	)

	var failures []error

	yield := func(frame Frame, streamErr error, attempted bool) error {
		if attempted || streamErr == nil {
			return nil
		}

		failures = append(failures, streamErr)

		return context.Canceled
	}

	_ = Stream(t.Context(), StreamOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		Bootstrap:    StreamBootstrapFor(remote, remote.Connections[0]),
		Command:      scriptSSH(t, script),
		Yield:        yield,
		HelloTimeout: time.Second,
		FrameTimeout: time.Second,
	})

	if len(failures) == 0 || !errors.Is(failures[0], ErrInvalidFrame) {
		t.Fatalf("stream failures = %v, want %v", failures, ErrInvalidFrame)
	}
}

func TestStreamWritesBootstrapOnStandardInput(t *testing.T) {
	remote := streamTestConfig()

	bootstrapPath := filepath.Join(t.TempDir(), "bootstrap.json")

	script := "cat > " + bootstrapPath + "\n" + frameScript(t, helloFrame("peer", "stream", 1))

	command := scriptSSH(t, script)

	var capturedArguments []string

	envCommand := func(ctx context.Context, executable string, arguments ...string) *exec.Cmd {
		capturedArguments = append([]string(nil), arguments...)

		cmd := command(ctx, executable, arguments...)

		cmd.Env = append(os.Environ(), "SESH_TEST_BOOTSTRAP=1")

		return cmd
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	yield := func(frame Frame, streamErr error, attempted bool) error {
		if !attempted && frame.Type == FrameHello {
			cancel()
		}

		return nil
	}

	_ = Stream(ctx, StreamOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		Bootstrap:    StreamBootstrapFor(remote, remote.Connections[0]),
		Command:      envCommand,
		Yield:        yield,
		HelloTimeout: time.Second,
		FrameTimeout: time.Second,
	})

	content, err := os.ReadFile(bootstrapPath)
	if err != nil {
		t.Fatalf("reading the peer bootstrap: %v", err)
	}

	expected, err := EncodeStreamBootstrap(StreamBootstrapFor(remote, remote.Connections[0]))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(bytes.TrimSpace(content), expected) {
		t.Fatalf("peer bootstrap = %q, want %q", content, expected)
	}

	for _, argument := range capturedArguments {
		if strings.Contains(argument, string(expected)) || strings.Contains(argument, "pollInterval") {
			t.Fatalf("bootstrap material traveled in argv: %q", argument)
		}
	}
}

type blockingFrameStream struct {
	started chan struct{}
	closed  chan struct{}
}

func (stream *blockingFrameStream) Read([]byte) (int, error) {
	close(stream.started)

	<-stream.closed

	return 0, io.EOF
}

func (stream *blockingFrameStream) Close() error {
	close(stream.closed)

	return nil
}

func TestReadFrameWithTimeoutClosesAndJoinsTimedOutRead(t *testing.T) {
	stream := &blockingFrameStream{started: make(chan struct{}), closed: make(chan struct{})}

	if _, err := readFrameWithTimeout(t.Context(), NewFrameReader(stream), stream, 20*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readFrameWithTimeout() error = %v, want deadline", err)
	}

	select {
	case <-stream.started:
	default:
		t.Fatal("timed-out frame read never started")
	}

	select {
	case <-stream.closed:
	default:
		t.Fatal("timed-out frame read was not closed before return")
	}
}

func TestStreamIdentityRequiresMonotonicSequence(t *testing.T) {
	var identity StreamIdentity

	if err := identity.Accept(snapshotFrame("peer", "stream", 1, nil)); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("Accept(snapshot first) error = %v, want %v", err, ErrInvalidFrame)
	}

	if err := identity.Accept(helloFrame("peer", "stream", 1)); err != nil {
		t.Fatalf("Accept(hello) error = %v", err)
	}

	if err := identity.Accept(helloFrame("peer", "stream", 2)); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("Accept(second hello) error = %v, want %v", err, ErrInvalidFrame)
	}

	if err := identity.Accept(snapshotFrame("peer", "stream", 2, nil)); err != nil {
		t.Fatalf("Accept(snapshot) error = %v", err)
	}

	if err := identity.Accept(snapshotFrame("peer", "stream", 2, nil)); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("Accept(repeated sequence) error = %v, want %v", err, ErrInvalidFrame)
	}

	if err := identity.Accept(Frame{Version: ProtocolVersion, Type: FrameHealth, PeerID: "peer", StreamInstance: "stream", Sequence: 3, Health: &Health{Sources: healthyHealth()}}); err != nil {
		t.Fatalf("Accept(health) error = %v", err)
	}
}
