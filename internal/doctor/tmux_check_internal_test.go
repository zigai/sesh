//go:build linux || darwin

package doctor

import (
	"context"
	"fmt"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
)

func TestOnlyAMissingServerCountsAsNotRunning(t *testing.T) {
	t.Parallel()

	service := NewService(Options{Socket: "/tmp/tmux-1000/default"})

	for err, want := range map[error]string{
		fmt.Errorf("probe: %w", gotmux.ErrNoServer):       ReasonNotRunning,
		fmt.Errorf("probe: %w", context.DeadlineExceeded): ReasonTimedOut,
		fmt.Errorf("probe: %w", context.Canceled):         ReasonCanceled,
	} {
		result := service.classifyTmuxProbeError(err)
		if result.Status != StatusFail || result.Reason != want || result.Fix == nil {
			t.Errorf("%v classified as %+v, want a %s failure with a fix", err, result, want)
		}
	}
}
