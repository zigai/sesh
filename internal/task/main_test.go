package task_test

import (
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/testsupport/testenv"
)

const (
	fakeHarnessName       = "pi"
	fakeHarnessArgsFile   = "harness-args"
	fakeHarnessIgnoreTerm = "harness-ignores-term"
	sleepSignalGate       = "harness-signal-gate"
	sleepSignalReady      = "harness-signal-ready"
	sleepSignalReceived   = "harness-signal-received"
)

func runSleepSignalHarness() int {
	stop := make(chan os.Signal, 1)
	exit := make(chan os.Signal, 1)

	signal.Notify(stop, syscall.SIGTERM)
	signal.Notify(exit, syscall.SIGUSR1)

	defer signal.Stop(stop)
	defer signal.Stop(exit)

	if err := os.WriteFile(fakeHarnessArgsFile, []byte(strings.Join(os.Args[1:], " ")), 0o600); err != nil {
		return 1
	}

	if err := os.WriteFile(sleepSignalReady, []byte("ready"), 0o600); err != nil {
		return 1
	}

	<-stop

	if err := os.WriteFile(sleepSignalReceived, []byte("received"), 0o600); err != nil {
		return 1
	}

	<-exit

	return 0
}

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == fakeHarnessName {
		os.Exit(runFakeHarness())
	}

	os.Exit(testenv.RunIsolated(m))
}

func runFakeHarness() int {
	if _, err := os.Stat(sleepSignalGate); err == nil {
		return runSleepSignalHarness()
	}

	if _, err := os.Stat(fakeHarnessIgnoreTerm); err == nil {
		signal.Ignore(syscall.SIGTERM)
	}

	if err := os.WriteFile(fakeHarnessArgsFile, []byte(strings.Join(os.Args[1:], " ")), 0o600); err != nil {
		return 1
	}

	for {
		time.Sleep(time.Hour)
	}
}
