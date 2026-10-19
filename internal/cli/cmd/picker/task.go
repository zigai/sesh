//go:build linux || darwin

package picker

import (
	"cmp"
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"time"

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
	"github.com/zigai/sesh/internal/git"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
	"github.com/zigai/sesh/internal/tui"
	"github.com/zigai/sesh/internal/tui/taskpicker"
)

const (
	taskPickerUIVerb = "pick-ui"
	taskPaneFlag     = "pane"
	taskExpandedFlag = "expanded"
	taskTitleFlag    = "title"
	taskAgentFlag    = "agent"
	taskMachineFlag  = "machine"
	taskProjectFlag  = "project"
	thisMachine      = "local"
	centered         = 2
)

var errRemoteTaskSession = errors.New("the remote task's session is not in the remote catalog")

// taskHandoff names the task the task picker popup hands to its opener to go to, or asks it to
// reopen the popup on Machine, expanded or not, with the title and agent chosen so far. Machine
// is a remote connection, empty for this machine.
type taskHandoff struct {
	Show     string `json:"show"`
	Reopen   bool   `json:"reopen"`
	Expanded bool   `json:"expanded"`
	Title    string `json:"title"`
	Agent    string `json:"agent"`
	Machine  string `json:"machine"`
	Session  string `json:"session"`
	Pane     string `json:"pane"`
}

type taskChoices struct {
	options       taskpicker.Options
	machines      []action.Location
	remoteProject string
	remote        RemoteServices
}

type taskFrontend struct {
	cmd    *cobra.Command
	remote RemoteFactory
}

func NewTaskMenuCommand(remoteFactory RemoteFactory) *cobra.Command {
	return actioncli.Command(action.TaskMenu, actioncli.Options[action.OpenTaskMenu, action.OpenedTaskMenu]{
		Operation:    "task_picker",
		SuccessOwned: false,
		Configure: func(cmd *cobra.Command) {
			cmd.Annotations[clibase.AnnotationLiveIO] = annotationEnabled
		},
		Prepare:  nil,
		Frontend: func(cmd *cobra.Command) action.Dispatcher { return taskFrontend{cmd: cmd, remote: remoteFactory} },
		Render:   nil,
	})
}

func NewTaskPickerUICommand(remoteFactory RemoteFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:    taskPickerUIVerb,
		Short:  "Run the task picker inside its popup",
		Hidden: true,
		Args:   clibase.UsageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTaskPickerUI(cmd, args, remoteFactory)
		},
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
	cmd.Flags().String(taskMachineFlag, "", "Remote connection to start the task on")
	cmd.Flags().String(taskProjectFlag, "", "Project to start the task in instead of the pane's")

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

	err = openTaskPicker(ctx, cmd, cfg, runtime, invocation.Caller, frontend.remote, "")

	return action.OpenedTaskMenu{}, errors.Join(err, runtime.Close())
}

func openTaskPicker(
	ctx context.Context, cmd *cobra.Command, cfg *config.Config, runtime *actioncli.Runtime, caller action.Caller, remoteFactory RemoteFactory,
	dir string,
) error {
	if caller.Pane == "" {
		caller.Pane = os.Getenv("TMUX_PANE")
	}

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

	handoff := taskHandoff{Show: "", Reopen: false, Expanded: false, Title: "", Agent: "", Machine: "", Session: "", Pane: ""}

	for {
		choices, err := loadTaskChoices(ctx, cfg, workspace.Socket, runtime, caller, remoteFactory, dir, handoff.Machine, false)
		if err != nil {
			return err
		}

		next, chosen, err := showTaskPopup(ctx, cmd, cfg, workspace.Socket, client, caller, dir, choices.options, handoff)
		if err != nil || !chosen {
			return err
		}

		if next.Session != "" {
			return showRemoteAttachPopup(ctx, cmd, cfg, workspace.Socket, client, next)
		}

		if !next.Reopen {
			return runtime.Local.ShowTask(ctx, caller, next.Show)
		}

		handoff = next
	}
}

