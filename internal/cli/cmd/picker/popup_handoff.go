//go:build linux || darwin

package picker

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/tui"
)

const (
	handoffMaxBytes      = 256 * 1024
	handoffMaxAgentViews = 256
)

var errPickerHandoffInvalid = errors.New("invalid popup handoff")

// createPopupHandoff makes the file a popup's program writes its choice to.
func createPopupHandoff() (string, func(), error) {
	file, err := os.CreateTemp(os.Getenv("XDG_RUNTIME_DIR"), "sesh-picker-*.handoff")
	if err != nil {
		return "", nil, oops.In("cli").With("operation", "create_picker_handoff").Wrap(err)
	}

	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, oops.In("cli").With("operation", "create_picker_handoff").Wrap(err)
	}

	return path, func() { _ = os.Remove(path) }, nil
}

func replayPickerKey(ctx context.Context, key string, socketPath string, clientName string) error {
	if key == "" {
		return nil
	}

	if key != tui.ReplayKeyFloatingTerminal || clientName == "" {
		return oops.In("cli").With("operation", "replay_picker_key").Wrap(errPickerHandoffInvalid)
	}

	args := []string{"-S", socketPath, "send-keys", "-K", "-c", clientName, key}
	if output, err := exec.CommandContext(ctx, "tmux", args...).CombinedOutput(); err != nil {
		return oops.In("cli").
			With("operation", "replay_picker_key").
			With("output", string(output)).
			Wrap(err)
	}

	return nil
}
