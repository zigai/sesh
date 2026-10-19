//go:build linux || darwin

// Package actioncli builds command-line commands from action definitions. The command path is
// the action ID: session.kill runs as `sesh session kill`.
package actioncli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/remote"
	"github.com/zigai/sesh/internal/session"
)

const (
	ownedValue = "true"

	ConnectionFlag = "connection"

	// ClientFlag and the flags after it name the tmux client that invoked an action; key
	// bindings fill them from tmux formats.
	ClientFlag        = "client"
	PaneFlag          = "pane"
	ClientSessionFlag = "client-session"
	ClientWidthFlag   = "client-width"
	ClientHeightFlag  = "client-height"
)

var errPeerDisconnected = errors.New("remote peer is not connected")

// Options adapts one action's command to command-line conventions the definition does not
// describe, such as interactive selection or a detailed text summary. Frontend runs interactive
// actions, which only the command line can present.
type Options[In, Out any] struct {
	Operation    string
	SuccessOwned bool
	Configure    func(cmd *cobra.Command)
	Prepare      func(ctx context.Context, cmd *cobra.Command, values action.Values) (bool, error)
	Frontend     func(cmd *cobra.Command) action.Dispatcher
	Render       func(cmd *cobra.Command, output Out, err error) error
}

type Runtime struct {
	Local      *app.Local
	Repository *app.Repository
	Router     action.Router
}

// Command builds the command for an action. Positional arguments are the subject followed by
// positional parameters; other parameters become flags.
func Command[In, Out any](definition *action.Definition[In, Out], opts Options[In, Out]) *cobra.Command {
	operation := opts.Operation
	if operation == "" {
		operation = definition.ID.Verb()
	}

	cmd := &cobra.Command{
		Use:   definition.ID.Verb() + usageSuffix(definition.Descriptor),
		Short: definition.Summary,
		Long:  long(definition.Descriptor),
		Args:  clibase.UsageArgs(argumentCount(definition.Descriptor)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, args, definition, opts)
		},
		Annotations: map[string]string{clibase.AnnotationOperation: operation, clibase.AnnotationAction: string(definition.ID)},
	}

	if opts.SuccessOwned {
		cmd.Annotations[clibase.AnnotationSuccessOwned] = ownedValue
	}

	AddEnvironmentFlags(cmd)

	if definition.Remote != action.RemoteNever {
		cmd.Flags().String(ConnectionFlag, "", "Run on this configured remote connection instead of this machine")
	}

	if definition.NeedsClient {
		addCallerFlags(cmd)
	}

	for _, param := range definition.Params {
		addParamFlag(cmd, param)
	}

	clibase.AddJSONFlag(cmd)

	if opts.Configure != nil {
		opts.Configure(cmd)
	}

	return cmd
}

func AddEnvironmentFlags(cmd *cobra.Command) {
	clibase.AddSocketFlag(cmd)
	cmd.Flags().String("repository", "", clibase.RepositoryFlagUsage)
	cmd.Flags().String("lease-root", "", clibase.LeaseRootFlagUsage)
	cmd.Flags().String("history", "", clibase.HistoryFlagUsage)
	cmd.Flags().String("agent-state", "", "AHT agent registry file [default: agent_status.store_path setting, else AHT's default registry]")
}

func addCallerFlags(cmd *cobra.Command) {
	cmd.Flags().String(ClientFlag, "", "Invoking tmux client, by name or tty [default: the client of the invoking pane]")
	cmd.Flags().String(PaneFlag, "", "Invoking tmux pane ID, for example %1 [default: $TMUX_PANE]")
	cmd.Flags().String(ClientSessionFlag, "", "Session name or ID the invoking client is expected to show")
	cmd.Flags().String(ClientWidthFlag, "", "Invoking client width [default: the client's width]")
	cmd.Flags().String(ClientHeightFlag, "", "Invoking client height [default: the client's height]")
}

