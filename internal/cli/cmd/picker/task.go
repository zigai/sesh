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
	taskExpandedFlag = "expanded"
	taskTitleFlag    = "title"
	taskAgentFlag    = "agent"
	// centered divides the space around a popup to center it.
	centered = 2
)

// taskHandoff names the task the task picker popup hands to its opener to go to, or asks it to
// reopen the popup, expanded or not, with the title and agent chosen so far.
type taskHandoff struct {
	Show     string `json:"show"`
	Reopen   bool   `json:"reopen"`
	Expanded bool   `json:"expanded"`
	Title    string `json:"title"`
	Agent    string `json:"agent"`
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
	cmd.Flags().Bool(taskExpandedFlag, false, "List every location of the project")
	cmd.Flags().String(taskTitleFlag, "", "Title to start with")
	cmd.Flags().String(taskAgentFlag, "", "Agent to start with")

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

// openTaskPicker opens the popup sized to what it offers, reopens it as often as it asks, then
// goes to the task it names.
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

	handoff := taskHandoff{Show: "", Reopen: false, Expanded: false, Title: "", Agent: ""}

	for {
		next, chosen, err := showTaskPopup(ctx, cmd, cfg, workspace.Socket, client, caller, offered, handoff)
		if err != nil || !chosen {
			return err
		}

		if !next.Reopen {
			return runtime.Local.ShowTask(ctx, caller, next.Show)
		}

		handoff = next
	}
}

// showTaskPopup shows the task picker popup, expanded when from asks for it, and returns what it
// handed back. In a project the popup keeps its top edge whether expanded or not, so expanding
// grows it downwards.
func showTaskPopup(
	ctx context.Context, cmd *cobra.Command, cfg *config.Config, socket string, client gotmux.ClientInfo, caller action.Caller,
	offered app.TaskOffer, from taskHandoff,
) (taskHandoff, bool, error) {
	executable, err := os.Executable()
	if err != nil {
		return taskHandoff{}, false, oops.In("cli").With("operation", "resolve_executable").Wrap(err)
	}

	handoffPath, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		return taskHandoff{}, false, err
	}
	defer cleanup()

	arguments := []string{
		"task", taskPickerUIVerb, "--socket", socket, "--" + projectHandoffFlag, handoffPath, "--" + taskPaneFlag, caller.Pane,
	}

	options := taskPickerOptions(offered, nil)

	if from.Reopen {
		arguments = append(arguments, "--"+taskExpandedFlag+"="+strconv.FormatBool(from.Expanded),
			"--"+taskTitleFlag+"="+from.Title, "--"+taskAgentFlag+"="+from.Agent)
		options.Expanded, options.Title, options.Agent = from.Expanded, from.Title, from.Agent
	}

	arguments = slices.Concat(arguments, forwardedConfigFlags(cmd))

	width, height := taskpicker.Size(cfg, options)
	width, height = min(width, client.Width), min(height, client.Height)
	popup := popupOptions(gotmux.Exec(executable, arguments...), strconv.Itoa(width), strconv.Itoa(height), cfg.Theme.Background)

	if offered.Project != "" {
		expanded := options
		expanded.Expanded = true
		_, tallest := taskpicker.Size(cfg, expanded)
		top := max(client.Height-min(tallest, client.Height), 0) / centered
		popup.Y = strconv.Itoa(top + height)
	}

	if err := client.Handle().Popup(ctx, popup); err != nil {
		return taskHandoff{}, false, oops.In("cli").With("operation", "open_task_picker").Wrap(err)
	}

	return readHandoff(handoffPath, func(handoff taskHandoff) bool { return handoff.Show != "" || handoff.Reopen })
}

func runTaskPickerUI(cmd *cobra.Command, _ []string) error {
	ctx := clibase.CommandContext(cmd)

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	handoffPath, _ := cmd.Flags().GetString(projectHandoffFlag)
	pane, _ := cmd.Flags().GetString(taskPaneFlag)
	expanded, _ := cmd.Flags().GetBool(taskExpandedFlag)
	title, _ := cmd.Flags().GetString(taskTitleFlag)
	agent, _ := cmd.Flags().GetString(taskAgentFlag)

	if handoffPath == "" || pane == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, errPickerHandoffInvalid)
	}

	runtime, err := actioncli.OpenRuntime(cmd, cfg)
	if err != nil {
		return err
	}

	caller := action.Caller{Client: "", Pane: pane, Session: "", Width: "", Height: ""}

	from := taskHandoff{Show: "", Reopen: true, Expanded: expanded, Title: title, Agent: agent}

	choice, chosen, err := pickTask(ctx, cmd, cfg, runtime, caller, from)
	if err = errors.Join(err, runtime.Close()); err != nil || !chosen {
		return err
	}

	switch {
	case choice.Reopen:
		return writeHandoff(handoffPath, taskHandoff{Show: "", Reopen: true, Expanded: choice.Expanded, Title: choice.Title, Agent: choice.Harness})
	case choice.Show != "":
		return writeHandoff(handoffPath, taskHandoff{Show: choice.Show, Reopen: false, Expanded: false, Title: "", Agent: ""})
	default:
		return nil
	}
}

// pickTask runs the picker, which starts the chosen task or tab through runtime before it closes.
func pickTask(
	ctx context.Context, cmd *cobra.Command, cfg *config.Config, runtime *actioncli.Runtime, caller action.Caller, from taskHandoff,
) (taskpicker.Choice, bool, error) {
	offered, err := runtime.Local.TaskOffer(ctx, caller)
	if err != nil {
		return taskpicker.Choice{}, false, err
	}

	start := func(choice taskpicker.Choice) error {
		if choice.Tab {
			_, err := action.Run(ctx, runtime.Router, action.TaskTab, action.Local(), caller, action.OpenTab{})

			return err
		}

		location := choice.Location
		_, err := action.Run(ctx, runtime.Router, action.TaskNew, action.Local(), caller, action.NewTask{
			Harness: choice.Harness, Where: string(location.Where), Branch: location.Branch, Remote: location.Remote,
			Directory: location.Directory, Title: choice.Title, Project: "",
		})

		return err
	}

	pickerOptions := taskPickerOptions(offered, start)
	if from.Reopen {
		pickerOptions.Expanded, pickerOptions.Title, pickerOptions.Agent = from.Expanded, from.Title, from.Agent
	}

	model := taskpicker.NewModel(cfg, pickerOptions)

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

func taskPickerOptions(offered app.TaskOffer, start func(taskpicker.Choice) error) taskpicker.Options {
	names := make([]string, 0, len(offered.Offer.Harnesses))
	for _, harness := range offered.Offer.Harnesses {
		names = append(names, harness.Name)
	}

	return taskpicker.Options{
		Project: offered.Project, Locations: offered.Offer.Locations, Current: offered.Offer.Current,
		NextNumber: offered.Offer.NextNumber, Branches: offered.Offer.Branches, Harnesses: names,
		Agent: "", Title: "", Expanded: false, Start: start,
	}
}
