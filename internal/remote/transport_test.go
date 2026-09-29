//go:build linux || darwin

package remote

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/config"
)

func testRemoteConfig() config.RemoteConfig {
	remote := config.DefaultConfig().Remote
	remote.Enabled = true
	remote.SSHPath = "ssh"
	remote.ConnectTimeout = "5s"
	remote.HeartbeatInterval = "19s"
	remote.TmuxPollInterval = "7s"
	remote.MaxParallelConnects = 4
	remote.Connections = []config.RemoteConnectionConfig{{ID: "work", Label: "Work", Host: "work.example"}}

	return remote
}

func scriptSSH(t *testing.T, script string) func(ctx context.Context, executable string, arguments ...string) *exec.Cmd {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-ssh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}

	return func(ctx context.Context, _ string, arguments ...string) *exec.Cmd {
		return exec.CommandContext(ctx, path, arguments...)
	}
}

func TestSSHCommandUsesConfiguredExecutableAndPolicy(t *testing.T) {
	remote := testRemoteConfig()
	remote.SSHPath = "/custom/ssh"

	command, err := NewSSHCommand(t.Context(), remote, remote.Connections[0], SSHCommandOptions{}, "true")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"/custom/ssh", "-T", "-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=19", "-o", "ServerAliveCountMax=3",
		"-o", "ClearAllForwardings=yes",
		"--", "work.example", "'true'",
	}

	if !slices.Equal(command.Args, want) {
		t.Fatalf("SSH argv = %#v, want %#v", command.Args, want)
	}
}

func TestSSHCommandUsesDefaultAndMinimumTimeouts(t *testing.T) {
	tests := []struct {
		name      string
		config    config.RemoteConfig
		connect   string
		heartbeat string
	}{
		{name: "defaults", config: config.RemoteConfig{}, connect: "ConnectTimeout=10", heartbeat: "ServerAliveInterval=15"},
		{name: "invalid", config: config.RemoteConfig{ConnectTimeout: "invalid", HeartbeatInterval: "-1s"}, connect: "ConnectTimeout=10", heartbeat: "ServerAliveInterval=15"},
		{name: "subsecond", config: config.RemoteConfig{ConnectTimeout: "20ms", HeartbeatInterval: "50ms"}, connect: "ConnectTimeout=1", heartbeat: "ServerAliveInterval=1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, err := NewSSHCommand(t.Context(), test.config, config.RemoteConnectionConfig{Host: "work"}, SSHCommandOptions{}, "true")
			if err != nil {
				t.Fatal(err)
			}

			if command.Args[0] != "ssh" || !slices.Contains(command.Args, test.connect) || !slices.Contains(command.Args, test.heartbeat) {
				t.Fatalf("SSH argv = %#v, want default executable with %s and %s", command.Args, test.connect, test.heartbeat)
			}
		})
	}
}

func TestSSHCommandNeverUnlinksRemoteSocketPaths(t *testing.T) {
	remote := testRemoteConfig()

	command, err := NewSSHCommand(t.Context(), remote, remote.Connections[0], SSHCommandOptions{
		Interactive: true,
		Forward: []SocketForward{{
			RemotePath: "/home/remote/.local/state/sesh/remote/bridge/attach-0123456789abcdef0123456789abcdef.sock",
			LocalPath:  "/run/user/1000/sesh/bridge.sock",
		}},
	}, "remote", "peer", "attach", "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(command.Args, " ")

	if strings.Contains(joined, "StreamLocalBindUnlink=yes") {
		t.Fatalf("SSH argv enables remote unlink semantics: %#v", command.Args)
	}

	if slices.Contains(command.Args, "ClearAllForwardings=yes") || strings.Contains(joined, "ClearAllForwardings=yes") {
		t.Fatalf("forwarding command must not enable ClearAllForwardings: %#v", command.Args)
	}

	for _, want := range []string{
		"-t", "-o ExitOnForwardFailure=yes", "-o StreamLocalBindUnlink=no",
		"-R", "/home/remote/.local/state/sesh/remote/bridge/attach-0123456789abcdef0123456789abcdef.sock:/run/user/1000/sesh/bridge.sock",
	} {
		if !slices.Contains(command.Args, want) && !strings.Contains(joined, want) {
			t.Fatalf("SSH argv = %#v, want %q", command.Args, want)
		}
	}

	if slices.Contains(command.Args, "-nT") || slices.Contains(command.Args, "BatchMode=yes") {
		t.Fatalf("interactive attach argv must not force batch mode: %#v", command.Args)
	}
}

func TestSSHCommandRejectsOptionLikeHostAndInvalidForwards(t *testing.T) {
	remote := testRemoteConfig()

	for _, badHost := range []string{"-oProxyCommand=rm", "-V", "--bad", "   "} {
		conn := config.RemoteConnectionConfig{ID: "bad", Host: badHost}
		if _, err := NewSSHCommand(t.Context(), remote, conn, SSHCommandOptions{}, "true"); !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("NewSSHCommand(host=%q) error = %v, want %v", badHost, err, ErrInvalidFrame)
		}
	}

	conn := remote.Connections[0]

	badForwards := [][]SocketForward{
		{{RemotePath: "relative.sock", LocalPath: "/valid/path"}},
		{{RemotePath: "/valid/path", LocalPath: "relative.sock"}},
		{{RemotePath: "/dirty/../path", LocalPath: "/valid/path"}},
	}
	for _, fwd := range badForwards {
		if _, err := NewSSHCommand(t.Context(), remote, conn, SSHCommandOptions{Forward: fwd}, "true"); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("NewSSHCommand(fwd=%#v) error = %v, want %v", fwd, err, ErrUnsafePath)
		}
	}
}

