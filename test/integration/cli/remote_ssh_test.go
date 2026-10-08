//go:build integration

package cli_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/remote"
)

type remoteSSHFixture struct {
	root, stateHome, cacheHome, dataHome, configHome string
	sshWrapper, argumentLog, peerID                  string
	connection                                       config.RemoteConnectionConfig
}

type remoteSSHTools struct{ sshd, sshKeygen, ssh string }

func TestRemoteActionCrossesDisposableOpenSSHServer(t *testing.T) {
	fixture := newRemoteSSHFixture(t)
	remoteConfig := config.DefaultConfig().Remote
	remoteConfig.Enabled = true
	remoteConfig.SSHPath = fixture.sshWrapper
	remoteConfig.Connections = []config.RemoteConnectionConfig{fixture.connection}
	state := remote.ConnectionState{
		ID: fixture.connection.ID, ConfigDigest: remote.ConnectionDigest(fixture.connection),
		PeerID: fixture.peerID, Connected: true,
	}
	options := remote.ActionOriginOptions{Remote: remoteConfig, Connection: fixture.connection, State: state}

	input, err := action.SessionCreate.EncodeInput(action.CreateSession{Name: "over-ssh", Directory: fixture.root})
	if err != nil {
		t.Fatal(err)
	}

	request, err := remote.NewActionRequest(options, action.SessionCreate.ID, input)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("XDG_STATE_HOME", fixture.stateHome)
	t.Setenv("XDG_CACHE_HOME", fixture.cacheHome)
	t.Setenv("XDG_DATA_HOME", fixture.dataHome)
	t.Setenv("XDG_CONFIG_HOME", fixture.configHome)

	result, err := remote.RunAction(t.Context(), options, request)
	if err != nil || !result.OK {
		t.Fatalf("RunAction() = %#v, %v, want success", result, err)
	}

	result2, err := remote.RunAction(t.Context(), options, request)
	if err != nil || !reflect.DeepEqual(result2, result) {
		t.Fatalf("replay = %#v, %v; want %#v", result2, err, result)
	}

	lookup, err := remote.NewInvocationLookupRequest(options, request.InvocationID)
	if err != nil {
		t.Fatal(err)
	}

	lookedUp, err := remote.RunInvocationLookup(t.Context(), options, lookup)
	if err != nil || !reflect.DeepEqual(lookedUp, result) {
		t.Fatalf("lookup = %#v, %v; want %#v", lookedUp, err, result)
	}

	assertRemoteRequestOffArgv(t, fixture.argumentLog, request)
}

func newRemoteSSHFixture(t *testing.T) remoteSSHFixture {
	t.Helper()

	tools := remoteIntegrationTools(t)

	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}

	root, err := os.MkdirTemp("", "sesh-ssh-") //nolint:usetesting // Unix socket paths need a short name; cleanup is registered below.
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(root) })

	fixture := remoteSSHFixture{
		root: root, stateHome: filepath.Join(root, "state"), cacheHome: filepath.Join(root, "cache"),
		dataHome: filepath.Join(root, "data"), configHome: filepath.Join(root, "config"),
		sshWrapper: filepath.Join(root, "ssh-fixture"), argumentLog: filepath.Join(root, "ssh-arguments"),
		peerID: "", connection: config.RemoteConnectionConfig{},
	}
	sshDir := filepath.Join(root, "home", ".ssh")
	makeRemoteFixtureDirs(t, sshDir, fixture)
	clientKey, hostKey := generateRemoteSSHKeys(t, tools.sshKeygen, sshDir, root)
	port := reserveLoopbackPort(t)
	clientConfig, sshdConfig := writeRemoteSSHConfigs(t, account.Username, sshDir, root, clientKey, hostKey, port)
	writeRemoteSSHWrapper(t, fixture.sshWrapper, fixture.argumentLog, tools.ssh, clientConfig)
	startRemoteSSHD(t, tools.sshd, sshdConfig, port, filepath.Join(root, "sshd.log"))

	binary := filepath.Join(root, "sesh")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../../cmd/sesh")
	build.Dir = "."

	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building sesh: %v: %s", err, output)
	}

	configPath := filepath.Join(fixture.configHome, "sesh", "config.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	fixture.peerID = initializeRemotePeerID(t, fixture.stateHome)
	tmuxServer := newCaptureCLIServer(t)
	fixture.connection = config.RemoteConnectionConfig{
		ID: "fixture", Label: "Fixture", Host: "sesh-fixture", SeshPath: binary,
		TmuxSocket: tmuxServer.Endpoint().SocketPath,
	}

	return fixture
}

func remoteIntegrationTools(t *testing.T) remoteSSHTools {
	t.Helper()

	return remoteSSHTools{
		sshd: requireRemoteIntegrationTool(t, "sshd"), sshKeygen: requireRemoteIntegrationTool(t, "ssh-keygen"),
		ssh: requireRemoteIntegrationTool(t, "ssh"),
	}
}

