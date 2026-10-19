//go:build linux && integration

package cli_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/remote"
	"github.com/zigai/sesh/internal/session"
)

func TestRemotePickerReturnsAcrossSSH(t *testing.T) {
	requirePickerTermtrace(t)

	fixture := newRemoteSSHFixture(t)
	for key, value := range remoteFixtureEnvironment(fixture) {
		t.Setenv(key, value)
	}

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("SESH_TMUX_CLIENT", "")
	t.Setenv("TERM", "xterm-256color")

	peer, err := tmuxserver.Open(fixture.connection.TmuxSocket)
	if err != nil {
		t.Fatal(err)
	}

	remoteTarget := newRemoteReturnSession(t, peer, fixture, "remote-work", "REMOTE_READY")

	origin := newCaptureCLIServer(t)

	start := newRemoteReturnSession(t, origin, fixture, "origin-start", "ORIGIN_READY")

	destination := newRemoteReturnSession(t, origin, fixture, "origin-destination", "RETURNED_TO_ORIGIN")

	configPath := filepath.Join(fixture.configHome, "sesh", "config.toml")
	writeTestConfig(t, configPath, fmt.Sprintf("[agent_sessions]\nenabled = false\n\n[remote]\nenabled = true\nreturn_to_origin = false\nssh_path = %q\n", fixture.sshWrapper)+
		remoteConnectionsTOML(fixture.connection))

	publishRemoteReturnCatalog(t, fixture, peer)

	executable, err := pickerTestExecutable()
	if err != nil {
		t.Fatal(err)
	}

	alias := "sesh-origin-return-" + strconv.Itoa(os.Getpid())

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	record := exec.CommandContext(ctx, termtraceExecutable, "record", "-d", alias,
		"--until-screen", "ORIGIN_READY", "--timeout", "8s", "--max-duration", "30s", "--no-input",
		executable, "-test.run=^TestRemoteReturnTerminalHelper$")

	record.Env = append(os.Environ(), "SESH_RETURN_HELPER=1", "SESH_RETURN_SOCKET="+origin.Endpoint().SocketPath, "SESH_RETURN_SESSION="+string(start.ID()))
	if output, err := record.CombinedOutput(); err != nil {
		t.Fatalf("record origin terminal: %v: %s", err, output)
	}

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()

		_ = exec.CommandContext(stopCtx, termtraceExecutable, "stop", "-q", alias, "--wait", "--timeout-ms", "1500").Run()
	})

	attachLine := fmt.Sprintf("%s remote attach fixture remote-work --origin-socket %s", fixture.connection.SeshPath, origin.Endpoint().SocketPath)
	remoteReturnCommand(t, ctx, "send", alias, "text:"+attachLine, "enter", "--until-screen", "REMOTE_READY", "--timeout", "8s")
	assertRemoteBridgeAuthentication(t, fixture, peer)
	popupLine := fixture.connection.SeshPath + " pick --ui popup"
	remoteReturnCommand(t, ctx, "send", "-q", alias, "text:"+popupLine, "enter", "--until-screen", "origin-destination", "--timeout", "8s")
	assertPickerScreenRows(t, ctx, alias, []string{"origin-destination", "origin-start", "remote-work"}, nil)
	remoteReturnCommand(t, ctx, "send", "-q", alias, "--text", "origin-destination", "--until-screen", "> origin-destination", "--timeout", "5s")
	remoteReturnCommand(t, ctx, "send", "-q", alias, "enter", "--until-screen", "RETURNED_TO_ORIGIN", "--timeout", "8s")

	clients, err := origin.Clients(t.Context())
	if err != nil || len(clients) != 1 {
		t.Fatalf("origin clients after return = %d, %v", len(clients), err)
	}

	if id, _ := clients[0].SessionID.Get(); id != destination.ID() {
		t.Fatalf("origin returned to %q, want %q", id, destination.ID())
	}

	remoteClients, err := remoteTarget.Clients(t.Context())
	if err != nil || len(remoteClients) != 0 {
		t.Fatalf("remote clients after return = %d, %v", len(remoteClients), err)
	}

	assertRemoteReturnCleanup(t, fixture, peer)

	if err := clients[0].Handle().Detach(t.Context()); err != nil {
		t.Fatal(err)
	}

	remoteReturnCommand(t, ctx, "status", "-q", alias, "--wait-complete", "--timeout-ms", "5000")
}