func TestSSHCommandEffectiveOptionsWithOpenSSH(t *testing.T) {
	remote := testRemoteConfig()

	verifyNonInteractiveOpenSSHOptions(t, remote)
	verifyInteractiveForwardOpenSSHOptions(t, remote)
}

func verifyNonInteractiveOpenSSHOptions(t *testing.T, remote config.RemoteConfig) {
	t.Helper()

	nonInteractive, err := NewSSHCommand(t.Context(), remote, remote.Connections[0], SSHCommandOptions{}, "true")
	if err != nil {
		t.Fatal(err)
	}

	gArgs := append([]string{"-G"}, nonInteractive.Args[1:len(nonInteractive.Args)-1]...)

	out, err := exec.CommandContext(t.Context(), "ssh", gArgs...).Output()
	if err != nil {
		t.Skipf("local ssh -G unavailable: %v", err)
	}

	text := string(out)
	for _, expected := range []string{"batchmode yes", "clearallforwardings yes", "connecttimeout 5", "serveraliveinterval 19"} {
		if !strings.Contains(text, expected) {
			t.Errorf("ssh -G missing %s:\n%s", expected, text)
		}
	}
}

func verifyInteractiveForwardOpenSSHOptions(t *testing.T, remote config.RemoteConfig) {
	t.Helper()

	interactive, err := NewSSHCommand(t.Context(), remote, remote.Connections[0], SSHCommandOptions{
		Interactive: true,
		Forward: []SocketForward{{
			RemotePath: "/tmp/remote-bridge.sock",
			LocalPath:  "/tmp/local-bridge.sock",
		}},
	}, "true")
	if err != nil {
		t.Fatal(err)
	}

	gArgsFwd := append([]string{"-G"}, interactive.Args[1:len(interactive.Args)-1]...)

	outFwd, err := exec.CommandContext(t.Context(), "ssh", gArgsFwd...).Output()
	if err != nil {
		t.Skipf("local ssh -G forward check unavailable: %v", err)
	}

	fwdText := string(outFwd)
	if strings.Contains(fwdText, "clearallforwardings yes") {
		t.Errorf("forwarding command has clearallforwardings yes:\n%s", fwdText)
	}

	for _, expected := range []string{
		"exitonforwardfailure yes", "streamlocalbindunlink no",
		"remoteforward /tmp/remote-bridge.sock /tmp/local-bridge.sock",
	} {
		if !strings.Contains(fwdText, expected) {
			t.Errorf("forwarding command missing %s:\n%s", expected, fwdText)
		}
	}
}

