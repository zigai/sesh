//go:build linux || darwin

// Package ptytest opens pseudo-terminals for tests.
package ptytest

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// Open returns a new pseudo-terminal's master and slave ends, closed when the test ends. It fails
// the test when the system cannot allocate one.
func Open(tb testing.TB) (*os.File, *os.File) {
	tb.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		tb.Fatalf("open pty master: %v", err)
	}

	tb.Cleanup(func() { _ = master.Close() })

	name, err := unlock(int(master.Fd()))
	if err != nil {
		tb.Fatalf("unlock pty: %v", err)
	}

	slave, err := os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		tb.Fatalf("open pty slave: %v", err)
	}

	tb.Cleanup(func() { _ = slave.Close() })

	return master, slave
}
