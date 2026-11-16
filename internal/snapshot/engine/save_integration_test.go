//go:build (linux || darwin) && integration

package engine

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
)

func TestSaveObservationAdmissionValidation(t *testing.T) {
	r := testRepository(t)

	_, err := SaveObservation(t.Context(), r, nil, nil, SaveConfig{LeaseRoot: ""})
	if !errors.Is(err, ErrLeaseRootRequired) {
		t.Fatalf("expected ErrLeaseRootRequired, got %v", err)
	}

	_, err = SaveObservation(
		t.Context(),
		r,
		nil,
		nil,
		SaveConfig{LeaseRoot: filepath.Join(t.TempDir(), "leases")},
	)
	if err == nil {
		t.Fatal("expected error for nil server")
	}

	server, err := tmuxserver.Open(filepath.Join(t.TempDir(), "absent.sock"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = SaveObservation(
		t.Context(),
		nil,
		server,
		nil,
		SaveConfig{LeaseRoot: filepath.Join(t.TempDir(), "leases")},
	)
	if !errors.Is(err, ErrRepositoryRequired) {
		t.Fatalf("expected ErrRepositoryRequired, got %v", err)
	}
}
