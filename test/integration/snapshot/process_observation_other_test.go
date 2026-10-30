//go:build integration && !linux && !darwin

package snapshot_test

import (
	"errors"
	"path/filepath"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/engine"
)

func TestProcessObservationUnsupportedOnNonLinux(t *testing.T) {
	server := tmuxtest.NewServer(t)
	dir := t.TempDir()
	session, _ := saveSessionFixture(t, server, "unsupported-non-linux", dir, "NON_LINUX_MARKER")

	_, err := capture.ObserveWithProcessEnrichment(t.Context(), server, []gotmux.SessionID{session.ID()}, []string{"PATH"})
	if err == nil {
		t.Fatal("expected ObserveWithProcessEnrichment to fail on non-Linux")
	}

	if !errors.Is(err, capture.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}

	r := openSaveRepository(t)
	cfg := engine.SaveConfig{
		LeaseRoot:         filepath.Join(t.TempDir(), "leases"),
		ProcessEnrichment: true,
	}

	_, saveErr := engine.SaveObservation(t.Context(), r, server, []gotmux.SessionID{session.ID()}, cfg)
	if saveErr == nil {
		t.Fatal("expected SaveObservation with ProcessEnrichment to fail on non-Linux")
	}

	if !errors.Is(saveErr, capture.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported from SaveObservation, got %v", saveErr)
	}
}