func TestSSHCommandPreservesRemoteArgumentsThroughShell(t *testing.T) {
	arguments := []string{"", "with spaces", "single'quote", "$(printf wrong)", "line\nbreak", "; exit 23", "\\", "\"quoted\""}
	remoteCommand := append([]string{"printf", "%s\\000"}, arguments...)

	command, err := NewSSHCommand(t.Context(), config.RemoteConfig{}, config.RemoteConnectionConfig{Host: "work"}, SSHCommandOptions{}, remoteCommand...)
	if err != nil {
		t.Fatal(err)
	}

	remoteShell := exec.CommandContext(t.Context(), "sh", "-c", command.Args[len(command.Args)-1])

	output, err := remoteShell.Output()
	if err != nil {
		t.Fatal(err)
	}

	if want := strings.Join(arguments, "\x00") + "\x00"; string(output) != want {
		t.Fatalf("remote arguments = %q, want %q", output, want)
	}
}

func TestPeerEntryPointIsFixedPerChannel(t *testing.T) {
	connection := config.RemoteConnectionConfig{ID: "work", Host: "work.example", SeshPath: "/opt/my sesh"}

	for channel, want := range map[Channel][]string{
		ChannelStream:        {"'/opt/my sesh'", "'remote'", "'peer'", "'stream'"},
		ChannelAction:        {"'/opt/my sesh'", "'remote'", "'peer'", "'action'"},
		ChannelAttachPrepare: {"'/opt/my sesh'", "'remote'", "'peer'", "'attach-prepare'"},
		ChannelAttach:        {"'/opt/my sesh'", "'remote'", "'peer'", "'attach'"},
	} {
		entry := PeerEntryPoint(connection, channel)

		if entry[0] != connection.SeshPath || entry[1] != "remote" || entry[2] != "peer" || entry[3] != string(channel) {
			t.Fatalf("PeerEntryPoint(%s) = %#v, want the fixed native path", channel, entry)
		}

		if got := ShellJoin(entry); got != strings.Join(want, " ") {
			t.Fatalf("ShellJoin(%s) = %q", channel, got)
		}
	}
}

func TestConnectionDigestTracksTransportInputs(t *testing.T) {
	base := config.RemoteConnectionConfig{ID: "work", Host: "work.example", SeshPath: "/usr/bin/sesh", TmuxSocket: "work"}
	digest := ConnectionDigest(base)

	if digest != ConnectionDigest(base) {
		t.Fatal("ConnectionDigest() is not stable for one configuration")
	}

	for name, mutate := range map[string]func(*config.RemoteConnectionConfig){
		"host":        func(c *config.RemoteConnectionConfig) { c.Host = "other.example" },
		"peer path":   func(c *config.RemoteConnectionConfig) { c.SeshPath = "/other/sesh" },
		"tmux socket": func(c *config.RemoteConnectionConfig) { c.TmuxSocket = "other" },
		"id":          func(c *config.RemoteConnectionConfig) { c.ID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			mutated := base
			mutate(&mutated)

			if ConnectionDigest(mutated) == digest {
				t.Fatalf("ConnectionDigest() ignored a %s change", name)
			}
		})
	}
}

func TestClassifySSHFailureUsesFixedPrivacySafeReasons(t *testing.T) {
	tests := []struct {
		name        string
		ctxErr      error
		diagnostics string
		want        error
		code        string
	}{
		{name: "authentication", diagnostics: "user@host: Permission denied (publickey). PRIVATE", want: ErrSSHAuthUnavailable, code: ConnectionErrorAuthUnavailable},
		{name: "host key", diagnostics: "Host key verification failed. PRIVATE", want: ErrSSHHostKeyRequired, code: ConnectionErrorHostKeyRequired},
		{name: "context timeout", ctxErr: context.DeadlineExceeded, diagnostics: "PRIVATE", want: context.DeadlineExceeded, code: ConnectionErrorTimeout},
		{name: "context canceled", ctxErr: context.Canceled, diagnostics: "PRIVATE", want: context.Canceled, code: ConnectionErrorUnavailable},
		{name: "reported timeout", diagnostics: "ssh: connect to host: Connection timed out PRIVATE", want: context.DeadlineExceeded, code: ConnectionErrorTimeout},
		{name: "remote peer missing", diagnostics: "sh: sesh: command not found PRIVATE", want: ErrRemotePeerUnavailable, code: ConnectionErrorRemotePeerUnavailable},
		{name: "forwarding failed", diagnostics: "remote port forwarding failed for listen path PRIVATE", want: ErrForwardingFailed, code: ConnectionErrorUnavailable},
		{name: "other", diagnostics: "PRIVATE network detail", want: ErrConnectionUnavailable, code: ConnectionErrorUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classified := ClassifySSHFailure(test.ctxErr, test.diagnostics)

			if !errors.Is(classified, test.want) {
				t.Fatalf("classification = %v, want %v", classified, test.want)
			}

			if got := ConnectionErrorCode(classified); got != test.code {
				t.Fatalf("reason = %q, want %q", got, test.code)
			}

			if strings.Contains(classified.Error(), "PRIVATE") {
				t.Fatalf("classification exposed diagnostics: %v", classified)
			}
		})
	}
}

