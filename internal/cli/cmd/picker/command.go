//go:build linux || darwin

package picker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
	"github.com/samber/oops"
	"github.com/spf13/cobra"
	"github.com/zigai/aht/v2/pkg/aht"
	gotmux "github.com/zigai/gotmux/tmux"
	"golang.org/x/sync/errgroup"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui"
)

const (
	presentationPopup presentation = iota
	presentationInline
	presentationFullscreen
)

const (
	annotationEnabled = "true"
	clientEnvironment = "SESH_TMUX_CLIENT"
	launchedAtFlag    = "launched-at"
	uiFlag            = "ui"
	uiAuto            = "auto"
	launchMaxAge      = time.Minute
)

const (
	popupTerminalMargin      = 2
	popupSeshMinWidth        = 40
	popupSeshMinHeight       = 12
	popupSeshDefaultWidth    = 64
	popupSeshDefaultHeight   = 16
	popupAgentsMinWidth      = 60
	popupAgentsMinHeight     = 14
	popupAgentsDefaultWidth  = 80
	popupAgentsDefaultHeight = 20
	popupPercentBase         = 100
	renderFPS                = 120
)

const (
	nameCacheLimit       = 128
	nameReaders          = 8
	nameLineLimit        = 64 * 1024
	nameFileReadLimit    = 8 * 1024 * 1024
	nameRefreshReadLimit = 64 * 1024 * 1024
)

var (
	errPickerTerminalRequired                   = errors.New("picker requires an interactive terminal")
	errUnknownPickerPresentation                = errors.New("unknown picker presentation")
	errInvalidPickerUI                          = errors.New("invalid picker ui: expected auto, popup, inline, or fullscreen")
	errRemoteAttachUnavailable                  = errors.New("remote attach is unavailable")
	errRemoteAttachCommandUnavailable           = errors.New("remote attach returned no terminal command")
	_                                 term.File = terminalOutput{Writer: nil, fd: 0}
)

var DefaultPickerRemoteFactory RemoteFactory

type presentation uint8

type fileDescriptor interface {
	Fd() uintptr
}

type commandRuntime struct {
	workspace app.Workspace
	state     *session.State
}

type terminalOutput struct {
	io.Writer

	fd uintptr
}

type transcriptNameCache struct {
	mu    sync.Mutex
	cache map[string]sessionNameState
}

type sessionNameState struct {
	offset   int64
	name     string
	hasInfo  bool
	skipping bool
	fileInfo os.FileInfo
}

type sessionNameLoad struct {
	path   string
	state  sessionNameState
	agents []*session.AgentSession
}

type sessionInfo struct {
	Type string  `json:"type"`
	Name *string `json:"name"`
}

func (o terminalOutput) Read([]byte) (int, error) { return 0, io.EOF }

func (o terminalOutput) Close() error { return nil }

func (o terminalOutput) Fd() uintptr { return o.fd }

func InvokingPickerClient(ctx context.Context, server *gotmux.Server) (gotmux.ClientInfo, error) {
	return invokingPickerClient(ctx, server)
}

// NewPickCommand returns the interactive session picker. The popup layout also serves as the
// child that picker-open runs inside a tmux popup, so the handoff flags stay hidden.
func NewPickCommand(ctx context.Context, remote ...RemoteFactory) *cobra.Command {
	remoteFactory := firstPickerRemoteFactory(remote)
	cmd := &cobra.Command{
		Use:         "pick",
		Short:       "Open the interactive session picker",
		Args:        clibase.UsageArgs(cobra.NoArgs),
		Annotations: map[string]string{clibase.AnnotationLiveIO: annotationEnabled, clibase.AnnotationOperation: "picker"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPickCommand(executionContext(ctx, cmd), cmd, remoteFactory)
		},
	}

	addPickerPathFlags(cmd)
	cmd.Flags().String(uiFlag, uiAuto, "Picker layout")
	clibase.SetFlagChoices(cmd, uiFlag, uiAuto, "popup", "inline", "fullscreen")
	cmd.Flags().String("switch-handoff", "", "Popup switch handoff file")
	cmd.Flags().Int64(launchedAtFlag, 0, "Popup launch time")
	cmd.Flags().String("target-pane", "", "Invoking tmux pane")
	cmd.Flags().String("current-session-id", "", "Invoking tmux session ID")

	for _, name := range []string{"switch-handoff", launchedAtFlag, "target-pane", "current-session-id"} {
		_ = cmd.Flags().MarkHidden(name)
	}

	cmd.AddCommand(newPickPopupCommand())

	return cmd
}

