//go:build linux

package lifecycle

import (
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

type boundedSignalHandler struct {
	mu            sync.Mutex
	registrations map[chan<- *dbus.Signal]struct{}
	received      map[*dbus.Signal]time.Time
	overflow      chan struct{}
	closed        bool
	failed        bool
}

func newBoundedSignalHandler() *boundedSignalHandler {
	return &boundedSignalHandler{
		mu:            sync.Mutex{},
		registrations: make(map[chan<- *dbus.Signal]struct{}),
		received:      make(map[*dbus.Signal]time.Time),
		overflow:      make(chan struct{}),
		closed:        false,
		failed:        false,
	}
}

func (h *boundedSignalHandler) DeliverSignal(_ string, _ string, signal *dbus.Signal) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed || h.failed {
		return
	}

	receivedAt := time.Now()
	for destination := range h.registrations {
		h.received[signal] = receivedAt

		select {
		case destination <- signal:
		case <-h.overflow:
			return
		default:
			// godbus's default handler would start a goroutine per full channel; admission fails
			// closed on the first overflow instead.
			delete(h.received, signal)
			h.failed = true
			close(h.overflow)

			return
		}
	}
}

func (h *boundedSignalHandler) AddSignal(destination chan<- *dbus.Signal) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		close(destination)

		return
	}

	h.registrations[destination] = struct{}{}
}

func (h *boundedSignalHandler) RemoveSignal(destination chan<- *dbus.Signal) {
	h.mu.Lock()
	defer h.mu.Unlock()

	delete(h.registrations, destination)
	clear(h.received)
}

func (h *boundedSignalHandler) Terminate() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	for destination := range h.registrations {
		close(destination)
	}

	clear(h.registrations)
	clear(h.received)
	h.closed = true
}

func (h *boundedSignalHandler) receivedAt(signal *dbus.Signal) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	receivedAt, ok := h.received[signal]
	delete(h.received, signal)

	return receivedAt, ok
}

func (h *boundedSignalHandler) overflowed() bool {
	select {
	case <-h.overflow:
		return true
	default:
		return false
	}
}
