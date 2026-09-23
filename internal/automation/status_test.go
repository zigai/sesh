//go:build linux || darwin

package automation_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/automation"
)

func TestFinalSaveStatusRoundTripUsesPrivateFile(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(directory, automation.FinalSaveStatusName)
	want := automation.FinalSaveStatus{
		StartedAtUnix:  1,
		Result:         automation.FinalSaveSaved,
		Attempt:        "attempt",
		Generation:     "generation",
		SnapshotStatus: "complete",
		Recorded:       true,
		Bookkeeping:    true,
	}

	if err := automation.WriteFinalSaveStatus(path, want); err != nil {
		t.Fatalf("WriteFinalSaveStatus failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("status mode = %#o, want 0600", got)
	}

	got, err := automation.ReadFinalSaveStatus(path)
	if err != nil {
		t.Fatalf("ReadFinalSaveStatus failed: %v", err)
	}

	if got != want {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
}

func TestFinalSaveStatusRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()

	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}

	linkedDirectory := filepath.Join(root, "linked")
	if err := os.Symlink(realDirectory, linkedDirectory); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	path := filepath.Join(linkedDirectory, automation.FinalSaveStatusName)
	if err := automation.WriteFinalSaveStatus(path, automation.FinalSaveStatus{}); err == nil {
		t.Fatal("WriteFinalSaveStatus succeeded through a symlinked parent")
	}

	if _, err := os.Stat(filepath.Join(realDirectory, automation.FinalSaveStatusName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target status Stat error = %v, want os.ErrNotExist", err)
	}

	if _, err := automation.ReadFinalSaveStatus(path); err == nil {
		t.Fatal("ReadFinalSaveStatus succeeded through a symlinked parent")
	}
}

func TestReadFinalSaveStatusRejectsFIFOWithoutBlocking(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}

	path := filepath.Join(directory, automation.FinalSaveStatusName)
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Mkfifo failed: %v", err)
	}

	done := make(chan error, 1)

	go func() {
		_, err := automation.ReadFinalSaveStatus(path)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, automation.ErrFinalStatusNotRegular) {
			t.Fatalf("ReadFinalSaveStatus error = %v, want ErrFinalStatusNotRegular", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReadFinalSaveStatus blocked on FIFO")
	}
}

func TestReadFinalSaveStatusRejectsOversizedValidJSONPrefix(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}

	data, err := json.Marshal(automation.FinalSaveStatus{Result: automation.FinalSaveSaved})
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	data = append(data, bytes.Repeat([]byte(" "), 65*1024)...)

	path := filepath.Join(directory, automation.FinalSaveStatusName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	if _, err := automation.ReadFinalSaveStatus(path); !errors.Is(err, automation.ErrFinalStatusTooLarge) {
		t.Fatalf("ReadFinalSaveStatus error = %v, want ErrFinalStatusTooLarge", err)
	}
}