func HasTerminal(cmd *cobra.Command) bool {
	return hasTerminal(cmd)
}

func ProgramOutput(output io.Writer) io.Writer {
	return programOutput(output)
}

func firstPickerRemoteFactory(values []RemoteFactory) RemoteFactory {
	if len(values) == 0 {
		return DefaultPickerRemoteFactory
	}

	return values[0]
}

func addPickerPathFlags(cmd *cobra.Command) {
	clibase.AddSocketFlag(cmd)
	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().String("lease-root", "", clibase.LeaseRootFlagUsage)
	cmd.Flags().String("history", "", clibase.HistoryFlagUsage)
	cmd.Flags().String("agent-state", "", "AHT agent registry file [default: agent_status.store_path setting, else AHT's default registry]")
}

func executionContext(parent context.Context, cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}

	return parent
}

func commandTerminalStreams(cmd *cobra.Command) gotmux.Streams {
	var streams gotmux.Streams

	if in, ok := cmd.InOrStdin().(*os.File); ok {
		streams.In = in
	}

	if out, ok := cmd.OutOrStdout().(*os.File); ok {
		streams.Out = out
	}

	if errFile, ok := cmd.ErrOrStderr().(*os.File); ok {
		streams.Err = errFile
	}

	return streams
}

func remoteCLIError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return oops.In("cli").With("operation", operation).Wrap(err)
}

func runPickCommand(ctx context.Context, cmd *cobra.Command, remoteFactory RemoteFactory) error {
	choice, _ := cmd.Flags().GetString(uiFlag)

	presentation, err := resolvePickerPresentation(choice, os.Getenv)
	if err != nil {
		return err
	}

	runtime, err := preparePickerCommand(cmd)
	if err != nil {
		return err
	}

	if !hasTerminal(cmd) {
		return clibase.NewExitError(clibase.ExitCodeUsage, errPickerTerminalRequired)
	}

	return runPickerInteractive(ctx, cmd, presentation, runtime, remoteFactory)
}

// resolvePickerPresentation maps the --ui choice to a layout. auto uses the popup layout inside
// a tmux popup, inline elsewhere inside tmux, and fullscreen outside tmux.
func resolvePickerPresentation(choice string, getenv func(string) string) (presentation, error) {
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "", uiAuto:
		switch {
		case strings.TrimSpace(getenv("TMUX")) == "":
			return presentationFullscreen, nil
		case getenv("TMUX_POPUP") != "":
			return presentationPopup, nil
		default:
			return presentationInline, nil
		}
	case "popup":
		return presentationPopup, nil
	case "inline":
		return presentationInline, nil
	case "fullscreen":
		return presentationFullscreen, nil
	default:
		return 0, clibase.NewExitError(clibase.ExitCodeUsage, errInvalidPickerUI)
	}
}

func runPickerInteractive(
	ctx context.Context,
	cmd *cobra.Command,
	presentation presentation,
	runtime commandRuntime,
	remoteFactory RemoteFactory,
) error {
	remote, err := configurePickerRemote(ctx, runtime.workspace.Config, runtime.workspace.Socket, remoteFactory)
	if err != nil {
		return err
	}

	services, err := newServices(ctx, runtime.workspace, commandTerminalStreams(cmd), remote)
	if err != nil {
		return err
	}

	services.currentPane = currentPane(cmd)
	handoffPath, _ := cmd.Flags().GetString("switch-handoff")
	services.popupChild = handoffPath != ""
	services.currentSessionID, _ = cmd.Flags().GetString("current-session-id")
	services.startInitialReads(ctx)

	model, err := buildPickerModel(ctx, presentation, runtime.workspace.Config, runtime.state, services.TUIServices())
	if err != nil {
		return errors.Join(oops.In("cli").With("operation", "construct_picker").Wrap(err), services.Close())
	}

	if launchedAt, ok := launchedAt(cmd, time.Now()); ok {
		model.SetLaunchedAt(launchedAt)
	}

	finalModel, runErr := runPickerProgram(ctx, cmd, model, presentation)

	return finishPickerInteractive(ctx, cmd, finalModel, services, runErr)
}

