//go:build linux || darwin

package picker

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/tui/taskpicker"
)

const (
	taskPickerUIVerb = "pick-ui"
	taskPaneFlag     = "pane"
)

// taskHandoff names the task the task picker popup hands to its opener to go to.
type taskHandoff struct {
	Show string `json:"show"`
}

// taskFrontend opens the task picker popup for task.menu. The popup starts the task or opens the
// tab itself; the opener then moves the client to a task the popup names, which a popup cannot do
// for its own client.
type taskFrontend struct {
	cmd *cobra.Command
}

// NewTaskMenuCommand is `sesh task menu`.
func NewTaskMenuCommand() *cobra.Command {
	return actioncli.Command(action.TaskMenu, actioncli.Options[action.OpenTaskMenu, action.OpenedTaskMenu]{
		Operation:    "task_picker",
		SuccessOwned: false,
		Configure: func(cmd *cobra.Command) {
			cmd.Annotations[clibase.AnnotationLiveIO] = annotationEnabled
		},
		Prepare:  nil,
		Frontend: func(cmd *cobra.Command) action.Dispatcher { return taskFrontend{cmd: cmd} },
		Render:   nil,
	})
}

// NewTaskPickerUICommand is the hidden command the task picker popup runs.
func NewTaskPickerUICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    taskPickerUIVerb,
		Short:  "Run the task picker inside its popup",
		Hidden: true,
		Args:   clibase.UsageArgs(cobra.NoArgs),
		RunE:   runTaskPickerUI,
		Annotations: map[string]string{
			clibase.AnnotationOperation: "task_picker", clibase.AnnotationLiveIO: annotationEnabled,
		},
	}

	actioncli.AddEnvironmentFlags(cmd)
	cmd.Flags().String(projectHandoffFlag, "", "File the choice is written to")
	cmd.Flags().String(taskPaneFlag, "", "Pane the task is started from")

	return cmd
}

func (frontend taskFrontend) Accepts(at action.Location, id action.ID) bool {
	return at.Kind == action.LocationLocal && id == action.TaskMenu.ID
}

func (frontend taskFrontend) Dispatch(ctx context.Context, invocation action.Invocation) (any, error) {
	cmd := frontend.cmd

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return action.OpenedTaskMenu{}, err
	}

	runtime, err := actioncli.OpenRuntime(cmd, cfg)
	if err != nil {
		return action.OpenedTaskMenu{}, err
	}

	err = openTaskPicker(ctx, cmd, cfg, runtime, invocation.Caller)

	return action.OpenedTaskMenu{}, errors.Join(err, runtime.Close())
}

// openTaskPicker opens the popup sized to what it offers, then goes to the task it names.
func openTaskPicker(ctx context.Context, cmd *cobra.Command, cfg *config.Config, runtime *actioncli.Runtime, caller action.Caller) error {
	workspace, err := app.Resolve(cfg, clibase.CommandLocations(cmd))
	if err != nil {
		return err
	}

	server, err := tmuxserver.OpenInteractive(workspace.Socket)
	if err != nil {
		return oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	client, err := resolvePopupClient(ctx, caller, server)
	if err != nil {
		return oops.In("cli").With("operation", "resolve_popup_client").Wrap(err)
	}

	offered, err := runtime.Local.TaskOffer(ctx, caller)
	if err != nil {
		return err
	}

	executable, err := os.Executable()
	if err != nil {
		return oops.In("cli").With("operation", "resolve_executable").Wrap(err)
	}

	handoffPath, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		return err
	}
	defer cleanup()

	arguments := slices.Concat([]string{
		"task", taskPickerUIVerb, "--socket", workspace.Socket, "--" + projectHandoffFlag, handoffPath,
		"--" + taskPaneFlag, caller.Pane,
	}, forwardedConfigFlags(cmd))

	width, height := taskpicker.Size(cfg, taskPickerOptions(offered, nil))
	width, height = min(width, client.Width), min(height, client.Height)
	options := popupOptions(gotmux.Exec(executable, arguments...), strconv.Itoa(width), strconv.Itoa(height), cfg.Theme.Background)

	if err := client.Handle().Popup(ctx, options); err != nil {
		return oops.In("cli").With("operation", "open_task_picker").Wrap(err)
	}

	handoff, chosen, err := readHandoff(handoffPath, func(handoff taskHandoff) bool { return handoff.Show != "" })
	if err != nil || !chosen {
		return err
	}

	return runtime.Local.ShowTask(ctx, caller, handoff.Show)
}

