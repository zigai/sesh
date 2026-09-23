//go:build linux || darwin

package session

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/strictjson"
)

const historyTestSessionName = "dev"

func TestLoadRejectsEmptyPath(t *testing.T) {
	if _, err := LoadState(""); !errors.Is(err, ErrEmptyStatePath) {
		t.Fatalf("LoadState(\"\") error = %v, want ErrEmptyStatePath", err)
	}
}

func TestSaveRejectsEmptyPath(t *testing.T) {
	if err := SaveState(t.Context(), "", &State{Version: CurrentHistoryVersion}); !errors.Is(err, ErrEmptyStatePath) {
		t.Fatalf("SaveState(\"\") error = %v, want ErrEmptyStatePath", err)
	}
}

func TestUpdateStatePreservesChangesFromEarlierWriter(t *testing.T) {
	path := testStatePath(t)
	service := NewService(ServiceDeps{})
	identity := Identity{Name: historyTestSessionName}

	if _, err := service.UpdateState(t.Context(), path, func(current *State) {
		current.RecordAgentView(identity, "%1")
	}); err != nil {
		t.Fatalf("recording agent view: %v", err)
	}

	updated, err := service.UpdateState(t.Context(), path, func(current *State) {
		current.PopupDisplayMode = "agents"
	})
	if err != nil {
		t.Fatalf("recording display mode: %v", err)
	}

	if updated.GetLastAgentView(identity, "%1") == 0 {
		t.Fatal("later update erased the earlier agent view")
	}

	if updated.PopupDisplayMode != "agents" {
		t.Fatalf("PopupDisplayMode = %q, want agents", updated.PopupDisplayMode)
	}
}

func TestUpdateStateSerializesConcurrentWriters(t *testing.T) {
	path := testStatePath(t)
	service := NewService(ServiceDeps{})
	start := make(chan struct{})
	errorsCh := make(chan error, 2)

	var ready sync.WaitGroup
	ready.Add(2)

	go func() {
		ready.Done()
		<-start

		_, err := service.UpdateState(t.Context(), path, func(current *State) {
			current.RecordSwitch(Identity{Name: historyTestSessionName})
		})
		errorsCh <- err
	}()
	go func() {
		ready.Done()
		<-start

		_, err := service.UpdateState(t.Context(), path, func(current *State) {
			current.PopupDisplayMode = "agents"
		})
		errorsCh <- err
	}()

	ready.Wait()
	close(start)

	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatalf("concurrent UpdateState() error = %v", err)
		}
	}

	updated, err := service.LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	if updated.GetLastSwitch(Identity{Name: historyTestSessionName}) == 0 {
		t.Fatal("missing switch history for dev")
	}

	if updated.PopupDisplayMode != "agents" {
		t.Fatalf("PopupDisplayMode = %q, want agents", updated.PopupDisplayMode)
	}
}

func TestUpdateStatePreservesGeneratedSwitchHistory(t *testing.T) {
	root := testStateDir(t)
	service := NewService(ServiceDeps{})
	iteration := 0

	rapid.Check(t, func(rt *rapid.T) {
		switches := rapid.MapOfN(
			rapid.StringMatching(`[a-z][a-z0-9]{0,11}`),
			rapid.Int64Range(1, 1<<40),
			0,
			24,
		).Draw(rt, "switches")
		mode := rapid.SampledFrom([]string{"sesh", "agents"}).Draw(rt, "mode")
		path := filepath.Join(root, strconv.Itoa(iteration)+".json")
		iteration++

		entries := make([]SwitchHistoryEntry, 0, len(switches))
		for name, lastSwitch := range switches {
			entries = append(entries, SwitchHistoryEntry{
				Session:    Identity{Name: name},
				LastSwitch: lastSwitch,
			})
		}

		if err := service.SaveState(t.Context(), path, &State{Version: CurrentHistoryVersion, SwitchHistory: entries}); err != nil {
			rt.Fatalf("SaveState() error = %v", err)
		}

		updated, err := service.UpdateState(t.Context(), path, func(current *State) {
			current.PopupDisplayMode = mode
		})
		if err != nil {
			rt.Fatalf("UpdateState() error = %v", err)
		}

		for name, wantSwitch := range switches {
			if gotSwitch := updated.GetLastSwitch(Identity{Name: name}); gotSwitch != wantSwitch {
				rt.Fatalf("GetLastSwitch(%q) = %d, want %d", name, gotSwitch, wantSwitch)
			}
		}

		if updated.PopupDisplayMode != mode {
			rt.Fatalf("PopupDisplayMode = %q, want %q", updated.PopupDisplayMode, mode)
		}
	})
}

func TestUpdateStateRejectsNilMutation(t *testing.T) {
	service := NewService(ServiceDeps{})
	path := testStatePath(t)

	_, err := service.UpdateState(t.Context(), path, nil)
	if !errors.Is(err, ErrNilStateUpdate) {
		t.Fatalf("UpdateState() error = %v, want ErrNilStateUpdate", err)
	}
}