// launchedAt returns when the invoking picker-open started, ignoring a value that
// cannot be a recent launch on this machine.
func launchedAt(cmd *cobra.Command, now time.Time) (time.Time, bool) {
	if cmd.Flags().Lookup(launchedAtFlag) == nil {
		return time.Time{}, false
	}

	nanos, err := cmd.Flags().GetInt64(launchedAtFlag)
	if err != nil || nanos <= 0 {
		return time.Time{}, false
	}

	launchedAt := time.Unix(0, nanos)
	if launchedAt.After(now) || now.Sub(launchedAt) > launchMaxAge {
		return time.Time{}, false
	}

	return launchedAt, true
}

func finishPickerInteractive(ctx context.Context, cmd *cobra.Command, finalModel *tui.Model, services *services, runErr error) error {
	var deferredErr error

	var handoffTarget *session.Session

	var handoffPane string

	handoffPath, _ := cmd.Flags().GetString("switch-handoff")

	if runErr == nil && ctx.Err() == nil {
		if target, ok := finalModel.DeferredSwitchTarget(); ok && handoffPath != "" {
			handoffTarget = &target
			handoffPane = finalModel.DeferredSwitchPane()
		} else {
			deferredErr = runPickerDeferred(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), finalModel, services)
		}
	}

	closeErr := services.Close()
	if handoffPath != "" && closeErr == nil {
		handoffErr := writePickerSwitchHandoff(handoffPath, handoffTarget, handoffPane, finalModel.ReplayKey(), services.takeDeferredAgentViews())
		deferredErr = errors.Join(deferredErr, handoffErr)
	}

	if runErr != nil {
		runErr = oops.In("cli").With("operation", "run_picker").Wrap(runErr)
	}

	return errors.Join(runErr, deferredErr, closeErr)
}

func preparePickerCommand(cmd *cobra.Command) (commandRuntime, error) {
	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return commandRuntime{}, err
	}

	state, err := session.LoadState(workspace.History)
	if err != nil {
		return commandRuntime{}, oops.In("cli").With("operation", "load_history").Wrap(err)
	}

	return commandRuntime{workspace: workspace, state: state}, nil
}

func configurePickerRemote(
	ctx context.Context,
	cfg *config.Config,
	socketPath string,
	remoteFactory RemoteFactory,
) (RemoteServices, error) {
	if remoteFactory == nil {
		return RemoteServices{
			Catalog: nil, Watcher: nil, Peer: nil, Attach: nil, OriginBridge: nil, Origin: nil,
		}, nil
	}

	remote, err := remoteFactory(ctx, cfg, socketPath)
	if err != nil {
		return RemoteServices{}, oops.In("cli").With("operation", "configure_remote_picker").Wrap(err)
	}

	return remote, nil
}

func hasTerminal(cmd *cobra.Command) bool {
	input, inputOK := cmd.InOrStdin().(fileDescriptor)
	output, outputOK := cmd.OutOrStdout().(fileDescriptor)

	return inputOK && outputOK && term.IsTerminal(input.Fd()) && term.IsTerminal(output.Fd())
}

func currentPane(cmd *cobra.Command) string {
	if flag := cmd.Flags().Lookup("target-pane"); flag != nil {
		pane, _ := cmd.Flags().GetString("target-pane")
		if pane != "" {
			return pane
		}
	}

	return os.Getenv("TMUX_PANE")
}

func applyClientDimensionOverrides(caller action.Caller, client *gotmux.ClientInfo) {
	if width, err := strconv.Atoi(caller.Width); err == nil && width > 0 {
		client.Width = width
	}

	if height, err := strconv.Atoi(caller.Height); err == nil && height > 0 {
		client.Height = height
	}
}

