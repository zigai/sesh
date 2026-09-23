//go:build linux || darwin

package cmdpicker

import (
	"errors"
	"os"
	"testing"

	"github.com/zigai/sesh/internal/session"
)

func TestPickerSwitchHandoffCarriesSelectionAndTreatsEmptyFileAsCancel(t *testing.T) {
	path, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("handoff permissions: %v, %v", info, err)
	}

	if _, selected, err := readPickerSwitchHandoff(path); err != nil || selected {
		t.Fatalf("empty handoff = %t, %v", selected, err)
	}

	target := session.Session{ID: "$8", Name: "agent-target", Kind: session.KindLive, Origin: session.OriginLocal}
	if err := writePickerSwitchHandoff(path, target, "%4"); err != nil {
		t.Fatal(err)
	}

	handoff, selected, err := readPickerSwitchHandoff(path)
	if err != nil || !selected || handoff.SessionID != target.ID || handoff.SessionName != target.Name || handoff.PaneID != "%4" {
		t.Fatalf("handoff = %+v, selected %t, error %v", handoff, selected, err)
	}
}

func TestPickerSwitchHandoffRejectsInvalidSelection(t *testing.T) {
	path, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if err := writePickerSwitchHandoff(path, session.Session{Name: "missing-id"}, ""); !errors.Is(err, errPickerHandoffInvalid) {
		t.Fatalf("invalid selection error = %v", err)
	}

	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := readPickerSwitchHandoff(path); !errors.Is(err, errPickerHandoffInvalid) {
		t.Fatalf("invalid payload error = %v", err)
	}
}
