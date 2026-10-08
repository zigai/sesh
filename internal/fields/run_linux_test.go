package fields_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/fields"
)

type captureProcess struct {
	cancel   context.CancelFunc
	finished chan struct{}
	childPID int
	childFD  int32
}

func TestRunCancelKillsChildAfterLeaderExit(t *testing.T) {
	captureCancellation(t, true)
}

func TestRunCancelKillsChildrenAfterStreamsClose(t *testing.T) {
	captureCancellation(t, false)
}

func captureCancellation(t *testing.T, leaderExits bool) {
	t.Helper()
	captureSubreaper(t)

	dir := t.TempDir()
	readyPath, releasePath := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	ready, release := captureFIFO(t, readyPath), captureFIFO(t, releasePath)
	ctx, cancel := context.WithCancel(t.Context())
	process := captureProcess{cancel: cancel, finished: make(chan struct{}), childPID: 0, childFD: -1}

	t.Cleanup(func() { process.close(t) })

	var result fields.Result

	script := `sleep 300 & printf '%s %s\n' "$$" "$!" > ` + readyPath + `; read release < ` + releasePath + `; printf shown`
	if !leaderExits {
		script = `exec 1>&- 2>&-; sleep 300 & printf '%s %s\n' "$$" "$!" > ` + readyPath + `; read release < ` + releasePath + `; wait`
	}

	go func() {
		result = fields.Run(ctx, shellField(script), sessionContext(""))

		close(process.finished)
	}()

	var leader int
	if _, err := fmt.Fscanf(bufio.NewReader(ready), "%d %d\n", &leader, &process.childPID); err != nil {
		t.Fatal(err)
	}

	process.childFD = capturePIDFD(t, process.childPID)

	leaderFD := capturePIDFD(t, leader)
	defer func() {
		if err := unix.Close(int(leaderFD)); err != nil {
			t.Error(err)
		}
	}()

	if _, err := release.WriteString("exit\n"); err != nil {
		t.Fatal(err)
	}

	if leaderExits && !captureProcessExited(t, leaderFD) {
		t.Fatal("shell leader did not exit")
	}

	reaped := []unix.PollFd{{Fd: leaderFD, Events: 0}}
	if _, err := unix.Poll(reaped, 1000); err != nil {
		t.Fatal(err)
	}

	cancel()
	<-process.finished

	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("Run() = %+v, want canceled", result)
	}

	if !captureProcessExited(t, process.childFD) {
		t.Fatal("background child survived capture cancellation")
	}
}

func (process *captureProcess) close(t *testing.T) {
	t.Helper()
	process.cancel()
	<-process.finished

	if process.childPID == 0 {
		return
	}

	var killErr error
	if process.childFD >= 0 {
		killErr = unix.PidfdSendSignal(int(process.childFD), unix.SIGKILL, nil, 0)
	} else {
		killErr = unix.Kill(process.childPID, unix.SIGKILL)
	}

	if killErr != nil && !errors.Is(killErr, unix.ESRCH) {
		t.Error(killErr)
	}

	var status unix.WaitStatus
	if _, err := unix.Wait4(process.childPID, &status, 0, nil); err != nil {
		t.Error(err)
	}

	if process.childFD >= 0 {
		if err := unix.Close(int(process.childFD)); err != nil {
			t.Error(err)
		}
	}
}

func captureSubreaper(t *testing.T) {
	t.Helper()

	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0); err != nil {
			t.Error(err)
		}
	})
}

func capturePIDFD(t *testing.T, pid int) int32 {
	t.Helper()

	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}

	if fd >= 0 && fd <= math.MaxInt32 {
		return int32(fd)
	}

	if err := unix.Close(fd); err != nil {
		t.Error(err)
	}

	t.Fatal("process descriptor is outside the poll descriptor range")

	return 0
}

func captureFIFO(t *testing.T, path string) *os.File {
	t.Helper()

	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})

	return file
}

func captureProcessExited(t *testing.T, fd int32) bool {
	t.Helper()

	poll := []unix.PollFd{{Fd: fd, Events: unix.POLLIN}}

	count, err := unix.Poll(poll, 1000)
	if err != nil {
		t.Fatal(err)
	}

	return count == 1 && poll[0].Revents&unix.POLLIN != 0
}
