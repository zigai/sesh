package fields

import (
	"errors"
	"fmt"
	"math"

	"golang.org/x/sys/unix"
)

func waitForFieldExit(pid int) error {
	queue, err := unix.Kqueue()
	if err != nil {
		return fmt.Errorf("create field process observer: %w", err)
	}
	unix.CloseOnExec(queue)

	err = observeFieldExit(queue, pid)

	return errors.Join(err, unix.Close(queue))
}

func observeFieldExit(queue, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("observe field command exit: %w", unix.EINVAL)
	}

	change := unix.Kevent_t{
		Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT, Data: 0, Udata: nil,
	}
	events := make([]unix.Kevent_t, 1)
	changes := []unix.Kevent_t{change}

	for {
		_, err := unix.Kevent(queue, changes, events, nil)
		if errors.Is(err, unix.EINTR) {
			changes = nil
			continue
		}

		if errors.Is(err, unix.ESRCH) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("observe field command exit: %w", err)
		}

		if events[0].Flags&unix.EV_ERROR != 0 {
			if events[0].Data < 0 || events[0].Data > math.MaxInt32 {
				return fmt.Errorf("observe field command exit: %w", unix.EINVAL)
			}

			cause := unix.Errno(events[0].Data)
			if errors.Is(cause, unix.ESRCH) {
				return nil
			}

			return fmt.Errorf("observe field command exit: %w", cause)
		}

		return nil
	}
}
