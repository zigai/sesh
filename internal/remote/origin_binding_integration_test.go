//go:build linux && integration

package remote

import (
	"os"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"
)

func TestOriginBindingCleanupPreservesReplacement(t *testing.T) {
	server := tmuxtest.NewServer(t)

	sessions, err := server.Sessions(t.Context())
	if err != nil || len(sessions) == 0 {
		t.Fatalf("fixture sessions: %v", err)
	}

	live := NewGotmuxLiveServer(server)

	name := "@sesh_origin_test"
	if err := server.Options().SetUser(t.Context(), name, "replacement"); err != nil {
		t.Fatal(err)
	}

	if err := live.clearOriginBinding(t.Context(), sessions[0].Handle(), name, "old-owner"); err != nil {
		t.Fatal(err)
	}

	value, err := server.Options().User(t.Context(), name)
	if got, _ := value.Local.Get(); err != nil || got != "replacement" {
		t.Fatalf("replacement binding changed: %q, %v", got, err)
	}

	if err := live.clearOriginBinding(t.Context(), sessions[0].Handle(), name, "replacement"); err != nil {
		t.Fatal(err)
	}

	value, err = server.Options().User(t.Context(), name)
	if _, present := value.Local.Get(); err != nil || present {
		t.Fatalf("owned binding was not removed: %v", err)
	}
}

func TestOriginBindingCleanupSurvivesKilledSession(t *testing.T) {
	server := tmuxtest.NewServer(t)

	handle, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{Name: "attachment"})
	if err != nil {
		t.Fatal(err)
	}

	name := "@sesh_origin_test"
	if err := server.Options().SetUser(t.Context(), name, "owned"); err != nil {
		t.Fatal(err)
	}

	if err := handle.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := NewGotmuxLiveServer(server).clearOriginBinding(t.Context(), handle, name, "owned"); err != nil {
		t.Fatal(err)
	}

	value, err := server.Options().User(t.Context(), name)
	if _, present := value.Local.Get(); err != nil || present {
		t.Fatalf("killed session left a stale terminal binding: %v", err)
	}
}

func TestOriginBindingCleanupRefusesDifferentDaemon(t *testing.T) {
	owned := tmuxtest.NewServer(t)

	sessions, err := owned.Sessions(t.Context())
	if err != nil || len(sessions) == 0 {
		t.Fatalf("owned sessions: %v", err)
	}

	foreign := tmuxtest.NewServer(t)

	name := "@sesh_origin_test"
	if err := foreign.Options().SetUser(t.Context(), name, "same-id"); err != nil {
		t.Fatal(err)
	}

	if err := NewGotmuxLiveServer(foreign).clearOriginBinding(t.Context(), sessions[0].Handle(), name, "same-id"); err != nil {
		t.Fatal(err)
	}

	value, err := foreign.Options().User(t.Context(), name)
	if id, present := value.Local.Get(); err != nil || !present || id != "same-id" {
		t.Fatalf("cleanup touched a different daemon binding: %v", err)
	}
}

func TestOriginAttachmentRefusesExistingTerminalBinding(t *testing.T) {
	server := tmuxtest.NewServer(t)

	sessions, err := server.Sessions(t.Context())
	if err != nil || len(sessions) == 0 {
		t.Fatalf("fixture sessions: %v", err)
	}

	terminal, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = terminal.Close() }()

	name, err := OriginAttachmentOption(terminal)
	if err != nil {
		t.Fatal(err)
	}

	if err := server.Options().SetUser(t.Context(), name, "foreign-attachment"); err != nil {
		t.Fatal(err)
	}

	identity := sessions[0].Handle().ServerIdentity()

	record := StoredCapability{Target: AttachTarget{
		SessionID: string(sessions[0].ID), TmuxServerPID: identity.PID, TmuxServerStartedAt: identity.Started,
	}, Grant: BridgeGrant{AttachmentID: "new-attachment"}}
	if err := NewGotmuxLiveServer(server).AttachWithOrigin(t.Context(), record, AttachStreams{In: terminal, Out: terminal, Err: terminal}); err == nil {
		t.Fatal("attachment proceeded despite another binding")
	}

	value, err := server.Options().User(t.Context(), name)
	if got, _ := value.Local.Get(); err != nil || got != "foreign-attachment" {
		t.Fatalf("foreign binding was overwritten: %q, %v", got, err)
	}

	clients, err := server.Clients(t.Context())
	if err != nil || len(clients) != 0 {
		t.Fatalf("refused attachment created a client: %d, %v", len(clients), err)
	}
}
