//go:build (linux || darwin) && integration

package engine

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
)

func TestUnrestoredSessionInspectionWarnsWhenSelectedServerCannotBeProbed(t *testing.T) {
	r := testRepository(t)

	server, err := tmuxserver.Open(filepath.Join(t.TempDir(), "PRIVATE_SOCKET"))
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer

	ctx := zerolog.New(&logs).WithContext(t.Context())
	if found := FindUnrestored(ctx, server, r); len(found.Sessions) != 0 {
		t.Fatal("unavailable server produced sessions")
	}

	assertRecoveryInspectionRecord(t, logs.Bytes(), "find_unrestored", "probe")
}
