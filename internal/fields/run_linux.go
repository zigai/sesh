package fields

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func waitForFieldExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}

		if err != nil {
			return fmt.Errorf("observe field command exit: %w", err)
		}

		return nil
	}
}
