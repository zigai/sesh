//go:build linux || darwin

package cmdpicker

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
	"strconv"
	"strings"
	"sync"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/samber/oops"
	"github.com/spf13/cobra"
	"github.com/zigai/aht/v2/pkg/aht"
	gotmux "github.com/zigai/gotmux/tmux"
	"golang.org/x/sync/errgroup"

	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui"
)

const (
	pickerPopup pickerPresentation = iota
	pickerInline
	pickerStandalone
)

const (
	pickerAnnotationEnabled = "true"
	pickerClientEnvironment = "SESH_TMUX_CLIENT"
)

const (
	pickerPopupTerminalMargin      = 2
	pickerPopupSeshMinWidth        = 40
	pickerPopupSeshMinHeight       = 12
	pickerPopupSeshDefaultWidth    = 64
	pickerPopupSeshDefaultHeight   = 16
	pickerPopupAgentsMinWidth      = 60
	pickerPopupAgentsMinHeight     = 14
	pickerPopupAgentsDefaultWidth  = 80
	pickerPopupAgentsDefaultHeight = 20
	pickerPopupPercentBase         = 100
	pickerRenderFPS                = 120
)

const (
	pickerNameCacheLimit       = 128
	pickerNameReaders          = 8
	pickerNameLineLimit        = 64 * 1024
	pickerNameFileReadLimit    = 8 * 1024 * 1024
	pickerNameRefreshReadLimit = 64 * 1024 * 1024
)

var (
	errPickerTerminalRequired                   = errors.New("picker requires an interactive terminal")
	errUnknownPickerPresentation                = errors.New("unknown picker presentation")
	errRemoteAttachUnavailable                  = errors.New("remote attach is unavailable")
	errRemoteAttachCommandUnavailable           = errors.New("remote attach returned no terminal command")
	_                                 term.File = pickerTerminalOutput{Writer: nil, fd: 0}
)

var DefaultPickerRemoteFactory PickerRemoteFactory

type pickerPresentation uint8

type fileDescriptor interface {
	Fd() uintptr
}

type pickerCommandRuntime struct {
	cfg            *config.Config
	state          *session.State
	socketPath     string
	repositoryPath string
	leaseRoot      string
	historyPath    string
}

type pickerTerminalOutput struct {
	io.Writer

	fd uintptr
}

type pickerTranscriptNameCache struct {
	mu    sync.Mutex
	cache map[string]pickerSessionNameState
}

type pickerSessionNameState struct {
	offset   int64
	name     string
	hasInfo  bool
	skipping bool
	fileInfo os.FileInfo
}

type pickerSessionNameLoad struct {
	path   string
	state  pickerSessionNameState
	agents []*session.AgentSession
}

type pickerSessionInfo struct {
	Type string  `json:"type"`
	Name *string `json:"name"`
}

func (o pickerTerminalOutput) Read([]byte) (int, error) { return 0, io.EOF }

func (o pickerTerminalOutput) Close() error { return nil }

func (o pickerTerminalOutput) Fd() uintptr { return o.fd }

func InvokingPickerClient(ctx context.Context, server *gotmux.Server) (gotmux.ClientInfo, error) {
	return invokingPickerClient(ctx, server)
}

func NewPopupCommand(ctx context.Context, remote ...PickerRemoteFactory) *cobra.Command {
	return newPickerCommand(ctx, "popup", "Open the framed session picker in the current terminal", pickerPopup, firstPickerRemoteFactory(remote))
}

func NewInlinePickerCommand(ctx context.Context, remote ...PickerRemoteFactory) *cobra.Command {
	return newPickerCommand(ctx, "inline", "Open the interactive session picker in the current pane", pickerInline, firstPickerRemoteFactory(remote))
}

func NewStandalonePickerCommand(ctx context.Context, remote ...PickerRemoteFactory) *cobra.Command {
	return newPickerCommand(ctx, "standalone", "Open the interactive session picker in this terminal", pickerStandalone, firstPickerRemoteFactory(remote))
}

func PickerHasTerminal(cmd *cobra.Command) bool {
	return pickerHasTerminal(cmd)
}

func PickerProgramOutput(output io.Writer) io.Writer {
	return pickerProgramOutput(output)
}

func firstPickerRemoteFactory(values []PickerRemoteFactory) PickerRemoteFactory {
	if len(values) == 0 {
		return DefaultPickerRemoteFactory
	}

	return values[0]
}

