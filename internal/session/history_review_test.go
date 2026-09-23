//go:build linux || darwin

package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/strictjson"
)

func TestSavePreservesUnsafeAndCorruptHistory(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			dir := testAtomicStateDir(t)
			path := filepath.Join(dir, "state.json")
			target := filepath.Join(dir, "unrelated")

			original := []byte("unrelated contents")
			if err := os.WriteFile(target, original, 0o600); err != nil {
				t.Fatal(err)
			}

			makeUnsafeHistoryEntry(t, kind, path, target)

			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}

			if err := SaveState(t.Context(), path, defaultState()); err == nil {
				t.Fatal("unsafe or corrupt history replaced")
			}

			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("history source replaced: %v", err)
			}

			data, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(data, original) {
				t.Fatalf("unrelated contents changed: %v", err)
			}
		})
	}
}

func TestHistoryCancellationAtPublicationPreservesCommittedState(t *testing.T) {
	path := filepath.Join(testAtomicStateDir(t), "state.json")
	if err := SaveState(t.Context(), path, defaultState()); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	svc := NewService(ServiceDeps{FaultHook: func(phase string) error {
		if phase == "rename" {
			cancel()
		}

		return nil
	}})

	result, err := svc.UpdateState(ctx, path, func(s *State) { s.RecordSwitch(Identity{Name: "uncommitted"}) })
	if !errors.Is(err, context.Canceled) || result != nil || errors.Is(err, ErrUncertain) {
		t.Fatalf("publication outcome: %+v, %v", result, err)
	}

	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("canceled publication changed committed data: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary history leaked: %v, %d", err, len(entries))
	}
}

func TestHistoryMissingClassifierRetainsJoinedCleanupFailure(t *testing.T) {
	missing := fmt.Errorf("read history: %w", os.ErrNotExist)
	if !isPureNotExist(missing) {
		t.Fatal("pure missing not recognized")
	}

	mixed := fmt.Errorf("finalize history: %w", errors.Join(missing, io.ErrUnexpectedEOF))
	if isPureNotExist(mixed) {
		t.Fatal("joined cleanup failure hidden")
	}
}

func TestHistoryDoesNotSilentlyDiscardUnrepresentableIdentity(t *testing.T) {
	path := filepath.Join(testAtomicStateDir(t), "state.json")
	svc := NewService(ServiceDeps{})

	result, err := svc.UpdateState(t.Context(), path, func(s *State) { s.RecordSwitch(Identity{Name: "session-\xff"}) })
	if !errors.Is(err, ErrInvalidIdentity) || result != nil {
		t.Fatalf("unrepresentable history was silently discarded: %+v, %v", result, err)
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid history published: %v", err)
	}
}

func TestHistoryRejectsInvalidNestedIdentitiesAndRemoteBounds(t *testing.T) {
	path := filepath.Join(testAtomicStateDir(t), "state.json")

	for _, value := range []string{
		`{"origin":"local","connectionId":"","socket":"","repositorySource":"","name":"dev","extra":1}`,
		`{"origin":"local","connectionId":"","socket":"","repositorySource":"","Name":"dev"}`,
		`{"origin":"local","connectionId":"","socket":"","repositorySource":"","name":"dev","name":"other"}`,
		`null`,
	} {
		data := []byte(`{"version":1,"switchHistory":[{"session":` + value + `,"lastSwitch":1}],"remoteSwitchHistory":[],"agentViewHistory":[],"lastAutosaveTimestamp":0,"popupDisplayMode":""}`)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}

		if got, err := LoadState(path); !errors.Is(err, strictjson.ErrInvalid) || got != nil {
			t.Fatalf("invalid identity accepted: %+v, %v", got, err)
		}
	}

	state := defaultState()

	state.RemoteSwitchHistory = make([]RemoteSwitchHistoryEntry, maxRemoteSwitchHistory+1)
	if err := state.Validate(); !errors.Is(err, ErrHistoryBoundExceeded) {
		t.Fatalf("remote bound not enforced: %v", err)
	}
}

func makeUnsafeHistoryEntry(t *testing.T, kind, path, target string) {
	t.Helper()

	var err error

	switch kind {
	case "symlink":
		err = os.Symlink(target, path)
	case "hardlink":
		err = os.Link(target, path)
	case "fifo":
		err = unix.Mkfifo(path, 0o600)
	case "corrupt":
		err = os.WriteFile(path, []byte("corrupt recovery source"), 0o600)
	default:
		t.Fatal("unknown fixture kind")
	}

	if err != nil {
		t.Fatal(err)
	}
}
