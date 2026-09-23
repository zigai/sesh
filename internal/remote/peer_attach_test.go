//go:build linux || darwin

package remote

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/session"
)

func attachTargetFor(session sessionTarget) AttachTarget {
	return AttachTarget{
		PeerID:              testPeerID,
		TmuxServerSocket:    session.socket,
		TmuxServerPID:       session.pid,
		TmuxServerStartedAt: session.startedAt,
		SessionID:           session.id,
		SessionName:         session.name,
		SessionCreatedAt:    session.createdAt,
		PaneID:              "",
		CatalogSequence:     1,
	}
}

type sessionTarget struct {
	id        string
	name      string
	socket    string
	pid       int
	startedAt time.Time
	createdAt time.Time
}

func defaultSessionTarget() sessionTarget {
	return sessionTarget{
		id: "$1", name: testSessionName, socket: "/tmp/tmux.sock", pid: 42,
		startedAt: time.Unix(100, 0).UTC(), createdAt: time.Unix(200, 0).UTC(),
	}
}

func attachFixture(t *testing.T) (PeerAttachOptions, AttachBootstrap, *fakeLiveServer, string) {
	t.Helper()

	_, bridgePath := openTestBridgeDir(t)

	live := &fakeLiveServer{sessions: []session.Session{liveTargetSession()}, paneOwners: map[string]string{}}

	options := PeerAttachOptions{
		PeerID:    testPeerID,
		BridgeDir: bridgePath,
		Live:      live,
		Now:       func() time.Time { return time.Unix(1000, 0).UTC() },
	}

	remote := testRemoteConfig()

	bootstrap, err := PrepareAttachment(AttachOriginOptions{
		Remote:       remote,
		Connection:   remote.Connections[0],
		BridgeDir:    bridgePath,
		OriginSocket: "/run/user/1000/sesh-bridge.sock",
		OriginLabel:  "laptop",
		Target:       attachTargetFor(defaultSessionTarget()),
		Timeout:      time.Minute,
		Now:          options.Now,
	})
	if err != nil {
		t.Fatalf("PrepareAttachment() error = %v", err)
	}

	return options, bootstrap, live, bridgePath
}

func TestRunPeerAttachPreparePublishesPrivateCapability(t *testing.T) {
	options, bootstrap, live, bridgePath := attachFixture(t)

	result, err := RunPeerAttachPrepare(t.Context(), options, bootstrap)
	if err != nil {
		t.Fatalf("RunPeerAttachPrepare() error = %v", err)
	}

	if result.AttachmentID != bootstrap.Capability.AttachmentID || result.BridgeSocket != bootstrap.BridgeSocket {
		t.Fatalf("prepare result = %#v, want the confirmed attachment", result)
	}

	if created, killed, attached := live.counts(); created != 0 || killed != 0 || attached != 0 {
		t.Fatal("prepare performed a workspace mutation or attachment")
	}

	assertStoredCapabilityRecord(t, bridgePath, bootstrap, options.Now())
	assertCapabilityFileMode(t, bridgePath, bootstrap.Capability.AttachmentID)
}

func assertStoredCapabilityRecord(t *testing.T, bridgePath string, bootstrap AttachBootstrap, now time.Time) {
	t.Helper()

	dir, err := openPrivateDir(bridgePath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()

	record, err := ReadCapability(dir, bootstrap.Capability.AttachmentID, now)
	if err != nil {
		t.Fatalf("ReadCapability() error = %v", err)
	}

	if record.Grant.Token != bootstrap.Capability.Token || record.Target.SessionID != "$1" {
		t.Fatalf("stored capability = %#v, want the validated target and capability", record)
	}
}

func assertCapabilityFileMode(t *testing.T, bridgePath, attachmentID string) {
	t.Helper()

	name, err := CapabilityName(attachmentID)
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(bridgePath, name))
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("capability mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestRunPeerAttachPrepareRejectsForeignBridgeSocket(t *testing.T) {
	options, bootstrap, _, _ := attachFixture(t)

	bootstrap.BridgeSocket = "/tmp/foreign.sock"

	if _, err := RunPeerAttachPrepare(t.Context(), options, bootstrap); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("RunPeerAttachPrepare() error = %v, want %v", err, ErrUnsafePath)
	}
}