func newPickerCommand(
	ctx context.Context,
	use, short string,
	presentation pickerPresentation,
	remoteFactory PickerRemoteFactory,
) *cobra.Command {
	cmd := &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        clibase.UsageArgs(cobra.NoArgs),
		Annotations: map[string]string{clibase.AnnotationLiveIO: pickerAnnotationEnabled, clibase.AnnotationOperation: "picker"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPickerCommand(pickerExecutionContext(ctx, cmd), cmd, presentation, remoteFactory)
		},
	}

	cmd.Flags().String("socket", "", "Explicit absolute path to the tmux server socket (defaults to the ambient server)")
	cmd.Flags().String("repository", "", "Native snapshot repository (defaults to the configured data directory)")
	cmd.Flags().String("lease-root", "", "Shared tmux endpoint lease root (defaults to the configured data directory)")
	cmd.Flags().String("history", "", "Session history state file")
	cmd.Flags().String("agent-state", "", "AHT agent registry path")

	if presentation == pickerPopup {
		cmd.Flags().Bool("tmux", false, "open using the configured size in a tmux popup")
		cmd.Flags().String("switch-handoff", "", "popup switch handoff")
		_ = cmd.Flags().MarkHidden("switch-handoff")
		cmd.Flags().StringP("client", "c", "", "target tmux client (name or tty)")
		cmd.Flags().StringP("target-pane", "t", "", "target tmux pane")
		cmd.Flags().String("current-session-id", "", "invoking tmux session ID")
		cmd.Flags().IntP("client-width", "w", 0, "client width override")
		cmd.Flags().IntP("client-height", "H", 0, "client height override")
	}

	return cmd
}

func pickerExecutionContext(parent context.Context, cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}

	return parent
}

func getBoolFlag(cmd *cobra.Command, name string) bool {
	if cmd == nil || cmd.Flags().Lookup(name) == nil {
		return false
	}

	value, _ := cmd.Flags().GetBool(name)

	return value
}

func commandTerminalStreams(cmd *cobra.Command) gotmux.TerminalStreams {
	var streams gotmux.TerminalStreams

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

func runPickerCommand(
	ctx context.Context,
	cmd *cobra.Command,
	presentation pickerPresentation,
	remoteFactory PickerRemoteFactory,
) error {
	runtime, err := preparePickerCommand(ctx, cmd)
	if err != nil {
		return err
	}

	if presentation == pickerPopup && getBoolFlag(cmd, "tmux") {
		return openPickerPopup(ctx, cmd, runtime)
	}

	if !pickerHasTerminal(cmd) {
		return clibase.NewExitError(clibase.ExitCodeUsage, errPickerTerminalRequired)
	}

	return runPickerInteractive(ctx, cmd, presentation, runtime, remoteFactory)
}

func runPickerInteractive(
	ctx context.Context,
	cmd *cobra.Command,
	presentation pickerPresentation,
	runtime pickerCommandRuntime,
	remoteFactory PickerRemoteFactory,
) error {
	remote, err := configurePickerRemote(ctx, runtime.cfg, runtime.socketPath, remoteFactory)
	if err != nil {
		return err
	}

	agentStatePath, _ := cmd.Flags().GetString("agent-state")

	services, err := newPickerServices(
		ctx, runtime.cfg, runtime.socketPath, runtime.repositoryPath, runtime.leaseRoot,
		runtime.historyPath, agentStatePath, commandTerminalStreams(cmd), remote,
	)
	if err != nil {
		return err
	}

	services.currentPane = pickerCurrentPane(cmd)
	if flag := cmd.Flags().Lookup("switch-handoff"); flag != nil {
		path, _ := cmd.Flags().GetString("switch-handoff")
		services.popupChild = path != ""
	}

	if flag := cmd.Flags().Lookup("current-session-id"); flag != nil {
		services.currentSessionID, _ = cmd.Flags().GetString("current-session-id")
	}

	model, err := buildPickerModel(ctx, presentation, runtime.cfg, runtime.state, services.TUIServices())
	if err != nil {
		return errors.Join(oops.In("cli").With("operation", "construct_picker").Wrap(err), services.Close())
	}

	finalModel, runErr := runPickerProgram(ctx, cmd, model, presentation)

	return finishPickerInteractive(ctx, cmd, finalModel, services, runErr)
}

func finishPickerInteractive(ctx context.Context, cmd *cobra.Command, finalModel *tui.Model, services *pickerServices, runErr error) error {
	var deferredErr error

	var handoffTarget *session.Session

	var handoffPane, handoffPath string

	if runErr == nil && ctx.Err() == nil {
		if flag := cmd.Flags().Lookup("switch-handoff"); flag != nil {
			handoffPath, _ = cmd.Flags().GetString("switch-handoff")
		}

		if target, ok := finalModel.DeferredSwitchTarget(); ok && handoffPath != "" {
			handoffTarget = &target
			handoffPane = finalModel.DeferredSwitchPane()
		} else {
			deferredErr = runPickerDeferred(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), finalModel, services)
		}
	}

	closeErr := services.Close()
	if handoffTarget != nil && closeErr == nil {
		deferredErr = writePickerSwitchHandoff(handoffPath, *handoffTarget, handoffPane)
	}

	if runErr != nil {
		runErr = oops.In("cli").With("operation", "run_picker").Wrap(runErr)
	}

	return errors.Join(runErr, deferredErr, closeErr)
}

