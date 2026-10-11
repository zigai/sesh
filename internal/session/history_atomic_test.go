//go:build linux || darwin

package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/strictjson"
)

var errInjectedFault = errors.New("injected fault")

func testAtomicStateDir(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestSaveRenameFailureLeavesOriginalFile(t *testing.T) {
	stateDir := testAtomicStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	original := []byte(`{"version":1,"switchHistory":[{"session":{"origin":"","connectionId":"","socket":"","repositorySource":"","name":"old"},"lastSwitch":1}],"remoteSwitchHistory":[],"agentViewHistory":[],"lastAutosaveTimestamp":0,"popupDisplayMode":""}`)
	if err := os.WriteFile(statePath, original, 0o600); err != nil {
		t.Fatalf("writing original state failed: %v", err)
	}

	svc := NewService(ServiceDeps{
		FaultHook: func(phase string) error {
			if phase == "rename" {
				return errInjectedFault
			}

			return nil
		},
	})

	err := svc.SaveState(t.Context(), statePath, &State{
		Version: CurrentHistoryVersion,
		SwitchHistory: []SwitchHistoryEntry{
			{Session: Identity{Name: "new"}, LastSwitchUnix: 2},
		},
	})
	if !errors.Is(err, errInjectedFault) {
		t.Fatalf("SaveState() error = %v, want injected fault", err)
	}

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("reading state file failed: %v", err)
	}

	if string(data) != string(original) {
		t.Fatalf("state file changed after failed rename: got %q, want %q", string(data), string(original))
	}
}

func TestLoadInvalidJSONPreservesTypedCauseAndFixedMessage(t *testing.T) {
	stateDir := testAtomicStateDir(t)

	statePath := filepath.Join(stateDir, "state.json")
	if err := os.WriteFile(statePath, []byte("{invalid"), 0o600); err != nil {
		t.Fatalf("writing invalid state failed: %v", err)
	}

	_, err := LoadState(statePath)
	if err == nil {
		t.Fatal("LoadState() error = nil, want non-nil")
	}

	if !errors.Is(err, strictjson.ErrInvalid) {
		t.Fatalf("LoadState() error = %v, want strictjson.ErrInvalid", err)
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatalf("expected oops error, got: %T (%v)", err, err)
	}

	if oopsErr.Domain() != "session" {
		t.Fatalf("oopsErr.Domain() = %q, want session", oopsErr.Domain())
	}

	if oopsErr.Context()["operation"] != "load_history" {
		t.Fatalf("oopsErr operation = %v, want load_history", oopsErr.Context()["operation"])
	}
}

func TestSaveSyncDirFailureReportsErrUncertain(t *testing.T) {
	stateDir := testAtomicStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")
	svc := NewService(ServiceDeps{
		FaultHook: func(phase string) error {
			if phase == "sync_dir" {
				return errInjectedFault
			}

			return nil
		},
	})

	err := svc.SaveState(t.Context(), statePath, &State{
		Version: CurrentHistoryVersion,
		SwitchHistory: []SwitchHistoryEntry{
			{Session: Identity{Name: historyTestSessionName}, LastSwitchUnix: 100},
		},
	})
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("SaveState() error = %v, want ErrUncertain on post-rename sync failure", err)
	}

	if !errors.Is(err, errInjectedFault) {
		t.Fatalf("SaveState() error = %v, want joined errInjectedFault", err)
	}
}

func TestSaveWriteFailureCleansUpTempFileAndPreservesOriginal(t *testing.T) {
	stateDir := testAtomicStateDir(t)
	statePath := filepath.Join(stateDir, "state.json")

	original := []byte(`{"version":1,"switchHistory":[],"remoteSwitchHistory":[],"agentViewHistory":[],"lastAutosaveTimestamp":0,"popupDisplayMode":""}`)
	if err := os.WriteFile(statePath, original, 0o600); err != nil {
		t.Fatalf("writing original state failed: %v", err)
	}

	svc := NewService(ServiceDeps{
		FaultHook: func(phase string) error {
			if phase == "write" {
				return errInjectedFault
			}

			return nil
		},
	})

	err := svc.SaveState(t.Context(), statePath, &State{
		Version: CurrentHistoryVersion,
		SwitchHistory: []SwitchHistoryEntry{
			{Session: Identity{Name: historyTestSessionName}, LastSwitchUnix: 1},
		},
	})
	if !errors.Is(err, errInjectedFault) {
		t.Fatalf("SaveState() error = %v, want errInjectedFault", err)
	}

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("reading state file failed: %v", err)
	}

	if string(data) != string(original) {
		t.Fatalf("original state modified: %s", string(data))
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("reading stateDir: %v", err)
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".state.json.tmp") {
			t.Fatalf("found stray temp file: %s", entry.Name())
		}
	}
}
