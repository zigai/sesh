package tmuxserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/samber/oops"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/process"
)

func TestDaemonIdentityEqualIncludesNativeBirth(t *testing.T) {
	server := gotmux.ServerIdentity{
		Endpoint:       gotmux.Endpoint{SocketPath: "/tmp/daemon.sock"},
		ReportedSocket: "/tmp/daemon.sock",
		PID:            42,
		Started:        time.Unix(100, 0),
		Generation:     0,
	}
	first := DaemonIdentity{
		Server: server,
		Birth:  process.Identity{BootID: "11111111-2222-3333-4444-555555555555", StartTime: 1000},
	}
	second := first

	if !first.Equal(second) {
		t.Fatal("identical daemon identities were not equal")
	}

	second.Birth.StartTime++
	if first.Equal(second) {
		t.Fatal("different native births were equal")
	}
}

func TestObserveDaemonIdentityCancellationIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ObserveDaemonIdentity(ctx, nil)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrDaemonIdentityUnavailable) {
		t.Fatalf("ObserveDaemonIdentity error = %v, want cancellation and unavailable identity", err)
	}

	oopsErr, ok := oops.AsOops(err)
	if !ok {
		t.Fatal("expected structured error")
	}

	if oopsErr.Code() != CodeDaemonIdentityUnavailable {
		t.Fatalf("error code = %q, want %q", oopsErr.Code(), CodeDaemonIdentityUnavailable)
	}
}

func TestValidDaemonServerIdentityRequiresSelectedSocket(t *testing.T) {
	endpoint := gotmux.Endpoint{SocketPath: "/tmp/daemon.sock"}
	valid := gotmux.ServerIdentity{
		Endpoint:       endpoint,
		ReportedSocket: endpoint.SocketPath,
		PID:            42,
		Started:        time.Unix(100, 0),
		Generation:     0,
	}

	if !validDaemonServerIdentity(valid, endpoint) {
		t.Fatal("valid identity was rejected")
	}

	valid.ReportedSocket = "/tmp/other.sock"
	if validDaemonServerIdentity(valid, endpoint) {
		t.Fatal("identity reported by another socket was accepted")
	}
}