func showTaskPopup(
	ctx context.Context, cmd *cobra.Command, cfg *config.Config, socket string, client gotmux.ClientInfo, caller action.Caller,
	dir string, options taskpicker.Options, from taskHandoff,
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

	if dir != "" {
		arguments = append(arguments, "--"+taskProjectFlag+"="+dir)
	}

	if from.Reopen {
		arguments = append(arguments, "--"+taskExpandedFlag+"="+strconv.FormatBool(from.Expanded),
			"--"+taskTitleFlag+"="+from.Title, "--"+taskAgentFlag+"="+from.Agent, "--"+taskMachineFlag+"="+from.Machine)
		options.Expanded, options.Title, options.Agent = from.Expanded, from.Title, from.Agent
	}

	arguments = slices.Concat(arguments, forwardedConfigFlags(cmd))

	width, height := taskpicker.Size(cfg, options)
	width, height = min(width, client.Width), min(height, client.Height)
	popup := popupOptions(gotmux.Exec(executable, arguments...), strconv.Itoa(width), strconv.Itoa(height), cfg.Theme.Background)

	if options.Project != "" {
		expanded := options
		expanded.Expanded = true
		_, tallest := taskpicker.Size(cfg, expanded)
		top := max(client.Height-min(tallest, client.Height), 0) / centered
		popup.Y = strconv.Itoa(top + height)
	}

	if err := client.Handle().Popup(ctx, popup); err != nil {
		return taskHandoff{}, false, oops.In("cli").With("operation", "open_task_picker").Wrap(err)
	}

	return readHandoff(handoffPath, func(handoff taskHandoff) bool { return handoff.Show != "" || handoff.Reopen || handoff.Session != "" })
}

func showRemoteAttachPopup(
	ctx context.Context, cmd *cobra.Command, cfg *config.Config, socket string, client gotmux.ClientInfo, to taskHandoff,
) error {
	executable, err := os.Executable()
	if err != nil {
		return oops.In("cli").With("operation", "resolve_executable").Wrap(err)
	}

	arguments := []string{"remote", "attach", to.Machine, to.Session, "--origin-socket", socket}
	if to.Pane != "" {
		arguments = append(arguments, "--pane", to.Pane)
	}

	if path, _ := cmd.Flags().GetString("config"); path != "" {
		arguments = append(arguments, "--config", path)
	} else if noConfig, _ := cmd.Flags().GetBool("no-config"); noConfig {
		arguments = append(arguments, "--no-config")
	}

	width, height := adaptivePickerPopupDimensions(cfg.Popup.AgentsWidth, cfg.Popup.AgentsHeight,
		gotmux.Size{Width: client.Width, Height: client.Height},
		gotmux.Size{Width: popupAgentsMinWidth, Height: popupAgentsMinHeight},
		gotmux.Size{Width: popupAgentsDefaultWidth, Height: popupAgentsDefaultHeight})

	popup := popupOptions(gotmux.Exec(executable, arguments...), width, height, cfg.Theme.Background)
	if err := client.Handle().Popup(ctx, popup); err != nil {
		return oops.In("cli").With("operation", "open_remote_attach").Wrap(err)
	}

	return nil
}

func runTaskPickerUI(cmd *cobra.Command, _ []string, remoteFactory RemoteFactory) error {
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
	machine, _ := cmd.Flags().GetString(taskMachineFlag)
	dir, _ := cmd.Flags().GetString(taskProjectFlag)

	if handoffPath == "" || pane == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, errPickerHandoffInvalid)
	}

	runtime, err := actioncli.OpenRuntime(cmd, cfg)
	if err != nil {
		return err
	}

	caller := action.Caller{Client: "", Pane: pane, Session: "", Width: "", Height: ""}

	from := taskHandoff{Show: "", Reopen: true, Expanded: expanded, Title: title, Agent: agent, Machine: machine, Session: "", Pane: ""}

	picked, chosen, err := pickTask(ctx, cmd, cfg, runtime, caller, remoteFactory, dir, from)
	if dir == "" {
		picked.started = ""
	}

	if err = errors.Join(err, runtime.Close()); err != nil || !chosen {
		return err
	}

	return finishTaskPicker(ctx, handoffPath, picked)
}

