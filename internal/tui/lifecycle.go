package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultShutdownTimeout = 2 * time.Second
)

type lifecycle struct {
	done        chan struct{}
	deadline    time.Time
	hasDeadline bool
	mu          sync.RWMutex
	err         error
	stopParent  func() bool
	closeOnce   sync.Once
	wg          sync.WaitGroup
	closed      atomic.Bool
}

func newLifecycle(parent context.Context) *lifecycle {
	done := make(chan struct{})
	deadline, hasDeadline := parent.Deadline()

	owner := &lifecycle{
		done:        done,
		deadline:    deadline,
		hasDeadline: hasDeadline,
		mu:          sync.RWMutex{},
		err:         nil,
		stopParent:  nil,
		closeOnce:   sync.Once{},
		wg:          sync.WaitGroup{},
		closed:      atomic.Bool{},
	}
	owner.stopParent = context.AfterFunc(parent, func() {
		owner.closeWithError(parent.Err())
	})

	return owner
}

func (l *lifecycle) BeginWorker() bool {
	if l == nil {
		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed.Load() || l.err != nil {
		return false
	}

	l.wg.Add(1)

	return true
}

func (l *lifecycle) EndWorker() {
	if l == nil {
		return
	}

	l.wg.Done()
}

func (l *lifecycle) Closed() bool {
	if l == nil {
		return true
	}

	return l.closed.Load() || l.Err() != nil
}

func (l *lifecycle) Close() bool {
	return l.CloseWithTimeout(defaultShutdownTimeout)
}

func (l *lifecycle) CloseWithTimeout(timeout time.Duration) bool {
	if l == nil {
		return true
	}

	l.mu.Lock()
	l.closed.Store(true)
	l.mu.Unlock()

	if l.stopParent != nil {
		l.stopParent()
	}

	l.closeWithError(context.Canceled)

	joined := make(chan struct{})

	go func() {
		l.wg.Wait()
		close(joined)
	}()

	select {
	case <-joined:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (l *lifecycle) Deadline() (time.Time, bool) {
	return l.deadline, l.hasDeadline
}

func (l *lifecycle) Done() <-chan struct{} {
	return l.done
}

func (l *lifecycle) Err() error {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return l.err
}

func (l *lifecycle) Value(any) any {
	return nil
}

func (l *lifecycle) closeWithError(err error) {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
		close(l.done)
	})
}
