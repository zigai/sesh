package session

import (
	"testing"
	"time"
)

func TestSessionSorter(t *testing.T) {
	now := time.Now()

	sessions := []Session{
		{Name: "old-activity", Activity: now.Add(-2 * time.Hour)},
		{Name: "recent-switch", Activity: now.Add(-30 * time.Minute)},
		{Name: "newer-activity", Activity: now.Add(-10 * time.Minute)},
		{Name: "older-switch", Activity: now.Add(-3 * time.Hour)},
	}

	st := &State{
		SwitchHistory: []SwitchHistoryEntry{
			{Session: Identity{Name: "recent-switch"}, LastSwitchUnix: now.Add(-5 * time.Minute).Unix()},
			{Session: Identity{Name: "older-switch"}, LastSwitchUnix: now.Add(-1 * time.Hour).Unix()},
			{Session: Identity{Name: "newer-activity"}, LastSwitchUnix: now.Add(-15 * time.Minute).Unix()},
		},
	}

	SortSessions(sessions, st)

	expected := []string{"recent-switch", "newer-activity", "older-switch", "old-activity"}

	for i, s := range sessions {
		if s.Name != expected[i] {
			t.Errorf("Position %d: got %s, want %s", i, s.Name, expected[i])
		}
	}
}

func TestSessionSortUsesActivityWithNilState(t *testing.T) {
	now := time.Now()
	sessions := []Session{
		{Name: "old", Activity: now.Add(-1 * time.Hour)},
		{Name: "new", Activity: now},
	}

	SortSessions(sessions, nil)

	if sessions[0].Name != "new" || sessions[1].Name != "old" {
		t.Fatalf("Sort() = %#v, want activity order", sessions)
	}
}

func TestSessionSortSkipsWhenNoSortKeysExist(t *testing.T) {
	sessions := []Session{
		{Name: "first"},
		{Name: "second"},
	}

	SortSessions(sessions, &State{
		SwitchHistory: []SwitchHistoryEntry{},
	})

	if sessions[0].Name != "first" || sessions[1].Name != "second" {
		t.Fatalf("Sort() = %#v, want original order without sort keys", sessions)
	}
}

func TestSessionSortUsesConnectionScopedSemanticKeys(t *testing.T) {
	now := time.Now()
	sessions := []Session{
		{
			Name:   historyTestSessionName,
			Origin: OriginSSH,
			RuntimeIdentity: RuntimeIdentity{
				ConnectionID: "one",
			},
		},
		{
			Name:   historyTestSessionName,
			Origin: OriginSSH,
			RuntimeIdentity: RuntimeIdentity{
				ConnectionID: "two",
			},
		},
	}
	st := &State{
		RemoteSwitchHistory: []RemoteSwitchHistoryEntry{{
			Session:        Identity{Origin: "ssh", ConnectionID: "two", Name: historyTestSessionName},
			LastSwitchUnix: now.Add(time.Minute).Unix(),
		}},
	}

	SortSessions(sessions, st)

	if got := sessions[0].RuntimeIdentity.ConnectionID; got != "two" {
		t.Fatalf("first connection = %q, want history for connection two", got)
	}
}

func TestSessionSortIsolatesSameNameOnDifferentSockets(t *testing.T) {
	now := time.Now()
	sessions := []Session{
		{
			Name:             "work",
			TmuxServerSocket: "/tmp/tmux-1000/unswitched",
			Activity:         now.Add(-time.Hour),
		},
		{
			Name:             "work",
			TmuxServerSocket: "/tmp/tmux-1000/active",
			Activity:         now.Add(-time.Hour),
		},
	}

	st := &State{
		SwitchHistory: []SwitchHistoryEntry{
			{
				Session: Identity{
					Socket: "/tmp/tmux-1000/active",
					Name:   "work",
				},
				LastSwitchUnix: now.Unix(),
			},
		},
	}

	SortSessions(sessions, st)

	if sessions[0].TmuxServerSocket != "/tmp/tmux-1000/active" {
		t.Fatalf("expected /tmp/tmux-1000/active to sort first, got %q", sessions[0].TmuxServerSocket)
	}
}

func TestSessionSortDeterministicTieBreak(t *testing.T) {
	now := time.Now()
	sessions := []Session{
		{Name: "zebra", Activity: now},
		{Name: "apple", Activity: now},
		{Name: "mango", Activity: now},
	}

	SortSessions(sessions, nil)

	if sessions[0].Name != "apple" || sessions[1].Name != "mango" || sessions[2].Name != "zebra" {
		t.Fatalf("expected alphabetical tie break, got %v, %v, %v", sessions[0].Name, sessions[1].Name, sessions[2].Name)
	}
}