func CallerOf(cmd *cobra.Command) action.Caller {
	value := func(name string) string {
		if cmd.Flags().Lookup(name) == nil {
			return ""
		}

		text, _ := cmd.Flags().GetString(name)

		return text
	}

	return action.Caller{
		Client:  value(ClientFlag),
		Pane:    value(PaneFlag),
		Session: value(ClientSessionFlag),
		Width:   value(ClientWidthFlag),
		Height:  value(ClientHeightFlag),
	}
}

// OpenRuntime opens the tmux server and lazily opened repository selected by the environment
// flags.
func OpenRuntime(cmd *cobra.Command, cfg *config.Config) (*Runtime, error) {
	workspace, err := app.Resolve(cfg, clibase.CommandLocations(cmd))
	if err != nil {
		return nil, err
	}

	server, err := workspace.OpenTmux()
	if err != nil {
		return nil, err
	}

	repository := app.NewRepository(workspace.Repository)
	local := app.NewLocal(app.LocalOptions{
		Config:             workspace.Config,
		Server:             server,
		Repository:         repository,
		LeaseRoot:          workspace.LeaseRoot,
		HistoryPath:        workspace.History,
		AgentSource:        workspace.AgentSource(),
		Streams:            TerminalStreams(cmd),
		Source:             "cli",
		DeferSwitchHistory: false,
		TaskLedger:         workspace.Tasks,
		WorkState:          workspace.Work,
		WorktreeDir:        workspace.WorktreeDir,
	})

	return &Runtime{Local: local, Repository: repository, Router: action.Router{Local: local, Peer: nil, Origin: nil}}, nil
}

func TerminalStreams(cmd *cobra.Command) gotmux.Streams {
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

func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}

	return runtime.Repository.Close()
}

func run[In, Out any](cmd *cobra.Command, args []string, definition *action.Definition[In, Out], opts Options[In, Out]) error {
	ctx := clibase.CommandContext(cmd)

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	subjectArgs, values, err := commandValues(cmd, args, definition.Descriptor)
	if err != nil {
		return err
	}

	if opts.Prepare != nil {
		proceed, prepareErr := opts.Prepare(ctx, cmd, values)
		if prepareErr != nil || !proceed {
			return prepareErr
		}
	}

	output, ran, runErr := dispatch(ctx, cmd, cfg, definition, opts, subjectArgs, values)
	if !ran || (runErr != nil && reflect.ValueOf(&output).Elem().IsZero()) {
		return runErr
	}

	if opts.Render != nil {
		return opts.Render(cmd, output, runErr)
	}

	return Render(cmd, definition, output, runErr)
}

func dispatch[In, Out any](
	ctx context.Context,
	cmd *cobra.Command,
	cfg *config.Config,
	definition *action.Definition[In, Out],
	opts Options[In, Out],
	subjectArgs []string,
	values action.Values,
) (Out, bool, error) {
	if connection := Connection(cmd); connection != "" {
		return executeRemote(ctx, cfg, definition, connection, subjectArgs, values)
	}

	runtime, err := OpenRuntime(cmd, cfg)
	if err != nil {
		var zero Out

		return zero, false, err
	}

	if opts.Frontend != nil {
		runtime.Router.Local = action.Dispatchers{runtime.Local, opts.Frontend(cmd)}
	}

	output, ran, err := execute(ctx, runtime, definition, CallerOf(cmd), subjectArgs, values)

	return output, ran, errors.Join(err, runtime.Close())
}

func execute[In, Out any](
	ctx context.Context,
	runtime *Runtime,
	definition *action.Definition[In, Out],
	caller action.Caller,
	subjectArgs []string,
	values action.Values,
) (Out, bool, error) {
	var zero Out

	subject, err := runtime.Local.ResolveSubject(ctx, definition.Subject, values.String("generation"), subjectArgs)
	if err != nil {
		return zero, false, err
	}

	input, err := definition.Bind(subject, values)
	if err != nil {
		return zero, false, usageError(err)
	}

	output, err := action.Run(ctx, runtime.Router, definition, subject.At, caller, input)

	return output, true, err
}

