//go:build linux && integration

package tmux

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/zigai/gotmux/tmuxtest"
)

func TestOpenRunsTheExecutableOfTheServerNamedByTMUX(t *testing.T) {
	server := tmuxtest.NewServer(t)
	socket := server.Endpoint().SocketPath

	probe, err := server.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	pid := strconv.Itoa(probe.Identity.PID)

	want, err := os.Readlink(filepath.Join("/proc", pid, "exe"))
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("TMUX", socket+","+pid+",0")

	if got := serverBinary(socket); got != want {
		t.Fatalf("server binary = %q, want the running server's executable %q", got, want)
	}

	opened, err := Open(socket)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := opened.Probe(t.Context())
	if err != nil || reopened.Identity.PID != probe.Identity.PID {
		t.Fatalf("probe through the server's executable = %+v, %v", reopened.Identity, err)
	}
}
