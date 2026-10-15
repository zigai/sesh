//go:build linux || darwin

package app

import (
	"errors"
	"fmt"
	"testing"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/project"
	"github.com/zigai/sesh/internal/snapshot/capture"
	"github.com/zigai/sesh/internal/snapshot/store"
	"github.com/zigai/sesh/internal/task"
)

func TestCaptureSessionNotFoundClassification(t *testing.T) {
	t.Parallel()

	missing := &capture.SessionNotFoundError{}
	for _, test := range []struct {
		name string
		err  error
		want action.Code
	}{
		{"missing session", missing, action.CodeNotFound},
		{"wrapped missing session", fmt.Errorf("capture: %w", missing), action.CodeNotFound},
		{"uncertain takes precedence", errors.Join(missing, store.ErrUncertain), action.CodeUncertain},
		{"classified takes precedence", action.Wrap(action.CodeTargetChanged, missing), action.CodeTargetChanged},
		{"empty selection", capture.ErrSelection, action.CodeInvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classify(test.err); got != test.want {
				t.Fatalf("classification = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTaskFailuresKeepLedgerUncertainty(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
		want action.Code
	}{
		{"failed start with an uncertain failure record", errors.Join(task.ErrNoBaseBranch, task.ErrUncertain), action.CodeUncertain},
		{"name held by another session", fmt.Errorf("open: %w", project.ErrNameTaken), action.CodeAlreadyExists},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classify(test.err); got != test.want {
				t.Fatalf("classification = %q, want %q", got, test.want)
			}
		})
	}
}