func executeRemote[In, Out any](
	ctx context.Context,
	cfg *config.Config,
	definition *action.Definition[In, Out],
	connection string,
	subjectArgs []string,
	values action.Values,
) (Out, bool, error) {
	var zero Out

	if !cfg.Remote.Enabled {
		return zero, false, action.Wrap(action.CodeUnavailable, remote.ErrRemoteDisabled)
	}

	if _, err := remote.FindConnection(cfg.Remote, connection); err != nil {
		return zero, false, clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	cacheHome := config.GetXDGPaths().CacheHome

	catalog, err := remote.ReadCatalog(cacheHome)
	if err != nil {
		return zero, false, err
	}

	state, ok := catalog.Connections[connection]
	if !ok || !state.Connected {
		return zero, false, action.Wrap(action.CodeUnavailable, errPeerDisconnected)
	}

	peer := remote.NewCatalogPeer(cfg.Remote, cacheHome)
	peer.Observe(catalog)

	subject := action.Subject{At: action.Peer(connection), Sessions: nil, Pane: "", Task: ""}
	if !peer.Accepts(subject.At, definition.ID) {
		return zero, false, action.Wrap(action.CodeForbidden, remote.ErrActionNotOffered)
	}

	if definition.Subject != action.SubjectNone {
		sessions, err := peerSessions(remote.CatalogSessions(state), definition.Subject, subjectArgs)
		if err != nil {
			return zero, false, err
		}

		subject.Sessions = sessions
	}

	input, err := definition.Bind(subject, values)
	if err != nil {
		return zero, false, usageError(err)
	}

	router := action.Router{Local: nil, Peer: peer, Origin: nil}
	output, err := action.Run(ctx, router, definition, subject.At, action.Caller{Client: "", Pane: "", Session: "", Width: "", Height: ""}, input)

	return output, true, err
}

// Connection returns the remote connection the command targets, or empty for this machine.
func Connection(cmd *cobra.Command) string {
	if cmd.Flags().Lookup(ConnectionFlag) == nil {
		return ""
	}

	connection, _ := cmd.Flags().GetString(ConnectionFlag)

	return connection
}

func peerSessions(rows []session.Session, kind action.SubjectKind, selectors []string) ([]session.Session, error) {
	live := slices.DeleteFunc(slices.Clone(rows), session.Session.Saved)
	saved := slices.DeleteFunc(slices.Clone(rows), func(row session.Session) bool { return !row.Saved() })

	selected := make([]session.Session, 0, len(selectors))

	for _, selector := range selectors {
		var (
			target session.Session
			err    error
		)

		switch kind {
		case action.SubjectLiveSession:
			target, err = action.SelectSession(live, selector)
		case action.SubjectSavedSession:
			target, err = action.SelectSession(saved, selector)
		case action.SubjectSession:
			target, err = action.SelectSession(live, selector)
			if action.CodeOf(err) == action.CodeNotFound {
				target, err = action.SelectSession(saved, selector)
			}
		case action.SubjectNone, action.SubjectAgentPane, action.SubjectTask:
			err = action.Wrap(action.CodeInvalidArgument, action.ErrSubjectMismatch)
		default:
			err = action.Wrap(action.CodeInvalidArgument, action.ErrSubjectMismatch)
		}

		if err != nil {
			return nil, err
		}

		selected = append(selected, target)
	}

	return selected, nil
}

func Render[In, Out any](cmd *cobra.Command, definition *action.Definition[In, Out], output Out, err error) error {
	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResultError(cmd, output, err)
	}

	if err != nil {
		return err
	}

	report := definition.Report(output)
	if report == "" {
		return nil
	}

	return clibase.WriteOutput(cmd, "%s\n", report)
}

func commandValues(cmd *cobra.Command, args []string, descriptor action.Descriptor) ([]string, action.Values, error) {
	values := make(action.Values)
	local := Connection(cmd) == ""

	positional := make([]action.Param, 0, len(descriptor.Params))
	for _, param := range descriptor.Params {
		if param.Positional {
			positional = append(positional, param)

			continue
		}

		if err := flagValue(cmd, param, values, local); err != nil {
			return nil, nil, err
		}
	}

	split := max(len(args)-len(positional), 0)
	for index, param := range positional {
		values.Set(param.Name, pathValue(param, args[split+index], local))
	}

	return args[:split], values, nil
}

