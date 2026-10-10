//go:build linux

package tmuxserver

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	fakeServerSocket = "/tmp/tmux-1000/default"
	fakeServerPID    = "4321"
	fakeServerBinary = "/opt/tmux/bin/tmux.bin"
)

type fakeProc struct {
	root string
}

func newFakeProc(t *testing.T, unixRows string) fakeProc {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o700); err != nil {
		t.Fatal(err)
	}

	table := "Num       RefCount Protocol Flags    Type St Inode Path\n" + unixRows
	if err := os.WriteFile(filepath.Join(root, "net", "unix"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}

	return fakeProc{root: root}
}

func (p fakeProc) process(t *testing.T, pid, executable string, descriptors ...string) {
	t.Helper()

	fd := filepath.Join(p.root, pid, "fd")
	if err := os.MkdirAll(fd, 0o700); err != nil {
		t.Fatal(err)
	}

	for index, target := range descriptors {
		if err := os.Symlink(target, filepath.Join(fd, string(rune('3'+index)))); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Symlink(executable, filepath.Join(p.root, pid, "exe")); err != nil {
		t.Fatal(err)
	}
}

func listeningRow(inode, path string) string {
	return "0000000000000000: 00000002 00000000 00010000 0001 01 " + inode + " " + path + "\n"
}

func TestServerBinaryUsesExecutableOfServerListeningOnSocket(t *testing.T) {
	proc := newFakeProc(t, listeningRow("9001", "/run/other.sock")+listeningRow("4242", fakeServerSocket))
	proc.process(t, fakeServerPID, fakeServerBinary, "pipe:[1]", "socket:[4242]")

	got := resolveServerBinary(fakeServerSocket+","+fakeServerPID+",0", fakeServerSocket, os.DirFS(proc.root))
	if got != fakeServerBinary {
		t.Fatalf("server binary = %q, want %q", got, fakeServerBinary)
	}
}

func TestServerBinaryFallsBackToPathWithoutVerifiedServer(t *testing.T) {
	for _, test := range []struct {
		name       string
		env        string
		rows       string
		executable string
		descriptor string
	}{
		{"outside tmux", "", listeningRow("4242", fakeServerSocket), fakeServerBinary, "socket:[4242]"},
		{"malformed environment", fakeServerSocket + "," + fakeServerPID, listeningRow("4242", fakeServerSocket), fakeServerBinary, "socket:[4242]"},
		{"other server socket", "/tmp/tmux-1000/other," + fakeServerPID + ",0", listeningRow("4242", fakeServerSocket), fakeServerBinary, "socket:[4242]"},
		{"stale pid", fakeServerSocket + "," + fakeServerPID + ",0", listeningRow("4242", fakeServerSocket), "/usr/bin/rm", "socket:[7]"},
		{"socket not listening", fakeServerSocket + "," + fakeServerPID + ",0", "0000000000000000: 00000002 00000000 00000000 0001 03 4242 " + fakeServerSocket + "\n", fakeServerBinary, "socket:[4242]"},
		{"replaced executable", fakeServerSocket + "," + fakeServerPID + ",0", listeningRow("4242", fakeServerSocket), fakeServerBinary + " (deleted)", "socket:[4242]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			proc := newFakeProc(t, test.rows)
			proc.process(t, fakeServerPID, test.executable, test.descriptor)

			if got := resolveServerBinary(test.env, fakeServerSocket, os.DirFS(proc.root)); got != "" {
				t.Fatalf("server binary = %q, want PATH fallback", got)
			}
		})
	}
}
