//go:build linux && integration

package agents_test

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func openTestPTY(t *testing.T) (*os.File, *os.File, error) {
	t.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening /dev/ptmx: %w", err)
	}

	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()

		return nil, nil, fmt.Errorf("unlocking pty: %w", err)
	}

	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()

		return nil, nil, fmt.Errorf("reading pty number: %w", err)
	}

	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()

		return nil, nil, fmt.Errorf("opening pty slave: %w", err)
	}

	return master, slave, nil
}