func preparePickerCommand(ctx context.Context, cmd *cobra.Command) (pickerCommandRuntime, error) {
	cfg, err := clibase.LoadCommandConfig(ctx, cmd)
	if err != nil {
		return pickerCommandRuntime{}, err
	}

	socketPath := resolvePickerSocket(cmd)
	if socketPath == "" {
		return pickerCommandRuntime{}, clibase.NewExitError(clibase.ExitCodeUsage, clibase.ErrSocketRequired)
	}

	repositoryFlag, _ := cmd.Flags().GetString("repository")
	leaseFlag, _ := cmd.Flags().GetString("lease-root")
	historyFlag, _ := cmd.Flags().GetString("history")

	repositoryPath, leaseRoot, historyPath, err := pickerPaths(cfg, repositoryFlag, leaseFlag, historyFlag)
	if err != nil {
		return pickerCommandRuntime{}, oops.In("cli").With("operation", "resolve_picker_paths").Wrap(err)
	}

	state, err := session.LoadState(historyPath)
	if err != nil {
		return pickerCommandRuntime{}, oops.In("cli").With("operation", "load_history").Wrap(err)
	}

	return pickerCommandRuntime{
		cfg: cfg, state: state, socketPath: socketPath, repositoryPath: repositoryPath,
		leaseRoot: leaseRoot, historyPath: historyPath,
	}, nil
}

func configurePickerRemote(
	ctx context.Context,
	cfg *config.Config,
	socketPath string,
	remoteFactory PickerRemoteFactory,
) (PickerRemoteServices, error) {
	if remoteFactory == nil {
		return PickerRemoteServices{
			Catalog: nil, Watcher: nil, Actions: nil, Attach: nil, OriginBridge: nil,
		}, nil
	}

	remote, err := remoteFactory(ctx, cfg, socketPath)
	if err != nil {
		return PickerRemoteServices{}, oops.In("cli").With("operation", "configure_remote_picker").Wrap(err)
	}

	return remote, nil
}

func pickerHasTerminal(cmd *cobra.Command) bool {
	input, inputOK := cmd.InOrStdin().(fileDescriptor)
	output, outputOK := cmd.OutOrStdout().(fileDescriptor)

	return inputOK && outputOK && term.IsTerminal(input.Fd()) && term.IsTerminal(output.Fd())
}

func pickerCurrentPane(cmd *cobra.Command) string {
	if flag := cmd.Flags().Lookup("target-pane"); flag != nil {
		pane, _ := cmd.Flags().GetString("target-pane")
		if pane != "" {
			return pane
		}
	}

	return os.Getenv("TMUX_PANE")
}

func applyClientDimensionOverrides(cmd *cobra.Command, client *gotmux.ClientInfo) {
	if w, _ := cmd.Flags().GetInt("client-width"); w > 0 {
		client.Width = w
	}

	if h, _ := cmd.Flags().GetInt("client-height"); h > 0 {
		client.Height = h
	}
}