func TestStateSwitchHistory(t *testing.T) {
	st := &State{
		Version: CurrentHistoryVersion,
	}

	before := time.Now().Unix()

	ident := Identity{Name: "session1"}
	st.RecordSwitch(ident)

	lastSwitch := st.GetLastSwitch(ident)
	if lastSwitch < before {
		t.Error("Switch timestamp should be >= current time")
	}

	if st.GetLastSwitch(Identity{Name: "nonexistent"}) != 0 {
		t.Error("Non-existent session should return 0")
	}
}

func TestStateRemoteSwitchHistoryUsesStructuredIdentity(t *testing.T) {
	st := &State{}
	first := Identity{Origin: "ssh", ConnectionID: "a", Name: "b:c"}
	second := Identity{Origin: "ssh", ConnectionID: "a:b", Name: "c"}

	st.RecordRemoteSwitch(first)

	if st.GetLastRemoteSwitch(first) == 0 {
		t.Fatal("recorded remote switch was not found")
	}

	if st.GetLastRemoteSwitch(second) != 0 {
		t.Fatal("distinct structured remote identity collided")
	}
}

func TestStateAgentViewHistoryUsesSessionAndPaneIdentity(t *testing.T) {
	st := &State{}
	local := Identity{Name: historyTestSessionName}
	remote := Identity{Origin: "ssh", ConnectionID: "work", Name: historyTestSessionName}

	st.RecordAgentView(local, "%7")

	if st.GetLastAgentView(local, "%7") == 0 {
		t.Fatal("recorded agent view was not found")
	}

	if st.GetLastAgentView(local, "%8") != 0 || st.GetLastAgentView(remote, "%7") != 0 {
		t.Fatal("distinct agent view identity collided")
	}
}

func TestStateRenameSession(t *testing.T) {
	st := &State{
		SwitchHistory: []SwitchHistoryEntry{
			{Session: Identity{Name: "old"}, LastSwitch: 123},
		},
		AgentViewHistory: []AgentViewHistoryEntry{{
			Session:  Identity{Name: "old"},
			PaneID:   "%7",
			ViewedAt: 456,
		}},
	}
	oldIdent := Identity{Name: "old"}
	newIdent := Identity{Name: "new"}
	st.RenameSession(oldIdent, newIdent)

	if st.GetLastSwitch(oldIdent) != 0 || st.GetLastSwitch(newIdent) != 123 {
		t.Error("switch history should move to the new session name")
	}

	if got := st.GetLastAgentView(Identity{Name: "new"}, "%7"); got != 456 {
		t.Fatalf("agent view history after rename = %d, want 456", got)
	}
}