func finishTaskPicker(ctx context.Context, handoffPath string, picked pickedTask) error {
	choice := picked.choice

	switch {
	case choice.Reopen:
		return writeHandoff(handoffPath, taskHandoff{
			Show: "", Reopen: true, Expanded: choice.Expanded, Title: choice.Title, Agent: choice.Harness,
			Machine: picked.machines[choice.Machine].Connection, Session: "", Pane: "",
		})
	case picked.at.Kind == action.LocationPeer && (choice.Show != "" || picked.session != ""):
		return handOffRemoteTask(ctx, handoffPath, picked)
	case choice.Show != "" || picked.started != "":
		return writeHandoff(handoffPath, taskHandoff{
			Show: cmp.Or(choice.Show, picked.started), Reopen: false, Expanded: false, Title: "", Agent: "", Machine: "", Session: "", Pane: "",
		})
	default:
		return nil
	}
}

// pickedTask is what the popup ended with: the choice, the popup's machines, on a remote
// connection the session the started task opened in, and the task started in another project's
// session, which the opener goes to.
type pickedTask struct {
	choice   taskpicker.Choice
	machines []action.Location
	at       action.Location
	session  string
	started  string
	remote   RemoteServices
}

func pickTask(
	ctx context.Context, cmd *cobra.Command, cfg *config.Config, runtime *actioncli.Runtime, caller action.Caller,
	remoteFactory RemoteFactory, dir string, from taskHandoff,
) (pickedTask, bool, error) {
	picked := pickedTask{choice: emptyChoice(), machines: nil, at: action.Local(), session: "", started: "", remote: emptyRemoteServices()}

	workspace, err := app.Resolve(cfg, clibase.CommandLocations(cmd))
	if err != nil {
		return picked, false, err
	}

	choices, err := loadTaskChoices(ctx, cfg, workspace.Socket, runtime, caller, remoteFactory, dir, from.Machine, from.Expanded)
	if err != nil {
		return picked, false, err
	}

	picked.machines, picked.remote = choices.machines, choices.remote
	picked.at = choices.machines[choices.options.Machine]

	start := func(choice taskpicker.Choice) error {
		location := choice.Location
		input := action.NewTask{
			Harness: choice.Harness, Where: string(location.Where), Branch: location.Branch, Remote: location.Remote,
			Directory: location.Directory, Title: choice.Title, Project: dir, Layout: "",
		}

		switch {
		case picked.at.Kind == action.LocationPeer:
			input.Project = choices.remoteProject
			router := action.Router{Local: nil, Peer: choices.remote.Peer, Origin: nil}

			started, err := action.Run(ctx, router, action.TaskNew, picked.at, emptyCaller(), input)
			picked.session = started.Session

			return err
		case choice.Tab:
			_, err := action.Run(ctx, runtime.Router, action.TaskTab, action.Local(), caller, action.OpenTab{})

			return err
		default:
			started, err := action.Run(ctx, runtime.Router, action.TaskNew, action.Local(), caller, input)
			picked.started = started.ID

			return err
		}
	}

	pickerOptions := choices.options
	pickerOptions.Start = start

	if from.Reopen {
		pickerOptions.Expanded, pickerOptions.Title, pickerOptions.Agent = from.Expanded, from.Title, from.Agent
	}

	if pickerOptions.Expanded && picked.at.Kind == action.LocationLocal {
		counting, stopCounting := context.WithCancel(ctx)
		defer stopCounting()

		pickerOptions.Changes = func() map[string]git.Diff {
			return task.Changes(counting, checkoutDirectories(pickerOptions.Locations))
		}
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
		return picked, false, oops.In("cli").With("operation", "execute_task_picker").Wrap(err)
	}

	choice, chosen := model.Chosen()
	picked.choice = choice

	return picked, chosen, nil
}

