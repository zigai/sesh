//go:build darwin && integration

package session_test

import (
	"fmt"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pty allocation unavailable: %v", err)
	}

	name, err := unlockPTY(int(master.Fd()))
	if err != nil {
		_ = master.Close()

		t.Skipf("pty allocation unavailable: %v", err)
	}

	slave, err := os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()

		t.Skipf("pty allocation unavailable: %v", err)
	}

	t.Cleanup(func() {
		_ = slave.Close()
		_ = master.Close()
	})

	return master, slave
}

func unlockPTY(fd int) (string, error) {
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return "", fmt.Errorf("granting pty: %w", err)
	}

	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return "", fmt.Errorf("unlocking pty: %w", err)
	}

	var name [128]byte

	//nolint:staticcheck // SA1019: x/sys/unix has no ioctl wrapper that fills the TIOCPTYGNAME name buffer.
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0])))
	if errno != 0 {
		return "", fmt.Errorf("reading pty name: %w", errno)
	}

	return unix.ByteSliceToString(name[:]), nil
}
