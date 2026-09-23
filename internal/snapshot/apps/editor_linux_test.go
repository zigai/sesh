//go:build linux

package apps

import (
	"bytes"
	"testing"
)

func TestNvimEmbeddedCoreRequiresNativeEmbedArgument(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments [][]byte
		want      bool
	}{
		{name: "native UI core", arguments: [][]byte{[]byte("nvim"), []byte("--embed"), []byte("--clean")}, want: true},
		{name: "nested editor", arguments: [][]byte{[]byte("nvim"), []byte("--clean")}, want: false},
		{name: "embed option is not native core position", arguments: [][]byte{[]byte("nvim"), []byte("--clean"), []byte("--embed")}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isNvimEmbeddedCore(test.arguments); got != test.want {
				t.Fatalf("isNvimEmbeddedCore(%q) = %t, want %t", bytes.Join(test.arguments, []byte(" ")), got, test.want)
			}
		})
	}
}

func TestUnixSocketLinePreservesExactSocketPath(t *testing.T) {
	t.Parallel()

	line := "0000000000000000: 00000002 00000000 00010000 0001 01 424242 /tmp/nvim socket.1"

	inode, path, ok := unixSocketLine(line)
	if !ok || inode != "424242" || path != "/tmp/nvim socket.1" {
		t.Fatalf("unixSocketLine() = %q, %q, %t", inode, path, ok)
	}
}

func TestUnixSocketLineRejectsMissingPath(t *testing.T) {
	t.Parallel()

	if _, _, ok := unixSocketLine("0000000000000000: 00000002 00000000 00010000 0001 01 424242"); ok {
		t.Fatal("unixSocketLine() accepted a record without a socket path")
	}
}