func resolvePopupClient(ctx context.Context, cmd *cobra.Command, server *gotmux.Server) (gotmux.ClientInfo, error) {
	targetClient, _ := cmd.Flags().GetString("client")
	if targetClient == "" {
		targetClient = os.Getenv(pickerClientEnvironment)
	}

	clients, err := server.Clients(ctx)
	if err != nil {
		return gotmux.ClientInfo{}, remoteCLIError("list_picker_clients", err)
	}

	if targetClient != "" {
		for _, c := range clients {
			if string(c.Name) == targetClient && !c.Control {
				applyClientDimensionOverrides(cmd, &c)

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
		applyClientDimensionOverrides(cmd, &eligible[0])

		return eligible[0], nil
	}

	invoked, err := invokingPickerClient(ctx, server)
	if err != nil {
		return gotmux.ClientInfo{}, err
	}

	applyClientDimensionOverrides(cmd, &invoked)

	return invoked, nil
}

func openPickerPopup(ctx context.Context, cmd *cobra.Command, runtime pickerCommandRuntime) error {
	server, err := integration.OpenInteractive(runtime.socketPath)
	if err != nil {
		return oops.In("cli").With("operation", "open_tmux").Wrap(err)
	}

	executable, err := os.Executable()
	if err != nil {
		return oops.In("cli").With("operation", "resolve_executable").Wrap(err)
	}

	client, err := resolvePopupClient(ctx, cmd, server)
	if err != nil {
		return oops.In("cli").With("operation", "resolve_popup_client").Wrap(err)
	}

	originSessionID, present := client.SessionID.Get()
	if !present || originSessionID == "" {
		return session.ErrInvokingClientUnavailable
	}

	if expected, _ := cmd.Flags().GetString("current-session-id"); expected != "" && expected != string(originSessionID) {
		return session.ErrInvokingClientUnavailable
	}

	handoffPath, cleanup, err := createPickerSwitchHandoff()
	if err != nil {
		return err
	}
	defer cleanup()

	targetPane := pickerCurrentPane(cmd)
	width, height := pickerPopupDimensions(runtime.cfg, runtime.state, gotmux.Size{Width: client.Width, Height: client.Height})
	options := pickerPopupOptions(
		gotmux.Exec(executable, pickerPopupArguments(cmd, runtime.socketPath, string(client.Name), string(originSessionID), targetPane, handoffPath)...), width, height,
	)

	services, err := newPickerServices(ctx, runtime.cfg, runtime.socketPath, runtime.repositoryPath,
		runtime.leaseRoot, runtime.historyPath, "", gotmux.TerminalStreams{In: nil, Out: nil, Err: nil},
		PickerRemoteServices{Catalog: nil, Watcher: nil, Actions: nil, Attach: nil, OriginBridge: nil})
	if err != nil {
		return err
	}

	if err := client.Handle().Popup(ctx, options); err != nil {
		return errors.Join(oops.In("cli").With("operation", "open_picker_popup").Wrap(err), services.Close())
	}

	return errors.Join(
		completePickerSwitchHandoff(ctx, services, handoffPath, string(client.Name), string(originSessionID)),
		services.Close(),
	)
}

func pickerPopupOptions(program gotmux.Program, width, height string) gotmux.PopupOptions {
	var opts gotmux.PopupOptions

	opts.Program = program
	opts.Width = width
	opts.Height = height
	opts.Borderless = true
	opts.Style = "bg=#000000"
	opts.X = "C"
	opts.Y = "C"
	opts.CloseOnExit = true

	return opts
}

func pickerPopupArguments(cmd *cobra.Command, socketPath, clientName, sessionID, paneID, handoffPath string) []string {
	arguments := []string{"popup", "--socket", socketPath}

	if handoffPath != "" {
		arguments = append(arguments, "--switch-handoff", handoffPath)
	}

	if clientName != "" {
		arguments = append(arguments, "--client", clientName)
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

func pickerPopupDimensions(cfg *config.Config, state *session.State, available gotmux.Size) (string, string) {
	agentsMode := effectivePickerPopupMode(cfg, state) == "agents"
	width, height := cfg.Popup.SeshWidth, cfg.Popup.SeshHeight

	if agentsMode {
		width, height = cfg.Popup.AgentsWidth, cfg.Popup.AgentsHeight
	}

	useAgentsBounds := agentsMode
	for _, mode := range cfg.Settings.DisplayModes {
		useAgentsBounds = useAgentsBounds || strings.EqualFold(strings.TrimSpace(mode), "agents")
	}

	minimum := gotmux.Size{Width: pickerPopupSeshMinWidth, Height: pickerPopupSeshMinHeight}
	fallback := gotmux.Size{Width: pickerPopupSeshDefaultWidth, Height: pickerPopupSeshDefaultHeight}

	if useAgentsBounds {
		minimum = gotmux.Size{Width: pickerPopupAgentsMinWidth, Height: pickerPopupAgentsMinHeight}
		fallback = gotmux.Size{Width: pickerPopupAgentsDefaultWidth, Height: pickerPopupAgentsDefaultHeight}
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
		if pickerPopupDimensionIsDynamic(width) {
			width = strconv.Itoa(fallback.Width)
		}

		if pickerPopupDimensionIsDynamic(height) {
			height = strconv.Itoa(fallback.Height)
		}

		return width, height
	}

	maximum := gotmux.Size{
		Width:  max(available.Width-pickerPopupTerminalMargin, 1),
		Height: max(available.Height-pickerPopupTerminalMargin, 1),
	}
	desired := gotmux.Size{
		Width:  desiredPickerPopupDimension(width, available.Width, minimum.Width, fallback.Width, maximum.Width),
		Height: desiredPickerPopupDimension(height, available.Height, minimum.Height, fallback.Height, maximum.Height),
	}
	balanced := balancePickerPopupSize(desired, maximum, minimum, fallback)

	return strconv.Itoa(balanced.Width), strconv.Itoa(balanced.Height)
}

func pickerPopupDimensionIsDynamic(value string) bool {
	return value == "" || strings.EqualFold(value, "auto") || strings.EqualFold(value, "dynamic")
}

func desiredPickerPopupDimension(configured string, available, minimum, fallback, maximum int) int {
	desired := fallback

	if !pickerPopupDimensionIsDynamic(configured) {
		parsed, err := strconv.Atoi(strings.TrimSuffix(configured, "%"))
		if err == nil && parsed > 0 {
			desired = parsed
			if strings.HasSuffix(configured, "%") {
				desired = max(max(available*parsed/pickerPopupPercentBase, 1), min(fallback, maximum))
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

func resolvePickerSocket(cmd *cobra.Command) string {
	socketPath, _ := cmd.Flags().GetString("socket")
	if socketPath != "" {
		return socketPath
	}

	tmuxValue := os.Getenv("TMUX")
	if tmuxValue == "" {
		return ""
	}

	socketPath, _, _ = strings.Cut(tmuxValue, ",")

	return socketPath
}

func buildPickerModel(
	ctx context.Context,
	presentation pickerPresentation,
	cfg *config.Config,
	state *session.State,
	services tui.Services,
) (*tui.Model, error) {
	switch presentation {
	case pickerPopup:
		model, err := tui.NewPopupModel(ctx, cfg, state, services)

		return model, wrapPickerModelError(err)
	case pickerInline:
		model, err := tui.NewInlineModel(ctx, cfg, state, services)

		return model, wrapPickerModelError(err)
	case pickerStandalone:
		model, err := tui.NewStandaloneModel(ctx, cfg, state, services)

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

func runPickerProgram(ctx context.Context, cmd *cobra.Command, model *tui.Model, presentation pickerPresentation) (*tui.Model, error) {
	output := pickerProgramOutput(cmd.OutOrStdout())

	programOptions := []tea.ProgramOption{
		tea.WithContext(ctx), tea.WithInput(cmd.InOrStdin()),
		tea.WithOutput(output), tea.WithoutSignalHandler(),
	}
	if presentation == pickerPopup {
		programOptions = append(programOptions, tea.WithFPS(pickerRenderFPS))
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

func pickerProgramOutput(output io.Writer) io.Writer {
	if _, ok := output.(term.File); ok {
		return output
	}

	terminal, ok := output.(fileDescriptor)
	if !ok || !term.IsTerminal(terminal.Fd()) {
		return output
	}

	return pickerTerminalOutput{Writer: output, fd: terminal.Fd()}
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
	services *pickerServices,
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
	current, err := server.CurrentWithEnv(ctx, gotmux.Environment{TMUX: os.Getenv("TMUX"), TMUXPane: os.Getenv("TMUX_PANE")})
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

	if name := os.Getenv(pickerClientEnvironment); name != "" {
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

func (r *pickerTranscriptNameCache) annotateInventory(ctx context.Context, summaries []session.AgentSummary) {
	r.mu.Lock()
	defer r.mu.Unlock()

	loads := pickerSessionNameLoads(summaries, r.cache)
	if len(loads) != 0 {
		readPickerSessionNames(ctx, loads)
	}

	r.cache = make(map[string]pickerSessionNameState, len(loads))
	for _, load := range loads {
		r.cache[load.path] = load.state
		if !load.state.hasInfo {
			continue
		}

		for _, agent := range load.agents {
			agent.HarnessSession = load.state.name
			agent.HarnessSession = pickerHarnessSessionLabel(*agent)
		}
	}
}

func pickerSessionNameLoads(
	summaries []session.AgentSummary,
	cache map[string]pickerSessionNameState,
) []pickerSessionNameLoad {
	loads := make([]pickerSessionNameLoad, 0)
	byPath := make(map[string]int)

	for _, summary := range summaries {
		for index := range summary.Agents {
			agent := &summary.Agents[index]
			if agent.Harness != string(aht.HarnessPi) || strings.TrimSpace(agent.SessionPath) == "" {
				continue
			}

			position, found := byPath[agent.SessionPath]
			if !found {
				if len(loads) == pickerNameCacheLimit {
					continue
				}

				position = len(loads)
				byPath[agent.SessionPath] = position
				loads = append(loads, pickerSessionNameLoad{
					path: agent.SessionPath, state: cache[agent.SessionPath], agents: nil,
				})
			}

			loads[position].agents = append(loads[position].agents, agent)
		}
	}

	return loads
}

func readPickerSessionNames(ctx context.Context, loads []pickerSessionNameLoad) {
	limit := min(pickerNameFileReadLimit, pickerNameRefreshReadLimit/len(loads))

	var readers errgroup.Group
	readers.SetLimit(pickerNameReaders)

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
	state pickerSessionNameState,
	limit int64,
) pickerSessionNameState {
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

	if pickerSessionNameReset(state, info) {
		state = pickerSessionNameState{offset: 0, name: "", hasInfo: false, skipping: false, fileInfo: nil}
	}

	state.fileInfo = info
	remaining := info.Size() - state.offset
	length := min(remaining, limit)
	reader := io.NewSectionReader(file, state.offset, length)

	return decodePickerSessionName(ctx, reader, state, length == remaining)
}

func pickerSessionNameReset(state pickerSessionNameState, info os.FileInfo) bool {
	if state.fileInfo == nil {
		return false
	}

	return !os.SameFile(state.fileInfo, info) || info.Size() < state.fileInfo.Size() ||
		(info.Size() == state.fileInfo.Size() && !info.ModTime().Equal(state.fileInfo.ModTime()))
}

func decodePickerSessionName(
	ctx context.Context,
	input io.Reader,
	state pickerSessionNameState,
	atFileEnd bool,
) pickerSessionNameState {
	reader := bufio.NewReaderSize(input, pickerNameLineLimit)

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

		if !state.skipping && !pickerSessionNameLineComplete(line, atFileEnd) {
			break
		}

		if !state.skipping {
			state = pickerSessionNameEntry(line, state)
		}

		state.offset += int64(len(line))
		state.skipping = line[len(line)-1] != '\n' && state.skipping
	}

	return state
}

func pickerSessionNameLineComplete(line []byte, atFileEnd bool) bool {
	return line[len(line)-1] == '\n' || (atFileEnd && json.Valid(line))
}

func pickerSessionNameEntry(line []byte, state pickerSessionNameState) pickerSessionNameState {
	if !bytes.Contains(line, []byte(`"session_info"`)) {
		return state
	}

	var entry pickerSessionInfo
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

func pickerHarnessSessionLabel(agent session.AgentSession) string {
	if label := strings.TrimSpace(agent.HarnessSession); label != "" {
		return label
	}

	if label := pickerHarnessSessionPathLabel(agent); label != "" {
		return label
	}

	if id := strings.TrimSpace(agent.RegistryID); id != "" {
		return "registry:" + id
	}

	return "unknown"
}

func pickerHarnessSessionPathLabel(agent session.AgentSession) string {
	path := strings.TrimSpace(agent.SessionPath)
	if path == "" {
		return ""
	}

	label := filepath.Base(path)
	if label == "." || label == string(filepath.Separator) {
		return ""
	}

	if agent.Harness == string(aht.HarnessPi) {
		if id := pickerPiSessionID(label); id != "" {
			return id
		}
	}

	return label
}

func pickerPiSessionID(filename string) string {
	const uuidFormat = "00000000-0000-0000-0000-000000000000"

	stem := strings.TrimSuffix(filename, filepath.Ext(filename))

	separator := strings.LastIndexByte(stem, '_')
	if separator < 0 {
		return ""
	}

	id := stem[separator+1:]
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