func TestRunPeerAttachPrepareRejectsUnusableCapabilityWindows(t *testing.T) {
	options, bootstrap, _, _ := attachFixture(t)

	expired := bootstrap
	expired.Capability.ExpiresAt = options.Now().Add(-time.Second)

	if _, err := RunPeerAttachPrepare(t.Context(), options, expired); !errors.Is(err, ErrCapabilityExpired) {
		t.Fatalf("RunPeerAttachPrepare(expired) error = %v, want %v", err, ErrCapabilityExpired)
	}

	unbounded := bootstrap
	unbounded.Capability.ExpiresAt = options.Now().Add(48 * time.Hour)

	if _, err := RunPeerAttachPrepare(t.Context(), options, unbounded); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("RunPeerAttachPrepare(unbounded) error = %v, want %v", err, ErrInvalidFrame)
	}
}

func TestRunPeerAttachPrepareValidatesTargetIncarnation(t *testing.T) {
	options, bootstrap, _, _ := attachFixture(t)

	bootstrap.Target.SessionCreatedAt = time.Unix(300, 0).UTC()

	if _, err := RunPeerAttachPrepare(t.Context(), options, bootstrap); !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("RunPeerAttachPrepare(replaced) error = %v, want %v", err, ErrTargetChanged)
	}

	absent := bootstrap
	absent.Target = attachTargetFor(defaultSessionTarget())
	absent.Target.SessionName = "gone"

	if _, err := RunPeerAttachPrepare(t.Context(), options, absent); !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("RunPeerAttachPrepare(absent) error = %v, want %v", err, ErrTargetChanged)
	}
}

func TestRunPeerAttachPrepareValidatesPaneOwnership(t *testing.T) {
	options, bootstrap, live, _ := attachFixture(t)

	bootstrap.Target.PaneID = "%7"

	if _, err := RunPeerAttachPrepare(t.Context(), options, bootstrap); !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("RunPeerAttachPrepare(unattributed pane) error = %v, want %v", err, ErrTargetChanged)
	}

	live.paneOwners["%7"] = "$1"
	options.PaneOwner = live

	if _, err := RunPeerAttachPrepare(t.Context(), options, bootstrap); err != nil {
		t.Fatalf("RunPeerAttachPrepare(owned pane) error = %v", err)
	}

	live.paneOwners["%7"] = "$2"

	if _, err := RunPeerAttachPrepare(t.Context(), options, bootstrap); !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("RunPeerAttachPrepare(foreign pane) error = %v, want %v", err, ErrTargetChanged)
	}
}

func TestRunPeerAttachConsumesAndRevokesCapability(t *testing.T) {
	options, bootstrap, live, bridgePath := attachFixture(t)

	if _, err := RunPeerAttachPrepare(t.Context(), options, bootstrap); err != nil {
		t.Fatal(err)
	}

	streams := AttachStreams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}

	if err := RunPeerAttach(t.Context(), options, bootstrap.Capability.AttachmentID, streams); err != nil {
		t.Fatalf("RunPeerAttach() error = %v", err)
	}

	live.mu.Lock()
	attached := append([]string(nil), live.attached...)
	live.mu.Unlock()

	if len(attached) != 1 || attached[0] != "$1" {
		t.Fatalf("attached sessions = %#v, want the validated session", attached)
	}

	dir, err := openPrivateDir(bridgePath, false)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = dir.Close() }()

	if _, err := ReadCapability(dir, bootstrap.Capability.AttachmentID, options.Now()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capability survived detach: %v", err)
	}

	if err := RunPeerAttach(t.Context(), options, bootstrap.Capability.AttachmentID, streams); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("RunPeerAttach(revoked) error = %v, want not-exist", err)
	}
}