func TestStateDeleteSession(t *testing.T) {
	st := &State{
		SwitchHistory: []SwitchHistoryEntry{
			{Session: Identity{Name: historyTestSessionName}, LastSwitch: 123},
			{Session: Identity{Name: "keep"}, LastSwitch: 456},
		},
		AgentViewHistory: []AgentViewHistoryEntry{
			{Session: Identity{Name: historyTestSessionName}, PaneID: "%7", ViewedAt: 789},
			{Session: Identity{Name: "keep"}, PaneID: "%8", ViewedAt: 999},
		},
	}
	st.DeleteSession(Identity{Name: historyTestSessionName})

	if st.GetLastSwitch(Identity{Name: historyTestSessionName}) != 0 {
		t.Fatal("dev switch history was not removed")
	}

	if st.GetLastSwitch(Identity{Name: "keep"}) != 456 {
		t.Fatalf("unrelated state changed: %#v", st)
	}

	if got := st.GetLastAgentView(Identity{Name: historyTestSessionName}, "%7"); got != 0 {
		t.Fatalf("deleted agent view history = %d, want 0", got)
	}

	if got := st.GetLastAgentView(Identity{Name: "keep"}, "%8"); got != 999 {
		t.Fatalf("unrelated agent view history = %d, want 999", got)
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := testStatePath(t)
	st := &State{
		Version: CurrentHistoryVersion,
		SwitchHistory: []SwitchHistoryEntry{
			{Session: Identity{Name: "session1"}, LastSwitch: 1234567890},
		},
		RemoteSwitchHistory: []RemoteSwitchHistoryEntry{{
			Session:    Identity{Origin: "ssh", ConnectionID: "work", Name: historyTestSessionName},
			LastSwitch: 1234567891,
		}},
		AgentViewHistory: []AgentViewHistoryEntry{{
			Session:  Identity{Name: "session1"},
			PaneID:   "%7",
			ViewedAt: 1234567892,
		}},
		PopupDisplayMode: "agents",
	}

	if err := SaveState(t.Context(), path, st); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loadedState, err := LoadState(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loadedState.GetLastSwitch(Identity{Name: "session1"}) != 1234567890 {
		t.Error("Switch history should match after load")
	}

	if loadedState.PopupDisplayMode != "agents" {
		t.Fatalf("PopupDisplayMode = %q, want agents", loadedState.PopupDisplayMode)
	}

	remoteIdentity := Identity{Origin: "ssh", ConnectionID: "work", Name: historyTestSessionName}
	if loadedState.GetLastRemoteSwitch(remoteIdentity) != 1234567891 {
		t.Error("Remote switch history should match after load")
	}

	if got := loadedState.GetLastAgentView(Identity{Name: "session1"}, "%7"); got != 1234567892 {
		t.Fatalf("agent view history after load = %d, want 1234567892", got)
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing_dir", "state.json")

	st, err := LoadState(path)
	if err != nil {
		t.Fatalf("Load should not fail for missing file: %v", err)
	}

	if len(st.SwitchHistory) != 0 {
		t.Error("Missing file should return empty state")
	}
}

func TestStateSourceIsolationSameNameDifferentSockets(t *testing.T) {
	st := &State{}
	first := Identity{Socket: "/tmp/tmux-1000/one", Name: "shared"}
	second := Identity{Socket: "/tmp/tmux-1000/two", Name: "shared"}

	st.RecordSwitchIdentity(first)

	if st.GetLastSwitchIdentity(first) == 0 {
		t.Fatal("recorded switch for first socket not found")
	}

	if st.GetLastSwitchIdentity(second) != 0 {
		t.Fatal("switch on first socket bled into second socket")
	}
}

func TestStatePrunesOldestSwitchWhenExceedingBound(t *testing.T) {
	st := &State{}

	for i := range maxSwitchHistory + 5 {
		st.RecordSwitchIdentity(Identity{
			Name: "session-" + strconv.Itoa(i),
		})
	}

	if len(st.SwitchHistory) > maxSwitchHistory {
		t.Fatalf("SwitchHistory length %d exceeded bound %d", len(st.SwitchHistory), maxSwitchHistory)
	}
}

func TestLoadRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(testStateDir(t), "oversized.json")

	data := make([]byte, maxHistoryFileSize+100)
	for i := range data {
		data[i] = ' '
	}

	data[0] = '{'
	data[len(data)-1] = '}'

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing oversized file: %v", err)
	}

	_, err := LoadState(path)
	if err == nil {
		t.Fatal("LoadState() expected error on oversized file, got nil")
	}

	if !errors.Is(err, ErrHistoryOversized) && !errors.Is(err, privatefs.ErrUnsafe) {
		t.Fatalf("expected ErrHistoryOversized or ErrUnsafe, got %v", err)
	}
}

func TestLoadRejectsSymlink(t *testing.T) {
	dir := testStateDir(t)
	targetPath := filepath.Join(dir, "real_state.json")
	linkPath := filepath.Join(dir, "symlink_state.json")

	st := &State{
		Version: CurrentHistoryVersion,
	}
	if err := SaveState(t.Context(), targetPath, st); err != nil {
		t.Fatalf("saving target state: %v", err)
	}

	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	_, err := LoadState(linkPath)
	if err == nil {
		t.Fatal("LoadState() expected error on symlinked state file, got nil")
	}

	if !errors.Is(err, privatefs.ErrUnsafe) && !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected rejection of symlink, got %v", err)
	}
}