func TestConnectionErrorCodeClassifiesProtocolFailures(t *testing.T) {
	for _, protocolErr := range []error{ErrInvalidFrame, ErrUnsupportedProtocol, ErrFrameTooLarge} {
		if got := ConnectionErrorCode(protocolErr); got != ConnectionErrorIncompatibleProtocol {
			t.Fatalf("protocol reason = %q, want %q", got, ConnectionErrorIncompatibleProtocol)
		}
	}

	if got := ConnectionErrorCode(ErrPeerMismatch); got != ConnectionErrorPeerMismatch {
		t.Fatalf("peer reason = %q, want %q", got, ConnectionErrorPeerMismatch)
	}

	if got := ConnectionErrorCode(ErrCatalogCapacity); got != ConnectionErrorCacheCapacity {
		t.Fatalf("capacity reason = %q, want %q", got, ConnectionErrorCacheCapacity)
	}

	if got := ConnectionErrorCode(nil); got != "" {
		t.Fatalf("nil reason = %q, want an empty code", got)
	}
}

func TestDiagnosticsBufferIsBounded(t *testing.T) {
	buffer := NewDiagnosticsBuffer(16)

	if written, err := buffer.Write([]byte("0123456789abcdefghij")); written != 20 || err != nil {
		t.Fatalf("Write() = (%d, %v), want (20, nil)", written, err)
	}

	if got := buffer.String(); got != "0123456789abcdef" {
		t.Fatalf("String() = %q, want the bounded prefix", got)
	}
}

func TestFinishCommandJoinsOwnedProcessCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	command := exec.CommandContext(ctx, "sh", "-c", "exec sleep 60")

	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	cancel()

	finishErr := FinishCommand(ctx, command, stdout, NewDiagnosticsBuffer(maxDiagnosticsBytes))
	if !errors.Is(finishErr, context.Canceled) {
		t.Fatalf("FinishCommand() error = %v, want cancellation", finishErr)
	}

	if command.ProcessState == nil {
		t.Fatal("FinishCommand() did not wait for the owned process")
	}
}

func TestPeerPolicyRefusesUnpermittedOperations(t *testing.T) {
	policy := PeerPolicy{Create: true, Kill: false, Sockets: []string{"/tmp/tmux-work"}}

	if err := policy.Check(ActionCreate, "/tmp/tmux-work"); err != nil {
		t.Fatalf("Check(create) error = %v", err)
	}

	if err := policy.Check(ActionKill, "/tmp/tmux-work"); !errors.Is(err, ErrPeerForbidden) {
		t.Fatalf("Check(kill) error = %v, want %v", err, ErrPeerForbidden)
	}

	if err := policy.Check(ActionCreate, "/tmp/tmux-other"); !errors.Is(err, ErrPeerForbidden) {
		t.Fatalf("Check(create on other socket) error = %v, want %v", err, ErrPeerForbidden)
	}

	if err := policy.Check(Action("push"), "/tmp/tmux-work"); !errors.Is(err, ErrPeerUnsupported) {
		t.Fatalf("Check(unknown) error = %v, want %v", err, ErrPeerUnsupported)
	}
}

func TestPermissionsAllowMirrorsOriginPolicy(t *testing.T) {
	permissions := config.RemotePermissionConfig{Create: true, Rename: true, Kill: false, Delete: false, Snapshot: true, Restore: false}

	if !PermissionsAllow(permissions, ActionCreate) || !PermissionsAllow(permissions, ActionSnapshot) {
		t.Fatal("PermissionsAllow() refused a permitted operation")
	}

	if PermissionsAllow(permissions, ActionKill) || PermissionsAllow(permissions, ActionRestore) || PermissionsAllow(permissions, Action("push")) {
		t.Fatal("PermissionsAllow() admitted a refused operation")
	}
}
