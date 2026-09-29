package capture

import (
	"errors"
	"fmt"
	"testing"

	"github.com/samber/oops"
	"github.com/zigai/gotmux/tmux"
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

func TestOutputLimitErrorHasStableCode(t *testing.T) {
	err := captureError(fmt.Errorf("list options: %w", tmux.ErrOutputLimit))
	if !errors.Is(err, tmux.ErrOutputLimit) {
		t.Fatal("lost output limit identity")
	}

	diagnostic, ok := oops.AsOops(err)
	if !ok || diagnostic.Code() != "output_limit_exceeded" || diagnostic.Domain() != "snapshot.capture" {
		t.Fatal("output limit was not classified")
	}
}