func makeRemoteFixtureDirs(t *testing.T, sshDir string, fixture remoteSSHFixture) {
	t.Helper()

	for _, path := range []string{
		sshDir, fixture.stateHome, fixture.cacheHome, fixture.dataHome, filepath.Join(fixture.configHome, "sesh"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func generateRemoteSSHKeys(t *testing.T, keygen, sshDir, root string) (string, string) {
	t.Helper()

	clientKey, hostKey := filepath.Join(sshDir, "id_ed25519"), filepath.Join(root, "host_ed25519")
	for _, key := range []string{clientKey, hostKey} {
		command := exec.CommandContext(t.Context(), keygen, "-q", "-t", "ed25519", "-N", "", "-f", key)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v: %s", err, output)
		}
	}

	return clientKey, hostKey
}

func reserveLoopbackPort(t *testing.T) int {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("loopback listener did not return a TCP address")
	}

	port := tcpAddress.Port

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	return port
}

func writeRemoteSSHConfigs(t *testing.T, username, sshDir, root, clientKey, hostKey string, port int) (string, string) {
	t.Helper()

	clientPublic, err := os.ReadFile(clientKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}

	authorized := filepath.Join(root, "authorized_keys")
	if err := os.WriteFile(authorized, clientPublic, 0o600); err != nil {
		t.Fatal(err)
	}

	sshdConfig := filepath.Join(root, "sshd_config")

	sshdBody := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nPubkeyAuthentication yes\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nStrictModes no\nPermitRootLogin yes\nAllowUsers %s\nAcceptEnv XDG_STATE_HOME XDG_CACHE_HOME XDG_DATA_HOME XDG_CONFIG_HOME\nLogLevel ERROR\n", port, hostKey, filepath.Join(root, "sshd.pid"), authorized, username)
	if err := os.WriteFile(sshdConfig, []byte(sshdBody), 0o600); err != nil {
		t.Fatal(err)
	}

	hostPublic, err := os.ReadFile(hostKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}

	hostFields := strings.Fields(string(hostPublic))
	if len(hostFields) < 2 {
		t.Fatal("invalid generated host public key")
	}

	knownHosts := filepath.Join(sshDir, "known_hosts")

	knownHost := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", port, hostFields[0], hostFields[1])
	if err := os.WriteFile(knownHosts, []byte(knownHost), 0o600); err != nil {
		t.Fatal(err)
	}

	clientConfig := filepath.Join(sshDir, "config")

	clientBody := fmt.Sprintf("Host sesh-fixture\n  HostName 127.0.0.1\n  Port %d\n  User %s\n  IdentityFile %s\n  IdentitiesOnly yes\n  StrictHostKeyChecking yes\n  UserKnownHostsFile %s\n  SendEnv XDG_STATE_HOME XDG_CACHE_HOME XDG_DATA_HOME XDG_CONFIG_HOME\n", port, username, clientKey, knownHosts)
	if err := os.WriteFile(clientConfig, []byte(clientBody), 0o600); err != nil {
		t.Fatal(err)
	}

	return clientConfig, sshdConfig
}

func writeRemoteSSHWrapper(t *testing.T, path, argumentLog, ssh, clientConfig string) {
	t.Helper()

	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nexec %q -F %q \"$@\"\n", argumentLog, ssh, clientConfig)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { //nolint:gosec // executable fixture is isolated beneath t.TempDir.
		t.Fatal(err)
	}
}

func startRemoteSSHD(t *testing.T, sshd, configPath string, port int, diagnosticsPath string) {
	t.Helper()

	diagnostics, err := os.OpenFile(diagnosticsPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	command := exec.CommandContext(ctx, sshd, "-D", "-e", "-f", configPath)

	command.Stderr = diagnostics
	if err := command.Start(); err != nil {
		cancel()

		_ = diagnostics.Close()

		t.Fatal(err)
	}

	t.Cleanup(func() {
		cancel()

		_ = command.Wait()
		_ = diagnostics.Close()
	})
	waitForSSH(t, port, diagnosticsPath)
}

func initializeRemotePeerID(t *testing.T, stateHome string) string {
	t.Helper()

	peerPath, err := remote.PeerStateDir(stateHome)
	if err != nil {
		t.Fatal(err)
	}

	peerDir, err := privatefs.Ensure(peerPath)
	if err != nil {
		t.Fatal(err)
	}

	peerID, idErr := remote.LoadOrCreatePeerID(peerDir)

	closeErr := peerDir.Close()
	if err := errors.Join(idErr, closeErr); err != nil {
		t.Fatal(err)
	}

	return peerID
}

func assertRemoteRequestOffArgv(t *testing.T, argumentLog string, request remote.ActionRequest) {
	t.Helper()

	arguments, err := os.ReadFile(argumentLog)
	if err != nil {
		t.Fatal(err)
	}

	for _, privateValue := range []string{request.InvocationID, request.IntentDigest} {
		if strings.Contains(string(arguments), privateValue) {
			t.Fatal("remote action request data appeared in the ssh argument vector")
		}
	}
}

func requireRemoteIntegrationTool(t *testing.T, name string) string {
	t.Helper()

	path, err := exec.LookPath(name)
	if err == nil {
		return path
	}

	if os.Getenv("TMUX_INTEGRATION_REQUIRED") == "1" {
		t.Fatalf("required remote integration tool %s is unavailable", name)
	}

	t.Skipf("remote integration tool %s is unavailable", name)

	return ""
}

func waitForSSH(t *testing.T, port int, diagnosticsPath string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	dialer := net.Dialer{Timeout: 50 * time.Millisecond}

	for time.Now().Before(deadline) {
		connection, err := dialer.DialContext(t.Context(), "tcp", address)
		if err == nil {
			_ = connection.Close()
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	diagnostics, _ := os.ReadFile(diagnosticsPath)
	t.Fatalf("sshd did not listen: %s", diagnostics)
}
