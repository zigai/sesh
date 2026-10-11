//go:build linux || darwin

package picker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"syscall"

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
	"github.com/zigai/sesh/internal/project"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui/projectpicker"
)

const (
	projectHandoffFlag  = "handoff"
	projectPickerUIVerb = "pick-ui"
)

type projectHandoff struct {
	Kind  projectpicker.ChoiceKind `json:"kind"`
	Value string                   `json:"value"`
}

// projectFrontend opens the project picker popup for project.pick and then runs the action for
// the choice made in it on the invoking client.
type projectFrontend struct {
	cmd *cobra.Command
}

// NewProjectPickCommand is `sesh project pick`.
func NewProjectPickCommand() *cobra.Command {
	return actioncli.Command(action.ProjectPick, actioncli.Options[action.PickProject, action.PickedProject]{
		Operation:    "project_picker",
		SuccessOwned: false,
		Configure: func(cmd *cobra.Command) {
			cmd.Annotations[clibase.AnnotationLiveIO] = annotationEnabled
		},
		Prepare:  nil,
		Frontend: func(cmd *cobra.Command) action.Dispatcher { return projectFrontend{cmd: cmd} },
		Render:   nil,
	})
}

// NewProjectPickerUICommand is the hidden command the project picker popup runs.
func NewProjectPickerUICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    projectPickerUIVerb,
		Short:  "Run the project picker inside its popup",
		Hidden: true,
		Args:   clibase.UsageArgs(cobra.NoArgs),
		RunE:   runProjectPickerUI,
		Annotations: map[string]string{
			clibase.AnnotationOperation: "project_picker", clibase.AnnotationLiveIO: annotationEnabled,
		},
	}

	actioncli.AddEnvironmentFlags(cmd)
	cmd.Flags().String(projectHandoffFlag, "", "File the choice is written to")

	return cmd
}

func (frontend projectFrontend) Accepts(at action.Location, id action.ID) bool {
	return at.Kind == action.LocationLocal && id == action.ProjectPick.ID
}

func (frontend projectFrontend) Dispatch(ctx context.Context, invocation action.Invocation) (any, error) {
	cmd := frontend.cmd

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return nil, err
	}

	workspace, err := app.Resolve(cfg, clibase.CommandLocations(cmd))
	if err != nil {
		return nil, err
	}

	server, err := tmuxserver.OpenInteractive(workspace.Socket)
	if err != nil {
		return nil, oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	client, err := resolvePopupClient(ctx, invocation.Caller, server)
	if err != nil {
		return nil, oops.In("cli").With("operation", "resolve_popup_client").Wrap(err)
	}

	executable, err := os.Executable()
	if err != nil {
		return nil, oops.In("cli").With("operation", "resolve_executable").Wrap(err)
	}

	handoffPath, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	arguments := slices.Concat(
		[]string{"project", projectPickerUIVerb, "--socket", workspace.Socket, "--" + projectHandoffFlag, handoffPath},
		forwardedConfigFlags(cmd),
	)

	width, height := popupDimensions(cfg, nil, gotmux.Size{Width: client.Width, Height: client.Height})
	options := popupOptions(gotmux.Exec(executable, arguments...), width, height, cfg.Theme.Background)

	if err := client.Handle().Popup(ctx, options); err != nil {
		return nil, oops.In("cli").With("operation", "open_project_picker").Wrap(err)
	}

	handoff, chosen, err := readHandoff(handoffPath, func(handoff projectHandoff) bool { return handoff.Value != "" })
	if err != nil || !chosen {
		return action.PickedProject{}, err
	}

	return action.PickedProject{}, runProjectChoice(ctx, cmd, cfg, invocation.Caller, handoff)
}

func runProjectChoice(ctx context.Context, cmd *cobra.Command, cfg *config.Config, caller action.Caller, handoff projectHandoff) error {
	runtime, err := actioncli.OpenRuntime(cmd, cfg)
	if err != nil {
		return err
	}

	var runErr error

	switch handoff.Kind {
	case projectpicker.ChoiceOpen, projectpicker.ChoiceAdopt:
		_, runErr = action.Run(ctx, runtime.Router, action.ProjectOpen, action.Local(), caller,
			action.OpenProject{Directory: handoff.Value, Adopt: handoff.Kind == projectpicker.ChoiceAdopt})
	case projectpicker.ChoiceClone:
		_, runErr = action.Run(ctx, runtime.Router, action.ProjectClone, action.Local(), caller, action.CloneProject{URL: handoff.Value})
	case projectpicker.ChoiceCreate:
		_, runErr = action.Run(ctx, runtime.Router, action.ProjectCreate, action.Local(), caller, action.CreateProject{Name: handoff.Value})
	default:
		runErr = errPickerHandoffInvalid
	}

	return errors.Join(runErr, runtime.Close())
}

