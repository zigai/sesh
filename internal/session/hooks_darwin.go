//go:build darwin

package session

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type exitProbe struct {
	queue int
	done  bool
}

func watchExit(pid int) (*exitProbe, error) {
	queue, err := unix.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("create hook leader queue: %w", err)
	}

	unix.CloseOnExec(queue)

	var change unix.Kevent_t

	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT

	_, err = unix.Kevent(queue, []unix.Kevent_t{change}, nil, nil)
	// The leader is an unreaped child, so ESRCH means it already exited.
	if errors.Is(err, unix.ESRCH) {
		return &exitProbe{queue: queue, done: true}, nil
	}

	if err != nil {
		return nil, errors.Join(fmt.Errorf("watch hook leader: %w", err), unix.Close(queue))
	}

	return &exitProbe{queue: queue, done: false}, nil
}

func (p *exitProbe) exited() (bool, error) {
	if p.done {
		return true, nil
	}

	events := make([]unix.Kevent_t, 1)

	count, err := unix.Kevent(p.queue, nil, events, &unix.Timespec{Sec: 0, Nsec: 0})
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("wait for hook leader: %w", err)
	}

	p.done = count > 0

	return p.done, nil
}

func (p *exitProbe) close() {
	_ = unix.Close(p.queue)
}

// macOS answers EPERM, not ESRCH, when only the unreaped leader's zombie is left in the group.
func processGroupGone(err error) bool {
	return errors.Is(err, unix.ESRCH) || errors.Is(err, unix.EPERM)
}
