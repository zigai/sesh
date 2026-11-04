//go:build linux || darwin

package picker

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/setup"
	"github.com/zigai/sesh/internal/tui"
)

type popupRequest struct {
	socket string
	client string
	pane   string
	mode   string
}

type popupOutcome struct {
	target   session.Session
	selected bool
	pane     string
	views    []tui.AgentView
	heights  []session.PopupHeight
	refresh  bool
	replay   string
	project  string
}

func (outcome popupOutcome) empty() bool {
	return !outcome.selected && len(outcome.views) == 0 && !outcome.refresh &&
		outcome.replay == "" && outcome.project == ""
}

// finishPopupPicker closes the popup before doing what the choice asks for. Disk writes then cannot
// hold it on screen, and tmux can focus a pane and accept keys, which it refuses or undoes while a
// popup is up. Closing the popup signals this process, which outlives it to finish the work.
func finishPopupPicker(
	ctx context.Context,
	cmd *cobra.Command,
	finalModel *tui.Model,
	services *services,
	workspace app.Workspace,
	state *session.State,
	startMode string,
	remoteFactory RemoteFactory,
) error {
	target, selected := finalModel.DeferredSwitchTarget()
	if selected && (target.ID == "" || target.Name == "" || target.Kind != session.KindLive ||
		target.Origin != session.OriginLocal) {
		return errors.Join(
			oops.In("cli").With("operation", "switch_picker_session").Wrap(errPickerHandoffInvalid),
			services.Close(),
		)
	}

	var deferredErr error
	if !selected {
		deferredErr = runPickerDeferred(
			ctx,
			cmd.InOrStdin(),
			cmd.OutOrStdout(),
			cmd.ErrOrStderr(),
			finalModel,
			services,
		)
	}

	heights := changedPopupHeights(state, finalModel.FittingPopupHeights())
	outcome := popupOutcome{
		target:   target,
		selected: selected,
		pane:     finalModel.DeferredSwitchPane(),
		views:    services.takeDeferredAgentViews(),
		heights:  heights,
		refresh:  len(heights) > 0 || state.PopupDisplayMode != startMode,
		replay:   finalModel.ReplayKey(),
		project:  finalModel.TaskProject(),
	}

	if outcome.empty() {
		return errors.Join(deferredErr, services.Close())
	}

	return errors.Join(
		deferredErr,
		completePopup(ctx, cmd, services, workspace, outcome, remoteFactory),
	)
}

func completePopup(
	ctx context.Context,
	cmd *cobra.Command,
	services *services,
	workspace app.Workspace,
	outcome popupOutcome,
	remoteFactory RemoteFactory,
) error {
	request := popupRequest{
		socket: workspace.Socket,
		client: os.Getenv(clientEnvironment),
		pane:   services.currentPane,
		mode:   popupModeFlag(cmd),
	}

	signal.Ignore(syscall.SIGHUP, syscall.SIGTERM)

	client, err := popupClient(ctx, services.server, request.client)
	if err != nil {
		return errors.Join(err, services.Close())
	}

	closeErr := remoteCLIError("close_picker_popup", client.Handle().ClosePopup(ctx))

	var enterErr error
	if outcome.selected {
		enterErr = services.performSwitchValidated(
			ctx,
			outcome.target,
			outcome.pane,
			request.client,
			services.currentSessionID,
			outcome.views,
		)
	} else {
		enterErr = services.recordPickerHistory(ctx, "", outcome.views)
	}

	services.recordPopupHeights(ctx, outcome.heights)

	var refreshErr error
	if outcome.refresh {
		refreshErr = refreshPopupCommand(ctx, cmd, services.server, workspace, request.mode, client)
	}

	return errors.Join(
		closeErr,
		enterErr,
		refreshErr,
		services.Close(),
		replayPickerKey(ctx, outcome.replay, request.socket, request.client),
		openHandoffTaskMenu(ctx, cmd, workspace.Config, outcome.project, request, remoteFactory),
	)
}

func popupModeFlag(cmd *cobra.Command) string {
	mode, _ := cmd.Flags().GetString(modeFlag)

	return mode
}

