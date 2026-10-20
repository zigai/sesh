//go:build linux || darwin

package agents_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/zigai/aht/v2/pkg/aht"

	"github.com/zigai/sesh/internal/agents"
)

var errWatchUnavailable = errors.New("PRIVATE_WATCH_ERROR")

type scriptedSource struct {
	mu        sync.Mutex
	calls     int
	emissions [][]aht.Session
	callCh    chan int
}

func newScriptedSource(emissions ...[]aht.Session) *scriptedSource {
	return &scriptedSource{emissions: emissions, callCh: make(chan int, 16)}
}

func (s *scriptedSource) List(context.Context, aht.Filter) ([]aht.Session, error) {
	return nil, nil
}

func (s *scriptedSource) Watch(ctx context.Context, _ aht.Filter, yield func(aht.StateSnapshot) error) error {
	s.mu.Lock()
	call := s.calls
	s.calls++
	s.mu.Unlock()

	s.callCh <- call

	if call >= len(s.emissions) {
		<-ctx.Done()

		return nil
	}

	return yield(aht.StateSnapshot{Revision: 1, Sessions: s.emissions[call]})
}

func (s *scriptedSource) callsMade() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.calls
}

type failingSource struct {
	mu       sync.Mutex
	failures int
	calls    int
	callCh   chan int
}

func newFailingSource(failures int) *failingSource {
	return &failingSource{failures: failures, callCh: make(chan int, 16)}
}

func (s *failingSource) List(context.Context, aht.Filter) ([]aht.Session, error) {
	return nil, errWatchUnavailable
}

func (s *failingSource) Watch(ctx context.Context, _ aht.Filter, _ func(aht.StateSnapshot) error) error {
	s.mu.Lock()
	call := s.calls
	s.calls++
	s.mu.Unlock()

	s.callCh <- call

	if call < s.failures {
		return errWatchUnavailable
	}

	<-ctx.Done()

	return nil
}

type recordingHandler struct {
	mu        sync.Mutex
	handled   [][]aht.Session
	clearCall int
	clearErr  error
}

func (h *recordingHandler) HandleSnapshot(_ context.Context, sessions []aht.Session) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.handled = append(h.handled, sessions)

	return nil
}

func (h *recordingHandler) ClearAll(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.clearCall++

	return h.clearErr
}

func (h *recordingHandler) handledCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.handled)
}

func (h *recordingHandler) clearCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.clearCall
}

func TestWatchLoopDeliversEverySnapshot(t *testing.T) {
	t.Parallel()

	source := newScriptedSource(
		[]aht.Session{trackerSession("%1", "@1", aht.ActivityIdle, aht.PresenceLive)},
		[]aht.Session{
			trackerSession("%1", "@1", aht.ActivityIdle, aht.PresenceLive),
			trackerSession("%2", "@2", aht.ActivityWaiting, aht.PresenceLive),
		},
	)
	handler := &recordingHandler{}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	opts := agents.DefaultWatchOptions()
	opts.Quiet = true

	done := make(chan struct{})

	go func() {
		agents.RunWatchLoop(ctx, source, handler, opts)
		close(done)
	}()

	waitForCalls(t, source.callCh, 2)
	cancel()
	waitDone(t, done)

	if handler.handledCount() != 2 {
		t.Fatalf("handled snapshots = %d, want 2", handler.handledCount())
	}
}

func TestWatchLoopRetriesUnavailableBroker(t *testing.T) {
	t.Parallel()

	source := newFailingSource(3)
	handler := &recordingHandler{}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	opts := agents.DefaultWatchOptions()
	opts.InitialBackoff = time.Millisecond
	opts.MaxBackoff = 5 * time.Millisecond
	opts.Quiet = true

	done := make(chan struct{})

	go func() {
		agents.RunWatchLoop(ctx, source, handler, opts)
		close(done)
	}()

	waitForCalls(t, source.callCh, 3)
	cancel()
	waitDone(t, done)

	if handler.handledCount() != 0 {
		t.Fatalf("handled snapshots = %d, want 0 while the broker is unavailable", handler.handledCount())
	}
}

func TestWatchLoopReturnsAfterCancellation(t *testing.T) {
	t.Parallel()

	source := newScriptedSource()
	handler := &recordingHandler{}

	ctx, cancel := context.WithCancel(t.Context())

	opts := agents.DefaultWatchOptions()
	opts.Quiet = true

	done := make(chan struct{})

	go func() {
		agents.RunWatchLoop(ctx, source, handler, opts)
		close(done)
	}()

	waitForCalls(t, source.callCh, 1)
	cancel()
	waitDone(t, done)

	if source.callsMade() != 1 {
		t.Fatalf("watch calls = %d, want 1", source.callsMade())
	}
}

func TestRunStatusTrackerClearsStylesOnShutdown(t *testing.T) {
	t.Parallel()

	source := newScriptedSource()
	handler := &recordingHandler{}

	ctx, cancel := context.WithCancel(t.Context())

	opts := agents.DefaultWatchOptions()
	opts.Quiet = true

	done := make(chan struct{})

	go func() {
		if err := agents.RunStatusTracker(ctx, source, handler, opts, true); err != nil {
			t.Errorf("RunStatusTracker failed: %v", err)
		}

		close(done)
	}()

	waitForCalls(t, source.callCh, 1)
	cancel()
	waitDone(t, done)

	if handler.clearCalls() != 1 {
		t.Fatalf("clear calls = %d, want 1", handler.clearCalls())
	}
}

func TestRunStatusTrackerReturnsAndLogsCleanupFailure(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	ctx, cancel := context.WithCancel(zerolog.New(&output).WithContext(t.Context()))
	source := newScriptedSource()
	handler := &recordingHandler{clearErr: errWatchUnavailable}
	opts := agents.DefaultWatchOptions()
	opts.Quiet = true
	done := make(chan error, 1)

	go func() { done <- agents.RunStatusTracker(ctx, source, handler, opts, true) }()

	waitForCalls(t, source.callCh, 1)
	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for tracker cleanup")
	}

	if !errors.Is(err, errWatchUnavailable) {
		t.Fatalf("RunStatusTracker error = %v, want cleanup cause", err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("log record count = %d, want 1: %s", len(lines), output.String())
	}

	var record map[string]any
	if decodeErr := json.Unmarshal([]byte(lines[0]), &record); decodeErr != nil {
		t.Fatalf("decode cleanup log: %v", decodeErr)
	}

	for field, want := range map[string]any{
		"level": "warn", "component": "agents", "operation": "clear_styles",
		"status": "uncertain", "reason": "cleanup_failed", "error": errWatchUnavailable.Error(),
	} {
		if got := record[field]; got != want {
			t.Fatalf("log field %q = %#v, want %#v: %#v", field, got, want, record)
		}
	}
}

func waitForCalls(t *testing.T, calls <-chan int, want int) {
	t.Helper()

	deadline := time.After(10 * time.Second)

	for {
		select {
		case <-calls:
			want--

			if want <= 0 {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %d watch calls", want)
		}
	}
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the watch loop to return")
	}
}