func loadTaskChoices(
	ctx context.Context, cfg *config.Config, socket string, runtime *actioncli.Runtime, caller action.Caller,
	remoteFactory RemoteFactory, dir, machine string, describe bool,
) (taskChoices, error) {
	choices := taskChoices{options: taskPickerOptions("", emptyTaskOffered()), machines: []action.Location{action.Local()}, remoteProject: "", remote: emptyRemoteServices()}

	at := machineLocation(machine)

	offered, err := runtime.Local.TaskOffer(ctx, caller, dir, describe && at.Kind == action.LocationLocal)
	if err != nil {
		return choices, err
	}

	choices.options = taskPickerOptions(offered.Project, app.OfferedTask(offered.Offer))

	if offered.Project == "" || remoteFactory == nil || !cfg.Remote.Enabled {
		return choices, nil
	}

	choices.remote, err = remoteFactory(ctx, cfg, socket)
	if err != nil {
		return choices, oops.In("cli").With("operation", "configure_remote_task").Wrap(err)
	}

	if choices.remote.Catalog == nil || choices.remote.Peer == nil {
		return choices, nil
	}

	machines, err := choices.listMachines()
	if err != nil {
		return choices, err
	}

	choices.options.Machines = machines

	index := slices.Index(choices.machines, at)
	if index <= 0 || !machines[index].Usable {
		return choices, nil
	}

	choices.useMachine(ctx, offered.Project, index, describe)

	return choices, nil
}

func machineLocation(machine string) action.Location {
	if machine == "" {
		return action.Local()
	}

	return action.Peer(machine)
}

func (choices *taskChoices) listMachines() ([]taskpicker.Machine, error) {
	catalog, err := choices.remote.Catalog.LoadRemoteCatalog()
	if err != nil {
		return nil, err
	}

	machines := []taskpicker.Machine{{Name: thisMachine, Usable: true}}

	for _, connection := range catalog.Connections {
		if !connection.Connected {
			continue
		}

		at := action.Peer(connection.ID)
		usable := choices.remote.Peer.Accepts(at, action.TaskOffer.ID) && choices.remote.Peer.Accepts(at, action.TaskNew.ID)
		machines = append(machines, taskpicker.Machine{Name: cmp.Or(connection.Label, connection.ID), Usable: usable})
		choices.machines = append(choices.machines, at)
	}

	return machines, nil
}

func (choices *taskChoices) useMachine(ctx context.Context, project string, index int, describe bool) {
	machines := choices.options.Machines
	router := action.Router{Local: nil, Peer: choices.remote.Peer, Origin: nil}
	remoteOffer, err := action.Run(ctx, router, action.TaskOffer, choices.machines[index], emptyCaller(),
		action.OfferTask{Project: project, Describe: describe})

	switch {
	case action.CodeOf(err) == action.CodeNotFound:
		choices.options = taskPickerOptions(project, emptyTaskOffered())
		choices.options.Problem = "not on " + machines[index].Name
	case err != nil:
		choices.options = taskPickerOptions(project, emptyTaskOffered())
		choices.options.Problem = machines[index].Name + " unavailable"
	default:
		choices.options = taskPickerOptions(project, remoteOffer)
		choices.remoteProject = remoteOffer.Project
	}

	choices.options.Machines, choices.options.Machine = machines, index
}

func taskPickerOptions(project string, offered action.TaskOffered) taskpicker.Options {
	locations := make([]task.Location, 0, len(offered.Locations))
	for _, location := range offered.Locations {
		locations = append(locations, task.Location{
			Where: task.Where(location.Where), Branch: location.Branch, Remote: location.Remote,
			Directory: location.Directory, Task: location.Task, TaskTitle: location.TaskTitle,
		})
	}

	refs := make(map[string]task.Ref, len(offered.Refs))
	for name, ref := range offered.Refs {
		refs[name] = task.Ref{Time: ref.Time, Subject: ref.Subject, Ahead: ref.Ahead, Behind: ref.Behind}
	}

	return taskpicker.Options{
		Project: project, Locations: locations, Current: offered.Current,
		NextNumber: offered.NextNumber, Branches: offered.Branches, Harnesses: offered.Harnesses,
		Agent: "", Title: "", Expanded: false, Refs: refs, Starts: offered.Starts, Now: time.Now(), Changes: nil, Start: nil,
		Machines: nil, Machine: 0, Problem: "",
	}
}