func popupClient(
	ctx context.Context,
	server *gotmux.Server,
	name string,
) (gotmux.ClientInfo, error) {
	clients, err := server.Clients(ctx)
	if err != nil {
		return gotmux.ClientInfo{}, remoteCLIError("list_picker_clients", err)
	}

	index := slices.IndexFunc(
		clients,
		func(client gotmux.ClientInfo) bool { return string(client.Name) == name },
	)
	if index < 0 {
		return gotmux.ClientInfo{}, session.ErrInvokingClientUnavailable
	}

	return clients[index], nil
}

func changedPopupHeights(
	state *session.State,
	heights []session.PopupHeight,
) []session.PopupHeight {
	var changed []session.PopupHeight

	for _, fit := range heights {
		if state.PopupHeight(fit.Mode) != fit.Height {
			changed = append(changed, fit)
		}
	}

	return changed
}

func refreshPopupCommand(
	ctx context.Context,
	cmd *cobra.Command,
	server *gotmux.Server,
	workspace app.Workspace,
	mode string,
	client gotmux.ClientInfo,
) error {
	state, err := session.LoadState(workspace.History)
	if err != nil {
		return oops.In("cli").With("operation", "load_history").Wrap(err)
	}

	executable, err := os.Executable()
	if err != nil {
		return oops.In("cli").With("operation", "resolve_executable").Wrap(err)
	}

	return savePopupCommand(ctx, cmd, server, workspace, state, mode, executable, client)
}

func openHandoffTaskMenu(
	ctx context.Context,
	cmd *cobra.Command,
	cfg *config.Config,
	project string,
	request popupRequest,
	remoteFactory RemoteFactory,
) error {
	if project == "" {
		return nil
	}

	runtime, err := actioncli.OpenRuntime(cmd, cfg)
	if err != nil {
		return err
	}

	return errors.Join(
		openTaskPicker(
			ctx,
			cmd,
			cfg,
			runtime,
			action.Caller{
				Client:  request.client,
				Pane:    request.pane,
				Session: "",
				Width:   "",
				Height:  "",
			},
			remoteFactory,
			project,
		),
		runtime.Close(),
	)
}

// savePopupCommand stores the tmux command the key binding runs, sized for client. The formats in it
// are expanded when the key is pressed, so it stays correct for any pane and session.
func savePopupCommand(
	ctx context.Context,
	cmd *cobra.Command,
	server *gotmux.Server,
	workspace app.Workspace,
	state *session.State,
	mode string,
	executable string,
	client gotmux.ClientInfo,
) error {
	width, height := popupDimensions(
		workspace.Config,
		state,
		gotmux.Size{Width: client.Width, Height: client.Height},
	)
	command := popupCommand(cmd, workspace, mode, executable, width, height)
	size := strconv.Itoa(client.Width) + "x" + strconv.Itoa(client.Height)
	options := server.Options()

	if err := options.SetUser(ctx, setup.PopupCommandOption(mode), command); err != nil {
		return remoteCLIError("save_popup_command", err)
	}

	if err := options.SetUser(ctx, setup.PopupSizeOption(mode), size); err != nil {
		return remoteCLIError("save_popup_command", err)
	}

	return nil
}

func popupCommand(
	cmd *cobra.Command,
	workspace app.Workspace,
	mode string,
	executable string,
	width string,
	height string,
) string {
	display := []string{
		"display-popup",
		"-B",
		"-E",
		"-x",
		"C",
		"-y",
		"C",
		"-w",
		width,
		"-h",
		height,
	}

	style := popupOptions(gotmux.Program{}, width, height, workspace.Config.Theme.Background).Style
	if style != "" {
		display = append(display, "-s", style)
	}

	display = append(display, "-e", clientEnvironment+"=#{client_name}", "--", executable)
	display = append(
		display,
		popupArguments(cmd, "#{socket_path}", "#{session_id}", "#{pane_id}", mode, time.Time{})...,
	)

	quoted := make([]string, 0, len(display))
	for _, word := range display {
		quoted = append(quoted, tmuxWord(word))
	}

	return strings.Join(quoted, " ")
}

func tmuxWord(word string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(word) + `"`
}
