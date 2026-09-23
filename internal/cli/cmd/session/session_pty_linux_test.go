//go:build linux && integration

package cmdsession

import (
	"os"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func openTestPTY(tb testing.TB) (*os.File, *os.File) {
	tb.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		tb.Skipf("pty allocation unavailable: %v", err)
	}

	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()

		tb.Skipf("pty allocation unavailable: %v", err)
	}

	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()

		tb.Skipf("pty allocation unavailable: %v", err)
	}

	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()

		tb.Skipf("pty allocation unavailable: %v", err)
	}

	tb.Cleanup(func() {
		_ = slave.Close()
		_ = master.Close()
	})

	return master, slave
}
