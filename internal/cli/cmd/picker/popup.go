//go:build linux || darwin

package picker

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/session"
)

// popupFrontend opens the picker popup for the command that invoked pick.popup; the popup child
// re-runs sesh with the same configuration and state flags.
type popupFrontend struct {
	cmd *cobra.Command
}

func newPickPopupCommand() *cobra.Command {
	return actioncli.Command(action.PickPopup, actioncli.Options[action.OpenPicker, action.ClosedPicker]{
		Operation:    "picker",
		SuccessOwned: false,
		Configure: func(cmd *cobra.Command) {
			cmd.Annotations[clibase.AnnotationLiveIO] = annotationEnabled
		},
		Prepare:  nil,
		Frontend: func(cmd *cobra.Command) action.Dispatcher { return popupFrontend{cmd: cmd} },
		Render:   nil,
	})
}

func (frontend popupFrontend) Accepts(at action.Location, id action.ID) bool {
	return at.Kind == action.LocationLocal && id == action.PickPopup.ID
}

func (frontend popupFrontend) Dispatch(ctx context.Context, invocation action.Invocation) (any, error) {
	launchedAt := time.Now()

	runtime, err := preparePickerCommand(frontend.cmd)
	if err != nil {
		return nil, err
	}

	if err := openPickerPopup(ctx, frontend.cmd, runtime, invocation.Caller, launchedAt); err != nil {
		return nil, err
	}

	return action.ClosedPicker{}, nil
}

// checkPopupClientSession refuses to open the popup when the client moved to another session
// after the key binding captured it.
func checkPopupClientSession(ctx context.Context, server *gotmux.Server, expected, shown string) error {
	if expected == "" || expected == shown {
		return nil
	}

	if strings.HasPrefix(expected, "$") {
		return session.ErrInvokingClientUnavailable
	}

	rows, err := session.ListLive(ctx, server, "")
	if err != nil {
		return oops.In("cli").With("operation", "resolve_popup_session").Wrap(err)
	}

	if !slices.ContainsFunc(rows, func(row session.Session) bool { return row.ID == shown && row.Name == expected }) {
		return session.ErrInvokingClientUnavailable
	}

	return nil
}
