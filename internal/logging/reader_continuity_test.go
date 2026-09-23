//go:build linux || darwin

package logging

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestFollowDoesNotReplayInitialRecords(t *testing.T) {
	for _, lines := range []int{0, 1} {
		t.Run(strconv.Itoa(lines), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.log")

			record := []byte("{\"message\":\"initial\"}\n")
			if err := os.WriteFile(path, record, 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 180*time.Millisecond)
			defer cancel()

			var output bytes.Buffer
			if err := StreamLogs(ctx, path, ReadOptions{Lines: lines, Follow: true}, &output); err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(output.Bytes(), record) {
				t.Fatalf("replayed initial records: %q", output.Bytes())
			}
		})
	}
}

func TestFollowKeepsPartialLineAndDrainsBeforeRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")

	initial := "{\"message\":\"initial\"}\n"
	if err := os.WriteFile(path, []byte(initial+"{\"message\":\"partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer

	state, err := readInitialLogs(t.Context(), []string{path}, FilterOptions{}, &output)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := state.close(); err != nil {
			t.Error(err)
		}
	})
	appendReaderFixture(t, path, "-completed\"}\n")

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}

	newRecord := "{\"message\":\"new\"}\n"
	if err := os.WriteFile(path, []byte(newRecord), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := state.step(t.Context(), path, FilterOptions{}, &output); err != nil {
		t.Fatal(err)
	}

	want := initial + "{\"message\":\"partial-completed\"}\n" + newRecord
	if output.String() != want {
		t.Fatalf("lost or joined records: %q", output.String())
	}
}

func TestFollowTruncateClearsUnterminatedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")

	initial := "{\"message\":\"initial\"}\n"
	if err := os.WriteFile(path, append([]byte(initial+"{\"message\":\""), bytes.Repeat([]byte("x"), 100)...), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer

	state, err := readInitialLogs(t.Context(), []string{path}, FilterOptions{}, &output)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := state.close(); err != nil {
			t.Error(err)
		}
	})

	next := "{\"message\":\"next\"}\n"
	if err := os.WriteFile(path, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := state.step(t.Context(), path, FilterOptions{}, &output); err != nil {
		t.Fatal(err)
	}

	if output.String() != initial+next {
		t.Fatalf("stale partial record: %q", output.String())
	}
}

func TestFollowPreservesOutputFailureAtRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := readInitialLogs(t.Context(), []string{path}, FilterOptions{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := state.close(); err != nil {
			t.Error(err)
		}
	})
	appendReaderFixture(t, path, "{\"message\":\"pending\"}\n")

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("{\"message\":\"new\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := state.step(t.Context(), path, FilterOptions{}, failedReaderOutput{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("lost writer error: %v", err)
	}
}

type failedReaderOutput struct{}

func (failedReaderOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func appendReaderFixture(t *testing.T, path, data string) {
	t.Helper()

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, writeErr := file.WriteString(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
}
