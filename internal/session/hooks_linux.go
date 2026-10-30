//go:build linux

package session

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type exitProbe struct {
	pid int
}

func watchExit(pid int) (*exitProbe, error) {
	return &exitProbe{pid: pid}, nil
}

func (p *exitProbe) exited() (bool, error) {
	var info unix.Siginfo

	err := unix.Waitid(unix.P_PID, p.pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("wait for hook leader: %w", err)
	}

	return info.Signo != 0, nil
}

func (p *exitProbe) close() {}

func processGroupGone(err error) bool {
	return errors.Is(err, unix.ESRCH)
}