func runTaskPickerUI(cmd *cobra.Command, _ []string) error {
	ctx := clibase.CommandContext(cmd)

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	handoffPath, _ := cmd.Flags().GetString(projectHandoffFlag)
	pane, _ := cmd.Flags().GetString(taskPaneFlag)

	if handoffPath == "" || pane == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, errPickerHandoffInvalid)
	}

	runtime, err := actioncli.OpenRuntime(cmd, cfg)
	if err != nil {
		return err
	}

	caller := action.Caller{Client: "", Pane: pane, Session: "", Width: "", Height: ""}

	choice, chosen, err := pickTask(ctx, cmd, cfg, runtime, caller)
	if err = errors.Join(err, runtime.Close()); err != nil || !chosen || choice.Show == "" {
		return err
	}

	return writeHandoff(handoffPath, taskHandoff{Show: choice.Show})
}

// pickTask runs the picker, which starts the chosen task or tab through runtime before it closes.
func pickTask(ctx context.Context, cmd *cobra.Command, cfg *config.Config, runtime *actioncli.Runtime, caller action.Caller) (taskpicker.Choice, bool, error) {
	offered, err := runtime.Local.TaskOffer(ctx, caller)
	if err != nil {
		return taskpicker.Choice{}, false, err
	}

	start := func(choice taskpicker.Choice) (string, error) {
		if choice.Tab {
			_, err := action.Run(ctx, runtime.Router, action.TaskTab, action.Local(), caller, action.OpenTab{})

			return "", err
		}

		location := choice.Location
		started, err := action.Run(ctx, runtime.Router, action.TaskNew, action.Local(), caller, action.NewTask{
			Harness: choice.Harness, Where: string(location.Where), Branch: location.Branch, Remote: location.Remote,
			Directory: location.Directory, Title: choice.Title, Project: choice.Project,
		})

		return started.ID, err
	}

	model := taskpicker.NewModel(cfg, taskPickerOptions(offered, start))

	options := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithInput(cmd.InOrStdin()),
		tea.WithOutput(ProgramOutput(cmd.OutOrStdout())),
		tea.WithoutSignalHandler(),
	}
	if output, ok := cmd.OutOrStdout().(*os.File); ok {
		if width, height, sizeErr := term.GetSize(output.Fd()); sizeErr == nil {
			options = append(options, tea.WithWindowSize(width, height))
		}
	}

	if _, err := tea.NewProgram(model, options...).Run(); err != nil {
		return taskpicker.Choice{}, false, oops.In("cli").With("operation", "execute_task_picker").Wrap(err)
	}

	choice, chosen := model.Chosen()

	return choice, chosen, nil
}

func taskPickerOptions(offered app.TaskOffer, start func(taskpicker.Choice) (string, error)) taskpicker.Options {
	names := make([]string, 0, len(offered.Offer.Harnesses))
	for _, harness := range offered.Offer.Harnesses {
		names = append(names, harness.Name)
	}

	home, _ := os.UserHomeDir()

	return taskpicker.Options{
		Project: offered.Project, Dir: offered.Dir, Home: home, Locations: offered.Offer.Locations,
		Location: offered.Offer.Location, Projects: offered.Offer.Projects, Harnesses: names,
		LastHarness: offered.Offer.LastHarness, Start: start,
	}
}