func flagValue(cmd *cobra.Command, param action.Param, values action.Values, local bool) error {
	flag := cmd.Flags().Lookup(param.Name)
	if flag == nil || !flag.Changed {
		return nil
	}

	switch param.Kind {
	case action.ParamBool:
		enabled, err := cmd.Flags().GetBool(param.Name)
		if err != nil {
			return clibase.NewExitError(clibase.ExitCodeUsage, err)
		}

		values.Set(param.Name, strconv.FormatBool(enabled))
	case action.ParamList:
		items, err := cmd.Flags().GetStringSlice(param.Name)
		if err != nil {
			return clibase.NewExitError(clibase.ExitCodeUsage, err)
		}

		values.Set(param.Name, items...)
	case action.ParamText, action.ParamPath, action.ParamChoice:
		value, err := cmd.Flags().GetString(param.Name)
		if err != nil {
			return clibase.NewExitError(clibase.ExitCodeUsage, err)
		}

		values.Set(param.Name, pathValue(param, value, local))
	}

	return nil
}

func pathValue(param action.Param, value string, local bool) string {
	if !local || param.Kind != action.ParamPath || value == "" || filepath.IsAbs(value) {
		return value
	}

	if _, expanded := session.ExpandHomePath(value); expanded {
		return value
	}

	if absolute, err := filepath.Abs(value); err == nil {
		return absolute
	}

	return value
}

func addParamFlag(cmd *cobra.Command, param action.Param) {
	if param.Positional {
		return
	}

	switch param.Kind {
	case action.ParamBool:
		enabled, _ := strconv.ParseBool(param.Default)
		cmd.Flags().BoolP(param.Name, param.Short, enabled, param.Usage)
	case action.ParamList:
		cmd.Flags().StringSliceP(param.Name, param.Short, nil, param.Usage)
	case action.ParamText, action.ParamPath:
		cmd.Flags().StringP(param.Name, param.Short, param.Default, param.Usage)
	case action.ParamChoice:
		cmd.Flags().StringP(param.Name, param.Short, param.Default, param.Usage)
		clibase.SetFlagChoices(cmd, param.Name, param.Choices...)
	}
}

func usageSuffix(descriptor action.Descriptor) string {
	var parts []string

	if descriptor.Subject != action.SubjectNone {
		name := subjectName(descriptor.Subject)

		switch descriptor.Arity {
		case action.ArityOne:
			parts = append(parts, "<"+name+">")
		case action.ArityMany:
			parts = append(parts, "<"+name+">...")
		case action.ArityOptional:
			parts = append(parts, "["+name+"...]")
		}
	}

	for _, param := range descriptor.Params {
		if param.Positional {
			parts = append(parts, "<"+param.Name+">")
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return " " + strings.Join(parts, " ")
}

func subjectName(kind action.SubjectKind) string {
	switch kind {
	case action.SubjectAgentPane:
		return "pane"
	case action.SubjectTask:
		return "task"
	case action.SubjectNone, action.SubjectLiveSession, action.SubjectSavedSession, action.SubjectSession:
		return "session"
	default:
		return "session"
	}
}

func long(descriptor action.Descriptor) string {
	if descriptor.Help == "" {
		return descriptor.Summary
	}

	return descriptor.Help
}

func argumentCount(descriptor action.Descriptor) cobra.PositionalArgs {
	positional := 0

	for _, param := range descriptor.Params {
		if param.Positional {
			positional++
		}
	}

	if descriptor.Subject == action.SubjectNone {
		return cobra.ExactArgs(positional)
	}

	switch descriptor.Arity {
	case action.ArityMany:
		return cobra.MinimumNArgs(positional + 1)
	case action.ArityOptional:
		return cobra.MinimumNArgs(positional)
	case action.ArityOne:
		return cobra.ExactArgs(positional + 1)
	default:
		return cobra.ExactArgs(positional + 1)
	}
}

func usageError(err error) error {
	if action.CodeOf(err) == action.CodeInvalidArgument {
		return clibase.NewExitError(clibase.ExitCodeUsage, err)
	}

	return err
}
