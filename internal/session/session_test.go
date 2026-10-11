package session_test

import (
	"testing"
	"time"

	"github.com/zigai/sesh/internal/session"
)

func TestSessionIdentityKeysPreserveLocalName(t *testing.T) {
	for _, origin := range []session.Origin{"", session.OriginLocal} {
		item := session.Session{
			Name:   "dev",
			Origin: origin,
			RuntimeIdentity: session.RuntimeIdentity{
				ConnectionID: "ignored",
				PeerID:       "ignored",
			},
		}

		wantIdentity := session.Identity{Origin: string(origin), ConnectionID: "ignored", Socket: "", RepositorySource: "", Name: "dev"}
		if got := item.SemanticIdentity(); got != wantIdentity {
			t.Fatalf("SemanticIdentity() = %#v, want %#v", got, wantIdentity)
		}

		if got := item.RuntimeKey(); got == "" {
			t.Fatal("RuntimeKey() is empty")
		}
	}
}

func TestRemoteSessionIdentityKeysSeparateSemanticAndRuntimeIdentity(t *testing.T) {
	item := session.Session{
		Name:   "dev",
		ID:     "$1",
		Origin: session.OriginSSH,
		RuntimeIdentity: session.RuntimeIdentity{
			ConnectionID:         "work",
			ConfigDigest:         "config-a",
			PeerID:               "peer-a",
			StreamInstance:       "stream-a",
			TmuxServerSocket:     "/tmp/tmux-1000/default",
			TmuxServerPID:        42,
			TmuxServerStartedAt:  time.Unix(100, 0),
			TmuxSessionCreatedAt: time.Unix(200, 0),
		},
	}

	wantIdentity := session.Identity{
		Origin:           "ssh",
		ConnectionID:     "work",
		Socket:           "/tmp/tmux-1000/default",
		RepositorySource: "",
		Name:             "dev",
	}
	if got := item.SemanticIdentity(); got != wantIdentity {
		t.Fatalf("SemanticIdentity() = %#v, want %#v", got, wantIdentity)
	}

	runtimeKey := item.RuntimeKey()
	changed := item

	changed.RuntimeIdentity.StreamInstance = "stream-b"
	if runtimeKey == changed.RuntimeKey() {
		t.Fatal("RuntimeKey() did not change with the runtime incarnation")
	}

	renumbered := item

	renumbered.ID = "$99"
	if runtimeKey == renumbered.RuntimeKey() {
		t.Fatal("RuntimeKey() did not change with the tmux session ID")
	}
}

func TestRemoteSemanticIdentityDoesNotCollideWhenNamesContainDelimiters(t *testing.T) {
	first := session.Session{
		Name:   "b:c",
		Origin: session.OriginSSH,
		RuntimeIdentity: session.RuntimeIdentity{
			ConnectionID: "a",
		},
	}
	second := session.Session{
		Name:   "c",
		Origin: session.OriginSSH,
		RuntimeIdentity: session.RuntimeIdentity{
			ConnectionID: "a:b",
		},
	}

	if first.SemanticIdentity() == second.SemanticIdentity() {
		t.Fatal("distinct connection and session names produced the same semantic identity")
	}
}

func TestLocalSessionIdentitiesOnDifferentSocketsDoNotCollide(t *testing.T) {
	first := session.Session{
		Name:             "dev",
		TmuxServerSocket: "/tmp/tmux-1000/first",
		Origin:           session.OriginLocal,
	}
	second := session.Session{
		Name:             "dev",
		TmuxServerSocket: "/tmp/tmux-1000/second",
		Origin:           session.OriginLocal,
	}

	if first.SemanticIdentity() == second.SemanticIdentity() {
		t.Fatal("sessions on different sockets produced the same semantic identity")
	}

	if first.EntityKey() == second.EntityKey() {
		t.Fatal("sessions on different sockets produced the same entity key")
	}
}

func TestSessionEntityKeyDistinguishesLiveSavedAndGenerations(t *testing.T) {
	live := session.Session{
		ID:               "$0",
		Name:             "dev",
		Kind:             session.KindLive,
		TmuxServerSocket: "/tmp/tmux-1000/default",
	}
	savedGen1 := session.Session{
		ID:         "s0",
		Name:       "dev",
		Kind:       session.KindSaved,
		Generation: "gen-1",
	}
	savedGen2 := session.Session{
		ID:         "s0",
		Name:       "dev",
		Kind:       session.KindSaved,
		Generation: "gen-2",
	}

	keys := map[string]bool{
		live.EntityKey():      true,
		savedGen1.EntityKey(): true,
		savedGen2.EntityKey(): true,
	}

	if len(keys) != 3 {
		t.Fatalf("expected 3 distinct entity keys for live and saved generations, got %d", len(keys))
	}
}

func TestSavedSessionSemanticIdentity(t *testing.T) {
	s := session.Session{
		ID:         "s1",
		Name:       "my-project",
		Kind:       session.KindSaved,
		Generation: "gen-abc",
	}

	ident := s.SemanticIdentity()
	if ident.Name != "my-project" {
		t.Fatalf("ident.Name = %q, want my-project", ident.Name)
	}

	if !s.Saved() {
		t.Fatal("Saved() should be true for KindSaved")
	}
}

func TestSavedSessionIdentitySeparatesRepositories(t *testing.T) {
	s1 := session.Session{
		ID:               "s1",
		Name:             "my-project",
		Kind:             session.KindSaved,
		Generation:       "gen-abc",
		RepositorySource: "/repo/one",
	}
	s2 := session.Session{
		ID:               "s1",
		Name:             "my-project",
		Kind:             session.KindSaved,
		Generation:       "gen-abc",
		RepositorySource: "/repo/two",
	}

	if s1.SemanticIdentity() == s2.SemanticIdentity() {
		t.Fatal("saved sessions from different repositories produced the same semantic identity")
	}

	if s1.EntityKey() == s2.EntityKey() {
		t.Fatal("saved sessions from different repositories produced the same entity key")
	}
}