func TestRunPeerAttachRejectsUnusableCapabilities(t *testing.T) {
	options, bootstrap, _, _ := attachFixture(t)

	streams := AttachStreams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}

	if err := RunPeerAttach(t.Context(), options, "short", streams); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("RunPeerAttach(invalid ID) error = %v, want %v", err, ErrInvalidFrame)
	}

	if err := RunPeerAttach(t.Context(), options, bootstrap.Capability.AttachmentID, streams); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("RunPeerAttach(missing record) error = %v, want not-exist", err)
	}

	if _, err := RunPeerAttachPrepare(t.Context(), options, bootstrap); err != nil {
		t.Fatal(err)
	}

	expired := options
	expired.Now = func() time.Time { return bootstrap.Capability.ExpiresAt.Add(time.Second) }

	if err := RunPeerAttach(t.Context(), expired, bootstrap.Capability.AttachmentID, streams); !errors.Is(err, ErrCapabilityExpired) {
		t.Fatalf("RunPeerAttach(expired) error = %v, want %v", err, ErrCapabilityExpired)
	}

	other := options
	other.PeerID = "other"

	if err := RunPeerAttach(t.Context(), other, bootstrap.Capability.AttachmentID, streams); !errors.Is(err, ErrPeerMismatch) {
		t.Fatalf("RunPeerAttach(peer mismatch) error = %v, want %v", err, ErrPeerMismatch)
	}
}

func TestRunPeerAttachRequiresTerminalStreams(t *testing.T) {
	options, bootstrap, _, _ := attachFixture(t)

	if _, err := RunPeerAttachPrepare(t.Context(), options, bootstrap); err != nil {
		t.Fatal(err)
	}

	if err := RunPeerAttach(t.Context(), options, bootstrap.Capability.AttachmentID, AttachStreams{}); !errors.Is(err, errNilAttachStreams) {
		t.Fatalf("RunPeerAttach(no streams) error = %v, want %v", err, errNilAttachStreams)
	}
}

func TestRunPeerAttachRequiresLiveServer(t *testing.T) {
	options, bootstrap, _, _ := attachFixture(t)
	options.Live = nil

	if _, err := RunPeerAttachPrepare(t.Context(), options, bootstrap); !errors.Is(err, ErrIncompleteDependencies) {
		t.Fatalf("RunPeerAttachPrepare() error = %v, want %v", err, ErrIncompleteDependencies)
	}
}

func TestCapabilityReaderOpensThePeerBridgeDirectory(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")

	dir, err := CapabilityReader(stateHome)
	if err != nil {
		t.Fatalf("CapabilityReader() error = %v", err)
	}

	defer func() { _ = dir.Close() }()

	if _, err := dir.Names(); err != nil {
		t.Fatalf("reading capability directory: %v", err)
	}
}

func TestPrepareAttachmentKeepsSecretsOutOfArguments(t *testing.T) {
	options, bootstrap, live, bridgePath := attachFixture(t)
	_ = options
	_ = live
	_ = bridgePath

	remote := testRemoteConfig()

	prepare, err := NewAttachPrepareCommand(t.Context(), remote, remote.Connections[0], bootstrap, AttachCommandOptions{})
	if err != nil {
		t.Fatalf("NewAttachPrepareCommand() error = %v", err)
	}

	attach, err := NewAttachCommand(t.Context(), remote, remote.Connections[0], bootstrap, AttachCommandOptions{
		Streams: AttachStreams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr},
	})
	if err != nil {
		t.Fatalf("NewAttachCommand() error = %v", err)
	}

	for _, command := range []*exec.Cmd{prepare, attach} {
		for _, argument := range command.Args {
			if strings.Contains(argument, bootstrap.Capability.Token) {
				t.Fatalf("bridge capability reached argv: %q", argument)
			}

			if strings.Contains(argument, "token") || strings.Contains(argument, "origin-token") {
				t.Fatalf("capability flag reached argv: %q", argument)
			}
		}

		for _, entry := range command.Env {
			if strings.Contains(entry, bootstrap.Capability.Token) {
				t.Fatalf("bridge capability reached the environment: %q", entry)
			}
		}
	}

	if !strings.Contains(strings.Join(attach.Args, " "), "'"+bootstrap.Capability.AttachmentID+"'") {
		t.Fatalf("attach argv = %#v, want the opaque attachment selector", attach.Args)
	}

	if !strings.Contains(strings.Join(attach.Args, " "), "-R "+bootstrap.BridgeSocket+":"+bootstrap.Capability.OriginSocket) {
		t.Fatalf("attach argv = %#v, want the scoped reverse forward", attach.Args)
	}
}
