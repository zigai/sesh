//go:build linux || darwin

package picker

import (
	"context"
	"sync"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

type initialReads struct {
	opened   chan struct{}
	closed   chan struct{}
	mu       sync.RWMutex
	conn     *gotmux.Connection
	openErr  error
	finished bool
	closeErr error
}

func openInitialReads(ctx context.Context, server *gotmux.Server, sessionID string) *initialReads {
	reads := &initialReads{
		opened:   make(chan struct{}),
		closed:   make(chan struct{}),
		mu:       sync.RWMutex{},
		conn:     nil,
		openErr:  nil,
		finished: false,
		closeErr: nil,
	}

	go func() {
		defer close(reads.opened)

		reads.conn, reads.openErr = openPickerControl(ctx, server, sessionID)
	}()

	return reads
}

func openPickerControl(ctx context.Context, server *gotmux.Server, sessionID string) (*gotmux.Connection, error) {
	conn, err := server.OpenControl(ctx, gotmux.SessionID(sessionID), gotmux.ControlOptions{
		PaneOutput:       false,
		NoEcho:           false,
		Flags:            nil,
		UTF8:             gotmux.UTF8Default,
		Colors256:        false,
		TerminalFeatures: nil,
		QueueDepth:       0,
		QueuedBytes:      0,
		FrameBytes:       0,
		EventBytes:       0,
		MaxStreams:       0,
	})
	if err != nil {
		return nil, oops.In("cli").With("operation", "open_picker_control").Wrap(err)
	}

	return conn, nil
}

func (r *initialReads) read(ctx context.Context, server *gotmux.Server, query func(*gotmux.Server) error) error {
	select {
	case <-r.opened:
	case <-ctx.Done():
		return ctx.Err()
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.finished {
		return query(server)
	}

	if r.openErr != nil {
		return r.openErr
	}

	return query(r.conn.Server())
}

func (r *initialReads) release() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.finished {
		return
	}

	r.finished = true

	go func() {
		defer close(r.closed)

		<-r.opened

		if r.conn != nil {
			if err := r.conn.Close(); err != nil {
				r.closeErr = oops.In("cli").With("operation", "close_picker_control").Wrap(err)
			}
		}
	}()
}

func (r *initialReads) finish() error {
	r.release()
	<-r.closed

	return r.closeErr
}
