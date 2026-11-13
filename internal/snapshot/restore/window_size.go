//go:build linux || darwin

package restore

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

// ReleaseWindowSizes returns created windows to the server's window-size policy. Restore resizes
// each new window to its saved size, and resize-window pins it to window-size manual, which keeps
// the window at that size when the client is resized. A window that already left manual, such as
// one whose restored options set a policy, is left as it is.
func ReleaseWindowSizes(ctx context.Context, server *gotmux.Server, windowIDs []string) error {
	var failures []error

	for _, id := range windowIDs {
		if err := releaseWindowSize(ctx, server, gotmux.WindowID(id)); err != nil {
			failures = append(failures, err)
		}
	}

	if err := errors.Join(failures...); err != nil {
		return oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "release_window_sizes").
			Wrap(err)
	}

	return nil
}

func releaseWindowSize(ctx context.Context, server *gotmux.Server, id gotmux.WindowID) error {
	window, err := server.Window(ctx, id)
	if err != nil {
		return fmt.Errorf("window %s: %w", id, err)
	}

	options := window.Options()

	size, err := options.WindowSize(ctx)
	if err != nil {
		return fmt.Errorf("read window-size of window %s: %w", id, err)
	}

	if local, ok := size.Local.Get(); !ok || local != gotmux.WindowSizeManual {
		return nil
	}

	if err := options.UnsetWindowSize(ctx); err != nil {
		return fmt.Errorf("unset window-size of window %s: %w", id, err)
	}

	return nil
}