func TestLoadRejectsInvalidVersion(t *testing.T) {
	path := filepath.Join(testStateDir(t), "state.json")

	data := []byte(`{"version": 999, "switchHistory": [], "remoteSwitchHistory": [], "agentViewHistory": [], "lastAutosaveTimestamp": 0, "popupDisplayMode": ""}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing invalid version: %v", err)
	}

	_, err := LoadState(path)
	if err == nil {
		t.Fatal("LoadState() expected error on invalid version, got nil")
	}

	if !errors.Is(err, ErrInvalidHistoryVersion) {
		t.Fatalf("expected ErrInvalidHistoryVersion, got %v", err)
	}
}

func testStateDir(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "history")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	return dir
}

func testStatePath(t *testing.T) string {
	t.Helper()

	return filepath.Join(testStateDir(t), "state.json")
}

func TestLoadStateNeverMutatesDirectoryPermissions(t *testing.T) {
	parent := t.TempDir()

	unsafeDir := filepath.Join(parent, "unsafe_history")
	if err := os.Mkdir(unsafeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(unsafeDir, "state.json")

	_, err := LoadState(statePath)
	if err == nil {
		t.Fatal("expected error reading from unsafe 0755 directory, got nil")
	}

	if !errors.Is(err, privatefs.ErrUnsafe) {
		t.Fatalf("expected privatefs.ErrUnsafe, got: %v", err)
	}

	fi, statErr := os.Stat(unsafeDir)
	if statErr != nil {
		t.Fatal(statErr)
	}

	if mode := fi.Mode().Perm(); mode != 0o755 {
		t.Fatalf("LoadState mutated directory mode to %o, want preserved %o", mode, 0o755)
	}
}

func TestSaveStateRejectsUnsafeDirectoryWithoutChmod(t *testing.T) {
	parent := t.TempDir()

	unsafeDir := filepath.Join(parent, "unsafe_history")
	if err := os.Mkdir(unsafeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(unsafeDir, "state.json")
	st := &State{Version: CurrentHistoryVersion}

	err := SaveState(t.Context(), statePath, st)
	if err == nil {
		t.Fatal("expected error saving to unsafe 0755 directory, got nil")
	}

	if !errors.Is(err, privatefs.ErrUnsafe) {
		t.Fatalf("expected privatefs.ErrUnsafe, got: %v", err)
	}

	fi, statErr := os.Stat(unsafeDir)
	if statErr != nil {
		t.Fatal(statErr)
	}

	if mode := fi.Mode().Perm(); mode != 0o755 {
		t.Fatalf("SaveState mutated directory mode to %o, want preserved %o", mode, 0o755)
	}
}

func TestLoadRejectsMissingVersionAndDuplicateFields(t *testing.T) {
	dir := testStateDir(t)

	missingVerPath := filepath.Join(dir, "missing_ver.json")
	if err := os.WriteFile(missingVerPath, []byte(`{"switchHistory":[],"remoteSwitchHistory":[],"agentViewHistory":[],"lastAutosaveTimestamp":0,"popupDisplayMode":""}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadState(missingVerPath); !errors.Is(err, strictjson.ErrInvalid) {
		t.Fatalf("missing version err = %v, want strictjson.ErrInvalid", err)
	}

	unknownFieldPath := filepath.Join(dir, "unknown_field.json")
	if err := os.WriteFile(unknownFieldPath, []byte(`{"version":1,"switchHistory":[],"remoteSwitchHistory":[],"agentViewHistory":[],"lastAutosaveTimestamp":0,"popupDisplayMode":"","extraField":123}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadState(unknownFieldPath); !errors.Is(err, strictjson.ErrInvalid) {
		t.Fatalf("unknown field err = %v, want strictjson.ErrInvalid", err)
	}

	dupSwitchPath := filepath.Join(dir, "dup_switch.json")
	if err := os.WriteFile(dupSwitchPath, []byte(`{"version":1,"switchHistory":[{"session":{"origin":"","connectionId":"","socket":"","repositorySource":"","name":"dev"},"lastSwitch":1},{"session":{"origin":"","connectionId":"","socket":"","repositorySource":"","name":"dev"},"lastSwitch":2}],"remoteSwitchHistory":[],"agentViewHistory":[],"lastAutosaveTimestamp":0,"popupDisplayMode":""}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadState(dupSwitchPath); !errors.Is(err, strictjson.ErrInvalid) {
		t.Fatalf("duplicate switch entry err = %v, want strictjson.ErrInvalid", err)
	}
}

func TestCrossSocketSameNameIsolation(t *testing.T) {
	st := &State{
		Version: CurrentHistoryVersion,
	}

	first := Identity{Socket: "/tmp/tmux-1000/one", Name: "shared"}
	second := Identity{Socket: "/tmp/tmux-1000/two", Name: "shared"}

	st.RecordSwitchIdentity(first)

	if st.GetLastSwitchIdentity(first) == 0 {
		t.Fatal("recorded switch for first socket not found")
	}

	if st.GetLastSwitchIdentity(second) != 0 {
		t.Fatal("switch on first socket bled into second socket")
	}

	localDefault := Identity{Name: "shared"}
	if st.GetLastSwitch(localDefault) != 0 {
		t.Fatal("GetLastSwitch matched socket-bound session without socket identity")
	}

	st.DeleteSession(localDefault)

	if st.GetLastSwitchIdentity(first) == 0 {
		t.Fatal("DeleteSession(localDefault) cross-deleted socket-bound session")
	}

	st.RenameSession(localDefault, Identity{Name: "renamed"})

	if st.GetLastSwitchIdentity(first) == 0 {
		t.Fatal("RenameSession(localDefault) cross-renamed socket-bound session")
	}

	st.RenameSession(first, Identity{Socket: "/tmp/tmux-1000/one", Name: "renamed"})

	if st.GetLastSwitchIdentity(first) != 0 {
		t.Fatal("old socket identity still present after rename")
	}

	renamed := Identity{Socket: "/tmp/tmux-1000/one", Name: "renamed"}
	if st.GetLastSwitchIdentity(renamed) == 0 {
		t.Fatal("renamed socket identity not found")
	}
}