func runProjectPickerUI(cmd *cobra.Command, _ []string) error {
	ctx := clibase.CommandContext(cmd)

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	handoffPath, _ := cmd.Flags().GetString(projectHandoffFlag)
	if handoffPath == "" {
		return clibase.NewExitError(clibase.ExitCodeUsage, errPickerHandoffInvalid)
	}

	workspace, err := app.Resolve(cfg, clibase.CommandLocations(cmd))
	if err != nil {
		return err
	}

	server, err := workspace.OpenTmux()
	if err != nil {
		return err
	}

	entries, err := projectPickerEntries(ctx, cfg, server)
	if err != nil {
		return err
	}

	model := projectpicker.NewModel(cfg, entries)

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
		return oops.In("cli").With("operation", "execute_project_picker").Wrap(err)
	}

	choice, chosen := model.Chosen()
	if !chosen {
		return nil
	}

	return writeHandoff(handoffPath, projectHandoff{Kind: choice.Kind, Value: choice.Value})
}

func projectPickerEntries(ctx context.Context, cfg *config.Config, server *gotmux.Server) ([]projectpicker.Entry, error) {
	catalog, err := project.NewCatalog(cfg.Projects)
	if err != nil {
		return nil, clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	projects, err := catalog.List()
	if err != nil {
		return nil, err
	}

	sessions, err := project.LiveSessions(ctx, server)
	if err != nil && !errors.Is(err, gotmux.ErrNoServer) {
		return nil, err
	}

	entries := make([]projectpicker.Entry, 0, len(projects))

	for _, p := range projects {
		entry := projectpicker.Entry{Root: p.Root, Name: p.Name, Path: session.ShortenHomePath(p.Root), Session: "", Taken: ""}

		for _, live := range sessions {
			switch {
			case live.Root == p.Root:
				entry.Session = live.Name
			case live.Root == "" && live.Name == project.SessionName(p):
				entry.Taken = live.Name
			}
		}

		if entry.Session != "" {
			entry.Taken = ""
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

func forwardedConfigFlags(cmd *cobra.Command) []string {
	var arguments []string

	for _, flag := range []string{"config", "repository", "lease-root", "history", "agent-state"} {
		if cmd.Flags().Lookup(flag) == nil {
			continue
		}

		value, _ := cmd.Flags().GetString(flag)
		if value != "" {
			arguments = append(arguments, "--"+flag, value)
		}
	}

	if noConfig, _ := cmd.Flags().GetBool("no-config"); noConfig {
		arguments = append(arguments, "--no-config")
	}

	return arguments
}

// writeHandoff writes a popup's choice to the handoff file its opener created.
func writeHandoff[T any](path string, handoff T) error {
	encoded, err := json.Marshal(handoff)
	if err != nil {
		return oops.In("cli").With("operation", "encode_picker_handoff").Wrap(err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return oops.In("cli").With("operation", "open_picker_handoff").Wrap(err)
	}

	_, writeErr := file.Write(encoded)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return oops.In("cli").With("operation", "write_picker_handoff").Wrap(err)
	}

	return nil
}

// readHandoff reads the choice a popup wrote, reporting false when the popup closed without
// one. A choice that does not decode or that valid rejects is an error.
func readHandoff[T any](path string, valid func(T) bool) (T, bool, error) {
	var handoff T

	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return handoff, false, oops.In("cli").With("operation", "open_picker_handoff").Wrap(err)
	}

	encoded, readErr := io.ReadAll(io.LimitReader(file, handoffMaxBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return handoff, false, oops.In("cli").With("operation", "read_picker_handoff").Wrap(err)
	}

	if len(encoded) == 0 {
		return handoff, false, nil
	}

	if len(encoded) > handoffMaxBytes || json.Unmarshal(encoded, &handoff) != nil || !valid(handoff) {
		var empty T

		return empty, false, oops.In("cli").With("operation", "read_picker_handoff").Wrap(errPickerHandoffInvalid)
	}

	return handoff, true, nil
}
