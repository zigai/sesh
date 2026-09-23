//go:build linux && integration

package session_test

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("pty allocation unavailable: %v", err)
	}

	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()

		t.Skipf("pty allocation unavailable: %v", err)
	}

	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()

		t.Skipf("pty allocation unavailable: %v", err)
	}

	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
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