func handOffRemoteTask(ctx context.Context, handoffPath string, picked pickedTask) error {
	if picked.remote.Watcher == nil {
		return oops.In("cli").With("operation", "attach_remote_task").Wrap(errRemoteAttachUnavailable)
	}

	sessionID, pane := picked.session, ""

	target, err := waitRemoteTaskSession(ctx, picked, func(connection tui.RemoteConnection) (session.Session, bool) {
		var none session.Session

		if picked.choice.Show != "" {
			index := slices.IndexFunc(connection.Tasks, func(live task.Live) bool { return live.Record.ID == picked.choice.Show })
			if index < 0 {
				return none, false
			}

			sessionID, pane = string(connection.Tasks[index].SessionID), string(connection.Tasks[index].AgentPane)
		}

		index := slices.IndexFunc(connection.Sessions, func(row session.Session) bool { return row.ID == sessionID })
		if index < 0 {
			return none, false
		}

		return connection.Sessions[index], true
	})
	if err != nil {
		return err
	}

	return writeHandoff(handoffPath, taskHandoff{
		Show: "", Reopen: false, Expanded: false, Title: "", Agent: "", Machine: picked.at.Connection, Session: target.ID, Pane: pane,
	})
}

// waitRemoteTaskSession waits until the remote catalog shows the session find looks for on the
// picked connection. A new project session reaches the catalog with the peer's next inventory.
func waitRemoteTaskSession(
	ctx context.Context, picked pickedTask, find func(tui.RemoteConnection) (session.Session, bool),
) (session.Session, error) {
	watching, stop := context.WithCancel(ctx)
	defer stop()

	var (
		found  session.Session
		listed bool
	)

	err := picked.remote.Watcher.WatchRemoteCatalog(watching, func(catalog tui.RemoteCatalog, err error) {
		if err != nil || listed {
			return
		}

		for _, connection := range catalog.Connections {
			if connection.ID == picked.at.Connection && connection.Connected {
				found, listed = find(connection)
			}
		}

		if listed {
			stop()
		}
	})

	switch {
	case listed:
		return found, nil
	case err != nil && ctx.Err() == nil:
		return found, oops.In("cli").With("operation", "find_remote_task_session").Wrap(err)
	default:
		return found, oops.In("cli").With("operation", "find_remote_task_session").Wrap(errors.Join(errRemoteTaskSession, ctx.Err()))
	}
}

func emptyChoice() taskpicker.Choice {
	return taskpicker.Choice{
		Reopen: false, Expanded: false, Machine: 0, Tab: false, Harness: "", Title: "", Location: task.Location{
			Where: "", Branch: "", Remote: "", Directory: "", Task: "", TaskTitle: "",
		},
		Show: "",
	}
}

func emptyCaller() action.Caller {
	return action.Caller{Client: "", Pane: "", Session: "", Width: "", Height: ""}
}

func emptyRemoteServices() RemoteServices {
	return RemoteServices{Catalog: nil, Watcher: nil, Peer: nil, Attach: nil, OriginBridge: nil, Origin: nil}
}

func emptyTaskOffered() action.TaskOffered {
	return action.TaskOffered{
		Project: "", Harnesses: nil, Locations: nil, Current: -1, NextNumber: 0, Branches: nil, Refs: nil, Starts: nil,
	}
}

func checkoutDirectories(locations []task.Location) []string {
	var directories []string

	for _, location := range locations {
		if location.Directory != "" && !slices.Contains(directories, location.Directory) {
			directories = append(directories, location.Directory)
		}
	}

	return directories
}
