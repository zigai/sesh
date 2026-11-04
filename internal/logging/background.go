//go:build linux || darwin

package logging

import (
	"errors"
	"sync"

	"github.com/rs/zerolog"
)

// BackgroundSink takes records without touching the disk, for commands a person is waiting on. Its
// goroutine opens the log file and writes to it, so an open failure surfaces from Close and a crash
// loses the records still queued.
type BackgroundSink struct {
	//exhaustruct:optional
	mu sync.Mutex

	logger  zerolog.Logger
	queued  [][]byte
	wake    chan struct{}
	closed  bool
	done    chan struct{}
	failure error
}

func OpenBackground(path string) *BackgroundSink {
	sink := &BackgroundSink{
		logger:  zerolog.Logger{},
		queued:  nil,
		wake:    make(chan struct{}, 1),
		closed:  false,
		done:    make(chan struct{}),
		failure: nil,
	}
	sink.logger = zerolog.New(sink).Level(zerolog.InfoLevel).Hook(timestampHook{})

	go sink.run(path)

	return sink
}

func (s *BackgroundSink) Logger() zerolog.Logger {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.logger
}

func (s *BackgroundSink) SetLevel(level zerolog.Level) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logger = s.logger.Level(level)
}

func (s *BackgroundSink) Write(p []byte) (int, error) {
	event := append([]byte(nil), p...)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return len(p), nil
	}

	s.queued = append(s.queued, event)

	select {
	case s.wake <- struct{}{}:
	default:
	}

	return len(p), nil
}

// Close waits until every queued record has reached the log file.
func (s *BackgroundSink) Close() error {
	s.mu.Lock()

	if !s.closed {
		s.closed = true
		close(s.wake)
	}

	s.mu.Unlock()

	<-s.done

	return s.failure
}

func (s *BackgroundSink) run(path string) {
	defer close(s.done)

	sink, err := Open(path)
	if err != nil {
		s.failure = err

		return
	}

	var writeErr error

	for {
		_, open := <-s.wake

		for _, event := range s.take() {
			if _, err := sink.Write(event); err != nil {
				writeErr = errors.Join(writeErr, err)
			}
		}

		if !open {
			s.failure = errors.Join(writeErr, sink.Close())

			return
		}
	}
}

func (s *BackgroundSink) take() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	events := s.queued
	s.queued = nil

	return events
}