func resolvePopupClient(ctx context.Context, caller action.Caller, server *gotmux.Server) (gotmux.ClientInfo, error) {
	targetClient := caller.Client
	if targetClient == "" {
		targetClient = os.Getenv(clientEnvironment)
	}

	clients, err := server.Clients(ctx)
	if err != nil {
		return gotmux.ClientInfo{}, remoteCLIError("list_picker_clients", err)
	}

	if targetClient != "" {
		for _, c := range clients {
			if string(c.Name) == targetClient && !c.Control {
				applyClientDimensionOverrides(caller, &c)

				return c, nil
			}
		}

		return gotmux.ClientInfo{}, session.ErrInvokingClientUnavailable
	}

	var eligible []gotmux.ClientInfo

	for _, c := range clients {
		if !c.Control {
			eligible = append(eligible, c)
		}
	}

	if len(eligible) == 0 {
		return gotmux.ClientInfo{}, session.ErrInvokingClientUnavailable
	}

	if len(eligible) == 1 {
		applyClientDimensionOverrides(caller, &eligible[0])

		return eligible[0], nil
	}

	invoked, err := invokingPickerClient(ctx, server)
	if err != nil {
		return gotmux.ClientInfo{}, err
	}

	applyClientDimensionOverrides(caller, &invoked)

	return invoked, nil
}

func openPickerPopup(ctx context.Context, cmd *cobra.Command, runtime commandRuntime, caller action.Caller, launchedAt time.Time) error {
	server, err := tmuxserver.OpenInteractive(runtime.workspace.Socket)
	if err != nil {
		return oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	executable, err := os.Executable()
	if err != nil {
		return oops.In("cli").With("operation", "resolve_executable").Wrap(err)
	}

	client, err := resolvePopupClient(ctx, caller, server)
	if err != nil {
		return oops.In("cli").With("operation", "resolve_popup_client").Wrap(err)
	}

	originSessionID, present := client.SessionID.Get()
	if !present || originSessionID == "" {
		return session.ErrInvokingClientUnavailable
	}

	if err := checkPopupClientSession(ctx, server, caller.Session, string(originSessionID)); err != nil {
		return err
	}

	handoffPath, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		return err
	}
	defer cleanup()

	targetPane := caller.Pane
	if targetPane == "" {
		targetPane = os.Getenv("TMUX_PANE")
	}

	width, height := popupDimensions(runtime.workspace.Config, runtime.state, gotmux.Size{Width: client.Width, Height: client.Height})
	options := popupOptions(
		gotmux.Exec(executable, popupArguments(cmd, runtime.workspace.Socket, string(originSessionID), targetPane, handoffPath, launchedAt)...),
		width,
		height,
		runtime.workspace.Config.Theme.Background,
	)

	services, err := newServices(ctx, runtime.workspace, gotmux.Streams{In: nil, Out: nil, Err: nil},
		RemoteServices{Catalog: nil, Watcher: nil, Peer: nil, Attach: nil, OriginBridge: nil, Origin: nil})
	if err != nil {
		return err
	}

	if err := client.Handle().Popup(ctx, options); err != nil {
		return errors.Join(oops.In("cli").With("operation", "open_picker_popup").Wrap(err), services.Close())
	}

	return errors.Join(
		completePickerSwitchHandoff(ctx, services, handoffPath, string(client.Name), string(originSessionID)),
		replayPickerKey(ctx, handoffPath, runtime.workspace.Socket, string(client.Name)),
		services.Close(),
	)
}

func popupOptions(program gotmux.Program, width, height, background string) gotmux.PopupOptions {
	var opts gotmux.PopupOptions

	opts.Program = program
	opts.Width = width
	opts.Height = height
	opts.Borderless = true

	bg := strings.TrimSpace(background)
	switch {
	case bg == "":
		opts.Style = "bg=#000000"
	case strings.EqualFold(bg, "default"):
		opts.Style = ""
	default:
		opts.Style = "bg=" + bg
	}

	opts.X = "C"
	opts.Y = "C"
	opts.CloseOnExit = true

	return opts
}