func assertRemoteBridgeAuthentication(t *testing.T, fixture remoteSSHFixture, peer *gotmux.Server) {
	t.Helper()

	record := remoteReturnCapability(t, fixture)
	if _, err := remote.NewBridgeClient(record.BridgeSocket, "invalid-token").GetCatalog(t.Context()); !errors.Is(err, remote.ErrBridgeAuthFailed) {
		t.Fatalf("unauthenticated forwarded socket request = %v", err)
	}

	arguments, err := os.ReadFile(fixture.argumentLog)
	if err != nil || strings.Contains(string(arguments), record.Grant.Token) {
		t.Fatalf("capability escaped into SSH arguments or arguments unavailable: %v", err)
	}

	options, err := peer.Options().List(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, option := range options {
		if value, _ := option.Value.Get(); strings.Contains(value, record.Grant.Token) {
			t.Fatal("capability escaped into tmux options")
		}
	}
}

func TestRemoteReturnTerminalHelper(t *testing.T) {
	if os.Getenv("SESH_RETURN_HELPER") != "1" {
		return
	}

	server, err := tmuxserver.Open(os.Getenv("SESH_RETURN_SOCKET"))
	if err != nil {
		t.Fatal(err)
	}

	handle, err := server.Session(t.Context(), gotmux.SessionID(os.Getenv("SESH_RETURN_SESSION")))
	if err != nil {
		t.Fatal(err)
	}

	if err := handle.Attach(t.Context(), gotmux.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, gotmux.AttachOptions{}); err != nil {
		t.Fatal(err)
	}
}

func remoteFixtureEnvironment(fixture remoteSSHFixture) map[string]string {
	return map[string]string{
		"HOME": filepath.Join(fixture.root, "home"), "XDG_STATE_HOME": fixture.stateHome,
		"XDG_CACHE_HOME": fixture.cacheHome, "XDG_DATA_HOME": fixture.dataHome, "XDG_CONFIG_HOME": fixture.configHome,
	}
}

func publishRemoteReturnCatalog(t *testing.T, fixture remoteSSHFixture, peer *gotmux.Server) {
	t.Helper()

	rows, err := session.ListLive(t.Context(), peer, "")
	if err != nil {
		t.Fatal(err)
	}

	bridgeDir, err := remote.BridgeDir(fixture.stateHome)
	if err != nil {
		t.Fatal(err)
	}

	catalog := remote.EmptyCatalog()
	catalog.Connections[fixture.connection.ID] = remote.ConnectionState{
		ID: fixture.connection.ID, Label: fixture.connection.Label, ConfigDigest: remote.ConnectionDigest(fixture.connection),
		PeerID: fixture.peerID, Connected: true, Sequence: 1, LastSeen: time.Now(), BridgeDir: bridgeDir,
		Snapshot: remote.Snapshot{Sessions: rows, Actions: defaultRemoteActions(), Health: remote.UnavailableHealth()},
	}

	cachePath, err := remote.Dir(fixture.cacheHome)
	if err != nil {
		t.Fatal(err)
	}

	dir, err := privatefs.Ensure(cachePath)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = dir.Close() }()

	if err := remote.WriteCatalog(dir, catalog); err != nil {
		t.Fatal(err)
	}
}

func defaultRemoteActions() []action.ID {
	ids := make([]action.ID, 0)

	for _, entry := range action.All() {
		if action.PolicyFor(config.DefaultConfig(), entry.Describe()).AllowRemote {
			ids = append(ids, entry.Describe().ID)
		}
	}

	return ids
}

func assertRemoteReturnCleanup(t *testing.T, fixture remoteSSHFixture, peer *gotmux.Server) {
	t.Helper()

	options, err := peer.Options().List(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, option := range options {
		if strings.HasPrefix(option.Name, "@sesh_origin_") {
			t.Fatal("remote attachment binding survived cleanup")
		}
	}

	bridgeDir, err := remote.BridgeDir(fixture.stateHome)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(bridgeDir)
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			t.Fatal("private capability survived completed attachment")
		}
	}
}

func remoteReturnCommand(t *testing.T, ctx context.Context, arguments ...string) {
	t.Helper()

	if output, err := exec.CommandContext(ctx, termtraceExecutable, arguments...).CombinedOutput(); err != nil {
		alias := arguments[1]
		if alias == "-q" {
			alias = arguments[2]
		}

		screen, screenErr := exec.CommandContext(ctx, termtraceExecutable, "screen", alias).CombinedOutput()
		t.Fatalf("terminal step: %v: %s\nScreen (%v):\n%s", err, output, screenErr, screen)
	}
}

func newRemoteReturnSession(t *testing.T, server *gotmux.Server, fixture remoteSSHFixture, name, marker string) gotmux.Session {
	t.Helper()

	handle, err := server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name: name, Program: gotmux.Exec("/bin/sh", "-c", "printf '"+marker+"\\n'; exec /bin/sh"),
		Env: remoteFixtureEnvironment(fixture),
	})
	if err != nil {
		t.Fatal(err)
	}

	return handle
}

func remoteReturnCapability(t *testing.T, fixture remoteSSHFixture) remote.StoredCapability {
	t.Helper()

	capabilities, err := remote.CapabilityReader(fixture.stateHome)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = capabilities.Close() }()

	names, err := capabilities.Names()
	if err != nil {
		t.Fatal(err)
	}

	var record remote.StoredCapability

	for _, name := range names {
		if strings.HasSuffix(name, ".json") {
			id := strings.TrimSuffix(strings.TrimPrefix(name, "attach-"), ".json")

			record, err = remote.ReadCapability(capabilities, id, time.Now())
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	if record.Grant.Token == "" {
		t.Fatal("attachment did not publish its private capability")
	}

	return record
}
