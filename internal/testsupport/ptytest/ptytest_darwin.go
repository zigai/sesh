package ptytest

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

func unlock(fd int) (string, error) {
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return "", fmt.Errorf("granting pty: %w", err)
	}

	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return "", fmt.Errorf("unlocking pty: %w", err)
	}

	var name [128]byte

	//nolint:staticcheck // SA1019: x/sys/unix has no ioctl wrapper that fills the TIOCPTYGNAME name buffer.
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(fd),
		unix.TIOCPTYGNAME,
		uintptr(unsafe.Pointer(&name[0])),
	)
	if errno != 0 {
		return "", fmt.Errorf("reading pty name: %w", errno)
	}

	return unix.ByteSliceToString(name[:]), nil
}