func popupArguments(cmd *cobra.Command, socketPath, sessionID, paneID, handoffPath string, launchedAt time.Time) []string {
	arguments := []string{"pick", "--" + uiFlag, "popup", "--socket", socketPath}

	if !launchedAt.IsZero() {
		arguments = append(arguments, "--"+launchedAtFlag, strconv.FormatInt(launchedAt.UnixNano(), 10))
	}

	if handoffPath != "" {
		arguments = append(arguments, "--switch-handoff", handoffPath)
	}

	if paneID != "" {
		arguments = append(arguments, "--target-pane", paneID)
	}

	if sessionID != "" {
		arguments = append(arguments, "--current-session-id", sessionID)
	}

	for _, flag := range []string{"config", "repository", "lease-root", "history", "agent-state"} {
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

func popupDimensions(cfg *config.Config, state *session.State, available gotmux.Size) (string, string) {
	agentsMode := effectivePickerPopupMode(cfg, state) == "agents"
	width, height := cfg.Popup.SeshWidth, cfg.Popup.SeshHeight

	if agentsMode {
		width, height = cfg.Popup.AgentsWidth, cfg.Popup.AgentsHeight
	}

	useAgentsBounds := agentsMode
	for _, mode := range cfg.Settings.DisplayModes {
		useAgentsBounds = useAgentsBounds || strings.EqualFold(strings.TrimSpace(mode), "agents")
	}

	minimum := gotmux.Size{Width: popupSeshMinWidth, Height: popupSeshMinHeight}
	fallback := gotmux.Size{Width: popupSeshDefaultWidth, Height: popupSeshDefaultHeight}

	if useAgentsBounds {
		minimum = gotmux.Size{Width: popupAgentsMinWidth, Height: popupAgentsMinHeight}
		fallback = gotmux.Size{Width: popupAgentsDefaultWidth, Height: popupAgentsDefaultHeight}
	}

	return adaptivePickerPopupDimensions(width, height, available, minimum, fallback)
}

func effectivePickerPopupMode(cfg *config.Config, state *session.State) string {
	selected := "sesh"
	if state != nil && strings.EqualFold(strings.TrimSpace(state.PopupDisplayMode), "agents") {
		selected = "agents"
	}

	for _, mode := range cfg.Settings.DisplayModes {
		if strings.EqualFold(strings.TrimSpace(mode), selected) {
			return selected
		}
	}

	if len(cfg.Settings.DisplayModes) > 0 && strings.EqualFold(strings.TrimSpace(cfg.Settings.DisplayModes[0]), "agents") {
		return "agents"
	}

	return "sesh"
}

func adaptivePickerPopupDimensions(width, height string, available, minimum, fallback gotmux.Size) (string, string) {
	width = strings.TrimSpace(width)
	height = strings.TrimSpace(height)

	if available.Width <= 0 || available.Height <= 0 {
		if popupDimensionIsDynamic(width) {
			width = strconv.Itoa(fallback.Width)
		}

		if popupDimensionIsDynamic(height) {
			height = strconv.Itoa(fallback.Height)
		}

		return width, height
	}

	maximum := gotmux.Size{
		Width:  max(available.Width-popupTerminalMargin, 1),
		Height: max(available.Height-popupTerminalMargin, 1),
	}
	desired := gotmux.Size{
		Width:  desiredPickerPopupDimension(width, available.Width, minimum.Width, fallback.Width, maximum.Width),
		Height: desiredPickerPopupDimension(height, available.Height, minimum.Height, fallback.Height, maximum.Height),
	}
	balanced := balancePickerPopupSize(desired, maximum, minimum, fallback)

	return strconv.Itoa(balanced.Width), strconv.Itoa(balanced.Height)
}

func popupDimensionIsDynamic(value string) bool {
	return value == "" || strings.EqualFold(value, "auto") || strings.EqualFold(value, "dynamic")
}

func desiredPickerPopupDimension(configured string, available, minimum, fallback, maximum int) int {
	desired := fallback

	if !popupDimensionIsDynamic(configured) {
		parsed, err := strconv.Atoi(strings.TrimSuffix(configured, "%"))
		if err == nil && parsed > 0 {
			desired = parsed
			if strings.HasSuffix(configured, "%") {
				desired = max(max(available*parsed/popupPercentBase, 1), min(fallback, maximum))
			}
		}
	}

	return min(max(desired, min(minimum, maximum)), maximum)
}

func balancePickerPopupSize(desired, maximum, minimum, target gotmux.Size) gotmux.Size {
	targetRatio := float64(target.Width) / float64(target.Height)
	desiredRatio := float64(desired.Width) / float64(desired.Height)

	if desiredRatio >= targetRatio {
		return desired
	}

	area := float64(desired.Width * desired.Height)
	balancedWidth := math.Sqrt(area * targetRatio)
	balancedHeight := balancedWidth / targetRatio
	scale := min(1.0, min(float64(maximum.Width)/balancedWidth, float64(maximum.Height)/balancedHeight))
	width := int(math.Round(min(max(balancedWidth*scale, 1), float64(maximum.Width))))
	height := int(math.Round(min(max(balancedHeight*scale, 1), float64(maximum.Height))))
	width = max(width, min(minimum.Width, maximum.Width))
	height = max(height, min(minimum.Height, maximum.Height))

	return gotmux.Size{Width: min(width, maximum.Width), Height: min(height, maximum.Height)}
}

func buildPickerModel(
	ctx context.Context,
	presentation presentation,
	cfg *config.Config,
	state *session.State,
	services tui.Services,
) (*tui.Model, error) {
	switch presentation {
	case presentationPopup:
		model, err := tui.NewPopupModel(ctx, cfg, state, services)

		return model, wrapPickerModelError(err)
	case presentationInline:
		model, err := tui.NewInlineModel(ctx, cfg, state, services)

		return model, wrapPickerModelError(err)
	case presentationFullscreen:
		model, err := tui.NewFullscreenModel(ctx, cfg, state, services)

		return model, wrapPickerModelError(err)
	default:
		return nil, wrapPickerModelError(errUnknownPickerPresentation)
	}
}

func wrapPickerModelError(err error) error {
	if err == nil {
		return nil
	}

	return oops.In("cli").With("operation", "initialize_picker_model").Wrap(err)
}

func runPickerProgram(ctx context.Context, cmd *cobra.Command, model *tui.Model, presentation presentation) (*tui.Model, error) {
	output := programOutput(cmd.OutOrStdout())

	programOptions := []tea.ProgramOption{
		tea.WithContext(ctx), tea.WithInput(cmd.InOrStdin()),
		tea.WithOutput(output), tea.WithoutSignalHandler(),
	}
	if presentation == presentationPopup {
		programOptions = append(programOptions, tea.WithFPS(renderFPS))
	}

	if profile, ok := colorProfile(os.Environ()); ok {
		programOptions = append(programOptions, tea.WithColorProfile(profile))
	}

	if output, ok := cmd.OutOrStdout().(fileDescriptor); ok {
		if width, height, err := term.GetSize(output.Fd()); err == nil {
			model.SetSize(width, height)
			programOptions = append(programOptions, tea.WithWindowSize(width, height))
		}
	}

	program := tea.NewProgram(model, programOptions...)

	final, err := program.Run()

	model.Close()

	if typed, ok := final.(*tui.Model); ok {
		return typed, wrapPickerProgramError(err)
	}

	return model, wrapPickerProgramError(err)
}

func programOutput(output io.Writer) io.Writer {
	if _, ok := output.(term.File); ok {
		return output
	}

	terminal, ok := output.(fileDescriptor)
	if !ok || !term.IsTerminal(terminal.Fd()) {
		return output
	}

	return terminalOutput{Writer: output, fd: terminal.Fd()}
}

// colorProfile returns the profile inside tmux, and false outside it. tmux converts RGB per
// client, so TrueColor is safe and skips bubbletea running `tmux info` before the first frame.
func colorProfile(environ []string) (colorprofile.Profile, bool) {
	inside := slices.ContainsFunc(environ, func(entry string) bool {
		value, ok := strings.CutPrefix(entry, "TMUX=")

		return ok && value != ""
	})
	if !inside {
		return colorprofile.Unknown, false
	}

	if profile := colorprofile.Env(environ); profile < colorprofile.ANSI {
		return profile, true
	}

	return colorprofile.TrueColor, true
}

func wrapPickerProgramError(err error) error {
	if err == nil {
		return nil
	}

	return oops.In("cli").With("operation", "execute_picker_program").Wrap(err)
}

func runPickerDeferred(
	ctx context.Context,
	stdin io.Reader,
	stdout, stderr io.Writer,
	model *tui.Model,
	services *services,
) error {
	if model == nil || services == nil {
		return nil
	}

	if target, ok := model.DeferredSwitchTarget(); ok {
		return services.performDeferredSwitch(ctx, target, model.DeferredSwitchPane())
	}

	if target, ok := model.DeferredAttachTarget(); ok {
		return services.performDeferredAttach(ctx, target)
	}

	if target, paneID, ok := model.DeferredRemoteAttach(); ok {
		if services.remote.Attach == nil {
			return oops.In("cli").With("operation", "prepare_remote_attach").Wrap(errRemoteAttachUnavailable)
		}

		command, err := services.remote.Attach.AttachRemoteSession(ctx, target, paneID)
		if err != nil {
			return oops.In("cli").With("operation", "prepare_remote_attach").Wrap(err)
		}

		if command == nil {
			return oops.In("cli").With("operation", "prepare_remote_attach").Wrap(errRemoteAttachCommandUnavailable)
		}

		command.SetStdin(stdin)
		command.SetStdout(stdout)
		command.SetStderr(stderr)

		if err := command.Run(); err != nil {
			return oops.In("cli").With("operation", "run_remote_attach").Wrap(err)
		}

		return nil
	}

	if model.DeferredOriginDetach() {
		return services.detachInvokingClient(ctx)
	}

	return nil
}

func invokingPickerClient(ctx context.Context, server *gotmux.Server) (gotmux.ClientInfo, error) {
	current, err := server.CurrentFrom(ctx, gotmux.TmuxVars{TMUX: os.Getenv("TMUX"), TMUXPane: os.Getenv("TMUX_PANE")})
	if err != nil {
		return gotmux.ClientInfo{}, remoteCLIError("resolve_invoking_picker_session", err)
	}

	currentSession, present := current.Session.Get()
	if !present {
		return gotmux.ClientInfo{}, session.ErrInvokingClientUnavailable
	}

	clients, err := currentSession.Handle().Clients(ctx)
	if err != nil {
		return gotmux.ClientInfo{}, remoteCLIError("resolve_invoking_picker_clients", err)
	}

	var eligible []gotmux.ClientInfo

	for _, client := range clients {
		if !client.Control {
			eligible = append(eligible, client)
		}
	}

	if name := os.Getenv(clientEnvironment); name != "" {
		for _, client := range eligible {
			if string(client.Name) == name {
				return client, nil
			}
		}

		return gotmux.ClientInfo{}, session.ErrInvokingClientUnavailable
	}

	if len(eligible) != 1 {
		return gotmux.ClientInfo{}, session.ErrInvokingClientUnavailable
	}

	return eligible[0], nil
}

func (r *transcriptNameCache) annotateInventory(ctx context.Context, summaries []session.AgentSummary) {
	r.mu.Lock()
	defer r.mu.Unlock()

	loads := sessionNameLoads(summaries, r.cache)
	if len(loads) != 0 {
		readPickerSessionNames(ctx, loads)
	}

	r.cache = make(map[string]sessionNameState, len(loads))
	for _, load := range loads {
		r.cache[load.path] = load.state
		if !load.state.hasInfo {
			continue
		}

		for _, agent := range load.agents {
			agent.HarnessSession = load.state.name
			agent.HarnessSession = harnessSessionLabel(*agent)
		}
	}
}

func sessionNameLoads(
	summaries []session.AgentSummary,
	cache map[string]sessionNameState,
) []sessionNameLoad {
	loads := make([]sessionNameLoad, 0)
	byPath := make(map[string]int)

	for _, summary := range summaries {
		for index := range summary.Agents {
			agent := &summary.Agents[index]
			if agent.Harness != string(aht.HarnessPi) || strings.TrimSpace(agent.SessionPath) == "" {
				continue
			}

			position, found := byPath[agent.SessionPath]
			if !found {
				if len(loads) == nameCacheLimit {
					continue
				}

				position = len(loads)
				byPath[agent.SessionPath] = position
				loads = append(loads, sessionNameLoad{
					path: agent.SessionPath, state: cache[agent.SessionPath], agents: nil,
				})
			}

			loads[position].agents = append(loads[position].agents, agent)
		}
	}

	return loads
}

func readPickerSessionNames(ctx context.Context, loads []sessionNameLoad) {
	limit := min(nameFileReadLimit, nameRefreshReadLimit/len(loads))

	var readers errgroup.Group
	readers.SetLimit(nameReaders)

	for index := range loads {
		if ctx.Err() != nil {
			break
		}

		readers.Go(func() error {
			loads[index].state = readPickerSessionName(ctx, loads[index].path, loads[index].state, int64(limit))

			return nil
		})
	}

	_ = readers.Wait()
}

func readPickerSessionName(
	ctx context.Context,
	path string,
	state sessionNameState,
	limit int64,
) sessionNameState {
	if ctx.Err() != nil {
		return state
	}

	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return state
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return state
	}

	if sessionNameReset(state, info) {
		state = sessionNameState{offset: 0, name: "", hasInfo: false, skipping: false, fileInfo: nil}
	}

	state.fileInfo = info
	remaining := info.Size() - state.offset
	length := min(remaining, limit)
	reader := io.NewSectionReader(file, state.offset, length)

	return decodePickerSessionName(ctx, reader, state, length == remaining)
}

func sessionNameReset(state sessionNameState, info os.FileInfo) bool {
	if state.fileInfo == nil {
		return false
	}

	return !os.SameFile(state.fileInfo, info) || info.Size() < state.fileInfo.Size() ||
		(info.Size() == state.fileInfo.Size() && !info.ModTime().Equal(state.fileInfo.ModTime()))
}

func decodePickerSessionName(
	ctx context.Context,
	input io.Reader,
	state sessionNameState,
	atFileEnd bool,
) sessionNameState {
	reader := bufio.NewReaderSize(input, nameLineLimit)

	for ctx.Err() == nil {
		line, err := reader.ReadSlice('\n')
		if len(line) == 0 || ctx.Err() != nil {
			break
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			state.skipping = true
			state.offset += int64(len(line))

			continue
		}

		if err != nil && !errors.Is(err, io.EOF) {
			break
		}

		if !state.skipping && !sessionNameLineComplete(line, atFileEnd) {
			break
		}

		if !state.skipping {
			state = sessionNameEntry(line, state)
		}

		state.offset += int64(len(line))
		state.skipping = line[len(line)-1] != '\n' && state.skipping
	}

	return state
}

func sessionNameLineComplete(line []byte, atFileEnd bool) bool {
	return line[len(line)-1] == '\n' || (atFileEnd && json.Valid(line))
}

func sessionNameEntry(line []byte, state sessionNameState) sessionNameState {
	if !bytes.Contains(line, []byte(`"session_info"`)) {
		return state
	}

	var entry sessionInfo
	if json.Unmarshal(line, &entry) != nil || entry.Type != "session_info" {
		return state
	}

	state.hasInfo = true

	state.name = ""
	if entry.Name != nil {
		state.name = strings.TrimSpace(*entry.Name)
	}

	return state
}

func harnessSessionLabel(agent session.AgentSession) string {
	if label := strings.TrimSpace(agent.HarnessSession); label != "" {
		return label
	}

	if label := harnessSessionPathLabel(agent); label != "" {
		return label
	}

	if id := strings.TrimSpace(agent.RegistryID); id != "" {
		return "registry:" + id
	}

	return "unknown"
}

func harnessSessionPathLabel(agent session.AgentSession) string {
	path := strings.TrimSpace(agent.SessionPath)
	if path == "" {
		return ""
	}

	label := filepath.Base(path)
	if label == "." || label == string(filepath.Separator) {
		return ""
	}

	if agent.Harness == string(aht.HarnessPi) {
		if id := piSessionID(label); id != "" {
			return id
		}
	}

	return label
}

func piSessionID(filename string) string {
	const uuidFormat = "00000000-0000-0000-0000-000000000000"

	stem := strings.TrimSuffix(filename, filepath.Ext(filename))

	_, id, ok := strings.CutLast(stem, "_")
	if !ok {
		return ""
	}

	if len(id) != len(uuidFormat) {
		return ""
	}

	for index, char := range id {
		switch uuidFormat[index] {
		case '-':
			if char != '-' {
				return ""
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
				return ""
			}
		}
	}

	return id
}
