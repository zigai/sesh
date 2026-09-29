//go:build integration

package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/cli"
	"github.com/zigai/sesh/internal/cli/clibase"
	cmdremote "github.com/zigai/sesh/internal/cli/cmd/remote"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/remote"
)

func remotePeerActionFixture(t *testing.T) (*tmux.Server, []byte, string) {
	t.Helper()

	server := newCaptureCLIServer(t)
	stateHome := filepath.Join(t.TempDir(), "state")
	cacheHome := filepath.Join(t.TempDir(), "cache")
	dataHome := filepath.Join(t.TempDir(), "data")
	configHome := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)

	peerPath, err := remote.PeerStateDir(stateHome)
	if err != nil {
		t.Fatal(err)
	}

	peerDir, err := privatefs.Ensure(peerPath)
	if err != nil {
		t.Fatal(err)
	}

	peerID, err := remote.LoadOrCreatePeerID(peerDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := peerDir.Close(); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[remote.permissions]\ncreate = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	request := remote.ActionRequest{
		Operation: remote.ActionCreate, ConnectionID: "origin", ConfigDigest: "digest",
		PeerID: peerID, TmuxServerSocket: server.Endpoint().SocketPath, SessionName: "remote-created", Directory: t.TempDir(),
	}

	request.InvocationID, err = remote.NewInvocationID()
	if err != nil {
		t.Fatal(err)
	}

	request.IntentDigest, err = remote.IntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}

	payload, err := remote.EncodeActionRequest(request)
	if err != nil {
		t.Fatal(err)
	}

	return server, payload, configPath
}

func runRemotePeerActionEntrypoint(t *testing.T, payload []byte, configPath string) remote.ActionResult {
	t.Helper()

	var stdout, stderr bytes.Buffer

	err := cli.ExecuteApplication(t.Context(), []string{"remote", "peer", "action", "--config", configPath}, bytes.NewReader(payload), &stdout, &stderr)
	if err != nil {
		t.Fatalf("peer action failed: %v; stderr=%q", err, stderr.String())
	}

	result, err := remote.DecodeActionResult(stdout.Bytes())
	if err != nil {
		t.Fatalf("DecodeActionResult(%q): %v", stdout.String(), err)
	}

	return result
}

func TestRemotePeerActionEntrypointCreatesAndReplaysOnDisposableServer(t *testing.T) {
	server, payload, configPath := remotePeerActionFixture(t)
	first := runRemotePeerActionEntrypoint(t, payload, configPath)

	second := runRemotePeerActionEntrypoint(t, payload, configPath)
	if !first.OK || second != first {
		t.Fatalf("first=%#v second=%#v", first, second)
	}

	sessions, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	count := 0

	for _, candidate := range sessions {
		if candidate.Name == "remote-created" {
			count++
		}
	}

	if count != 1 {
		t.Fatalf("created session count = %d, want 1", count)
	}
}

func TestRemoteCommandTreeMarksProtocolChannelsLive(t *testing.T) {
	if cmdremote.NewDaemonMonitorCommand().Annotations[clibase.AnnotationLiveIO] != "true" {
		t.Error("daemon remote-monitor missing live I/O annotation")
	}

	root := cmdremote.NewCommand()
	for _, path := range [][]string{{"peer", "stream"}, {"peer", "action"}, {"peer", "lookup"}, {"peer", "attach-prepare"}, {"peer", "attach"}, {"attach"}} {
		command, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("Find(%v): %v", path, err)
		}

		if command.Annotations[clibase.AnnotationLiveIO] != "true" {
			t.Errorf("%v missing live I/O annotation", path)
		}
	}
}
