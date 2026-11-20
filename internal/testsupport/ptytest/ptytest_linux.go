package ptytest

import (
	"fmt"
	"strconv"

	"golang.org/x/sys/unix"
)

func unlock(fd int) (string, error) {
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return "", fmt.Errorf("unlocking pty: %w", err)
	}

	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		return "", fmt.Errorf("reading pty number: %w", err)
	}

	return "/dev/pts/" + strconv.Itoa(number), nil
}
