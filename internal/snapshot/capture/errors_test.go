package capture

import (
	"errors"
	"testing"

	"github.com/samber/oops"
)

func TestSelectionErrorNeedsNoServerEffects(t *testing.T) {
	_, err := Observe(t.Context(), nil, nil)
	if !errors.Is(err, ErrSelection) {
		t.Fatal("lost explicit-selection error")
	}

	diagnostic, ok := oops.AsOops(err)
	if !ok || diagnostic.Code() != "selection_required" || diagnostic.Domain() != "snapshot.capture" {
		t.Fatal("missing capture diagnostics")
	}

	if diagnostic.Context()["operation"] != "observe" {
		t.Fatal("missing capture operation")
	}
}
