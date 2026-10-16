//go:build linux || darwin

package remote

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"time"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/cli/clibase"
	cmdpicker "github.com/zigai/sesh/internal/cli/cmd/picker"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/remote"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/task"
)

const (
	watchInterval     = 250 * time.Millisecond
	heartbeatInterval = 15 * time.Second
	lookupArgCount    = 2
)

var errRemoteTargetMissing = errors.New("remote target is not present in the cached catalog")

// lookupOutput is a receipt as `remote lookup` reports it. Output is the action's own
// result.
type lookupOutput struct {
	InvocationID string         `json:"invocationId"`
	Action       action.ID      `json:"action"`
	OK           bool           `json:"ok"`
	Code         action.Code    `json:"code,omitempty"`
	Output       jsontext.Value `json:"output,omitzero"`
}

type lookupRuntime struct {
	peerID   string
	receipts *privatefs.Dir
}

type peerRuntime struct {
	peerID, bridgeDir string
	server            *gotmux.Server
	live              *remote.GotmuxLiveServer
	receipts          *privatefs.Dir
}

func NewCommand() *cobra.Command {
	return newRemoteCommand()
}

func newRemoteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Browse and operate configured remote tmux peers",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		RunE:  clibase.RequireSubcommand,
	}
	cmd.AddCommand(
		newRemoteAttachCommand(),
		newRemoteLookupCommand(),
		newRemotePeerCommand(),
	)

	return cmd
}

// NewDaemonMonitorCommand returns the foreground monitor that maintains the local remote
// inventory cache.
func NewDaemonMonitorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote-monitor",
		Short: "Run the foreground remote inventory monitor",
		Args:  clibase.UsageArgs(cobra.NoArgs),
		Annotations: map[string]string{
			clibase.AnnotationLiveIO:    "true",
			clibase.AnnotationOperation: "remote_monitor",
		},
		RunE: runRemoteMonitor,
	}
	cmd.Flags().String("socket", "", "Managed tmux socket retained for service-definition consistency")

	return cmd
}

func newRemotePeerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "peer",
		Short:  "Serve one fixed remote protocol channel",
		Hidden: true,
		Args:   clibase.UsageArgs(cobra.NoArgs),
		RunE:   clibase.RequireSubcommand,
	}
	for _, child := range []*cobra.Command{
		{Use: "stream", Args: clibase.UsageArgs(cobra.NoArgs), Annotations: liveAnnotations("remote_peer_stream"), RunE: runRemotePeerStream},
		{Use: "action", Args: clibase.UsageArgs(cobra.NoArgs), Annotations: liveAnnotations("remote_peer_action"), RunE: runRemotePeerAction},
		{Use: "lookup", Args: clibase.UsageArgs(cobra.NoArgs), Annotations: liveAnnotations("remote_peer_lookup"), RunE: runRemotePeerLookup},
		{Use: "attach-prepare", Args: clibase.UsageArgs(cobra.NoArgs), Annotations: liveAnnotations("remote_peer_attach_prepare"), RunE: runRemotePeerAttachPrepare},
		{Use: "attach <attachment-id>", Args: clibase.UsageArgs(cobra.ExactArgs(1)), Annotations: liveAnnotations("remote_peer_attach"), RunE: runRemotePeerAttach},
	} {
		if child.Name() == "action" {
			child.Annotations[clibase.AnnotationSuccessOwned] = "true"
		}

		cmd.AddCommand(child)
	}

	return cmd
}

func liveAnnotations(operation string) map[string]string {
	return map[string]string{
		clibase.AnnotationLiveIO:    "true",
		clibase.AnnotationOperation: operation,
	}
}

// newPeerLocal runs actions on this peer against its own snapshot repository and leases.
func newPeerLocal(workspace app.Workspace, server *gotmux.Server) (*app.Local, *app.Repository) {
	repository := app.NewRepository(workspace.Repository)
	local := app.NewLocal(app.LocalOptions{
		Config:             workspace.Config,
		Server:             server,
		Repository:         repository,
		LeaseRoot:          workspace.LeaseRoot,
		HistoryPath:        workspace.History,
		AgentSource:        workspace.AgentSource(),
		Streams:            gotmux.Streams{In: nil, Out: nil, Err: nil},
		Source:             "remote",
		DeferSwitchHistory: false,
		TaskLedger:         workspace.Tasks,
		WorktreeDir:        workspace.WorktreeDir,
	})

	return local, repository
}

// actions lists the actions this peer accepts from other machines.
func actions(cfg *config.Config) []action.ID {
	ids := make([]action.ID, 0)

	for _, entry := range action.All() {
		descriptor := entry.Describe()
		if action.PolicyFor(cfg, descriptor).AllowRemote {
			ids = append(ids, descriptor.ID)
		}
	}

	return ids
}

func openRemotePrivate(path string) (*privatefs.Dir, error) {
	dir, err := privatefs.Ensure(path)
	if err != nil {
		return nil, oops.In("cli").With("operation", "open_remote_state").Wrap(err)
	}

	return dir, nil
}

func cliError(operation string, err error) error {
	if err == nil {
		return nil
	}

	if diagnostic, ok := oops.AsOops(err); ok {
		if diagnostic.Domain() == "remote" {
			return err
		}

		for _, layer := range diagnostic.Layers() {
			if layer != nil && layer.Domain == "remote" {
				return err
			}
		}

		if domain := diagnostic.Domain(); domain != "" {
			return oops.In(domain).With("operation", operation).Wrap(err)
		}
	}

	return oops.In("cli").With("operation", operation).Wrap(err)
}

func runRemoteMonitor(cmd *cobra.Command, _ []string) (resultErr error) {
	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	path, err := remote.Dir(config.GetXDGPaths().CacheHome)
	if err != nil {
		return cliError("resolve_remote_cache", err)
	}

	dir, err := openRemotePrivate(path)
	if err != nil {
		return err
	}

	defer func() { resultErr = errors.Join(resultErr, cliError("close_remote_cache", dir.Close())) }()

	reloads, err := startRemoteMonitorReloads(clibase.CommandContext(cmd), cmd)
	if err != nil {
		return err
	}

	defer reloads.close()

	ctx := clibase.CommandContext(cmd)

	err = remote.RunMonitor(ctx, remote.MonitorOptions{
		CatalogDir: dir,
		Remote:     cfg.Remote,
		Command:    nil,
		Now:        nil,
		Reloads:    reloads.configs,
	})
	if ctx.Err() != nil && clibase.OnlyErrorCauses(err, context.Canceled) {
		return nil
	}

	return cliError("run_remote_monitor", err)
}

func readRemoteInput(reader io.Reader, limit int) ([]byte, error) { //nolint:unparam // Action and bootstrap protocols keep independent limits.
	content, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, cliError("read_remote_input", err)
	}

	if len(content) > limit {
		return nil, remote.ErrFrameTooLarge
	}

	return content, nil
}

func openPeerRuntime(socket string, needReceipts bool) (*peerRuntime, error) {
	server, err := tmuxserver.Open(socket)
	if err != nil {
		return nil, cliError("open_remote_tmux", err)
	}

	stateHome := config.GetXDGPaths().StateHome

	statePath, err := remote.PeerStateDir(stateHome)
	if err != nil {
		return nil, cliError("resolve_remote_peer_state", err)
	}

	stateDir, err := openRemotePrivate(statePath)
	if err != nil {
		return nil, err
	}

	peerID, idErr := remote.LoadOrCreatePeerID(stateDir)

	closeErr := stateDir.Close()
	if err := errors.Join(
		cliError("load_remote_peer_identity", idErr),
		cliError("close_remote_peer_state", closeErr),
	); err != nil {
		return nil, err
	}

	bridgeDir, err := remote.BridgeDir(stateHome)
	if err != nil {
		return nil, cliError("resolve_remote_bridge", err)
	}

	runtime := &peerRuntime{
		peerID:    peerID,
		bridgeDir: bridgeDir,
		server:    server,
		live:      remote.NewGotmuxLiveServer(server),
		receipts:  nil,
	}

	if needReceipts {
		if err := runtime.openReceipts(stateHome); err != nil {
			return nil, err
		}
	}

	return runtime, nil
}

func (runtime *peerRuntime) openReceipts(stateHome string) error {
	receiptPath, err := remote.InvocationDir(stateHome)
	if err != nil {
		return cliError("resolve_remote_invocations", err)
	}

	runtime.receipts, err = openRemotePrivate(receiptPath)

	return err
}

func (runtime *peerRuntime) close() error {
	if runtime == nil || runtime.receipts == nil {
		return nil
	}

	return cliError("close_remote_invocations", runtime.receipts.Close())
}

func runRemotePeerStream(cmd *cobra.Command, _ []string) error {
	payload, err := readRemoteInput(cmd.InOrStdin(), remote.MaxBootstrapBytes)
	if err != nil {
		return err
	}

	bootstrap, err := remote.DecodeStreamBootstrap(payload)
	if err != nil {
		return cliError("decode_remote_stream_bootstrap", err)
	}

	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return err
	}

	workspace.Socket = bootstrap.TmuxSocket

	runtime, err := openPeerRuntime(workspace.Socket, false)
	if err != nil {
		return err
	}

	hostname, _ := os.Hostname()

	return cliError("run_remote_inventory", remote.RunInventoryStream(clibase.CommandContext(cmd), remote.InventoryOptions{
		PeerID: runtime.peerID, Hostname: hostname, BridgeDir: runtime.bridgeDir, TmuxSocket: bootstrap.TmuxSocket,
		PollInterval:      remote.ParsedDuration(bootstrap.PollInterval, time.Second),
		HeartbeatInterval: remote.ParsedDuration(bootstrap.HeartbeatInterval, heartbeatInterval),
		Sessions:          runtime.live.Sessions,
		Agents: func(ctx context.Context) ([]session.AgentSummary, error) {
			return peerAgentSummaries(ctx, workspace)
		},
		SavedSessions: func(ctx context.Context) ([]session.Session, error) { return peerSavedSessions(ctx, workspace) },
		Tasks: func(ctx context.Context) ([]remote.TaskRow, error) {
			return peerTasks(ctx, runtime.server, workspace.Tasks)
		},
		Actions: actions(workspace.Config),
	}, cmd.OutOrStdout()))
}

func peerTasks(ctx context.Context, server *gotmux.Server, ledger string) ([]remote.TaskRow, error) {
	tasks, err := task.Inventory(ctx, server, task.NewStore(ledger))
	if err != nil {
		return nil, cliError("list_remote_tasks", err)
	}

	rows := make([]remote.TaskRow, 0, len(tasks))
	for _, live := range tasks {
		rows = append(rows, remote.NewTaskRow(live))
	}

	return rows, nil
}

func runRemotePeerAction(cmd *cobra.Command, _ []string) (resultErr error) {
	payload, err := readRemoteInput(cmd.InOrStdin(), remote.MaxActionBytes)
	if err != nil {
		return err
	}

	request, err := remote.DecodeActionRequest(payload)
	if err != nil {
		return cliError("decode_remote_action", err)
	}

	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return err
	}

	workspace.Socket = request.Socket

	runtime, err := openPeerRuntime(workspace.Socket, true)
	if err != nil {
		return err
	}

	defer func() { resultErr = errors.Join(resultErr, runtime.close()) }()

	local, repository := newPeerLocal(workspace, runtime.server)

	defer func() {
		resultErr = errors.Join(resultErr, cliError("close_remote_repository", repository.Close()))
	}()

	allowed := actions(workspace.Config)
	result := remote.ExecutePeerAction(clibase.CommandContext(cmd), remote.PeerActionOptions{
		PeerID:     runtime.peerID,
		Receipts:   runtime.receipts,
		Allowed:    func(id action.ID) bool { return slices.Contains(allowed, id) },
		Dispatcher: local,
		Now:        nil,
	}, request)
	logPeerActionResult(clibase.CommandContext(cmd), result)

	return cliError("write_remote_action_result", remote.WriteActionResult(cmd.OutOrStdout(), result))
}

func logPeerActionResult(ctx context.Context, result remote.ActionResult) {
	level, status, reason := zerolog.InfoLevel, "ok", ""
	if !result.OK {
		level, status, reason = zerolog.ErrorLevel, "failed", string(result.Code)
		if result.Code == action.CodePartial || result.Code == action.CodeUncertain {
			level, status = zerolog.WarnLevel, "degraded"
		}
	}

	event := zerolog.Ctx(ctx).WithLevel(level).Str("component", "remote").
		Str("operation", "remote_peer_action").Str("action", string(result.Action)).Str("status", status).Str("phase", "receipt")
	if reason != "" {
		event.Str("reason", reason)
	}

	event.Msg("remote action receipt resolved")
}

func decodeRemoteAttachBootstrap(cmd *cobra.Command) (remote.AttachBootstrap, error) {
	payload, err := readRemoteInput(cmd.InOrStdin(), remote.MaxBootstrapBytes)
	if err != nil {
		return remote.AttachBootstrap{}, err
	}

	bootstrap, err := remote.DecodeAttachBootstrap(payload)

	return bootstrap, cliError("decode_remote_attach_bootstrap", err)
}

func runRemotePeerAttachPrepare(cmd *cobra.Command, _ []string) error {
	bootstrap, err := decodeRemoteAttachBootstrap(cmd)
	if err != nil {
		return err
	}

	runtime, err := openPeerRuntime(bootstrap.Target.TmuxServerSocket, false)
	if err != nil {
		return err
	}

	result, err := remote.RunPeerAttachPrepare(clibase.CommandContext(cmd), remote.PeerAttachOptions{
		PeerID: runtime.peerID, BridgeDir: runtime.bridgeDir, Live: runtime.live, PaneOwner: runtime.live,
		Now: nil,
	}, bootstrap)
	if err != nil {
		return cliError("prepare_remote_peer_attach", err)
	}

	encoded, err := remote.EncodeAttachPrepareResult(result)
	if err != nil {
		return cliError("encode_remote_attach_result", err)
	}

	_, err = cmd.OutOrStdout().Write(append(encoded, '\n'))

	return cliError("write_remote_attach_result", err)
}

func runRemotePeerAttach(cmd *cobra.Command, args []string) error {
	capabilities, err := remote.CapabilityReader(config.GetXDGPaths().StateHome)
	if err != nil {
		return cliError("open_remote_capabilities", err)
	}

	record, err := remote.ReadCapability(capabilities, args[0], time.Now())

	closeErr := capabilities.Close()
	if err := errors.Join(
		cliError("read_remote_capability", err),
		cliError("close_remote_capabilities", closeErr),
	); err != nil {
		return err
	}

	runtime, err := openPeerRuntime(record.Target.TmuxServerSocket, false)
	if err != nil {
		return err
	}

	in, out, errFile, streamErr := terminalFiles(cmd)
	if streamErr != nil {
		return streamErr
	}

	return cliError("run_remote_peer_attach", remote.RunPeerAttach(clibase.CommandContext(cmd), remote.PeerAttachOptions{
		PeerID: runtime.peerID, BridgeDir: runtime.bridgeDir, Live: runtime.live, PaneOwner: runtime.live,
		Now: nil,
	}, args[0], remote.AttachStreams{In: in, Out: out, Err: errFile}))
}

func terminalFiles(cmd *cobra.Command) (*os.File, *os.File, *os.File, error) {
	in, inOK := cmd.InOrStdin().(*os.File)
	out, outOK := outputFile(cmd.OutOrStdout())

	errFile, errOK := outputFile(cmd.ErrOrStderr())
	if !inOK || !outOK || !errOK {
		return nil, nil, nil, session.ErrTerminalRequired
	}

	return in, out, errFile, nil
}

func outputFile(writer io.Writer) (*os.File, bool) {
	if file, ok := writer.(*os.File); ok {
		return file, true
	}

	type unwrapper interface {
		Unwrap() io.Writer
	}
	if u, ok := writer.(unwrapper); ok {
		return outputFile(u.Unwrap())
	}

	type fileProvider interface {
		File() *os.File
	}
	if fp, ok := writer.(fileProvider); ok {
		return fp.File(), true
	}

	return nil, false
}

func peerAgentSummaries(ctx context.Context, workspace app.Workspace) ([]session.AgentSummary, error) {
	observed, err := agents.LoadLiveAgents(ctx, workspace.AgentSource())
	if err != nil {
		return nil, oops.In("cli").With("operation", "load_remote_agents").Wrap(err)
	}

	return cmdpicker.AgentSummaries(observed), nil
}

func peerSavedSessions(ctx context.Context, workspace app.Workspace) ([]session.Session, error) {
	repository, err := workspace.OpenExistingRepository(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, oops.In("cli").With("operation", "open_remote_repository").Wrap(err)
	}

	rows, listErr := session.ListSaved(ctx, repository, "remote", "")
	closeErr := repository.Close()

	if listErr != nil {
		listErr = oops.In("cli").With("operation", "list_remote_saved_sessions").Wrap(listErr)
	}

	if closeErr != nil {
		closeErr = oops.In("cli").With("operation", "close_remote_repository").Wrap(closeErr)
	}

	return rows, errors.Join(listErr, closeErr)
}

func newRemoteLookupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lookup <connection> <invocation>",
		Short: "Inspect one durable remote action receipt without redispatch",
		Args:  clibase.UsageArgs(cobra.ExactArgs(lookupArgCount)),
		Annotations: map[string]string{
			clibase.AnnotationOperation: "remote_lookup",
		},
		RunE: runRemoteLookup,
	}
	clibase.AddJSONFlag(cmd)

	return cmd
}

func runRemotePeerLookup(cmd *cobra.Command, _ []string) (resultErr error) {
	payload, err := readRemoteInput(cmd.InOrStdin(), remote.MaxActionBytes)
	if err != nil {
		return err
	}

	request, err := remote.DecodeInvocationLookupRequest(payload)
	if err != nil {
		return cliError("decode_remote_lookup", err)
	}

	runtime, err := openLookupRuntime()
	if err != nil {
		return err
	}

	if runtime.receipts != nil {
		defer func() {
			resultErr = errors.Join(resultErr, cliError("close_remote_lookup", runtime.receipts.Close()))
		}()
	}

	result := remote.ActionResult{
		InvocationID: request.InvocationID, Action: "", OK: false, Code: action.CodeNotFound, Output: nil,
	}
	if runtime.receipts != nil {
		result = remote.ExecutePeerLookup(runtime.receipts, runtime.peerID, request)
	}

	return cliError("write_remote_lookup_result", remote.WriteActionResult(cmd.OutOrStdout(), result))
}

func openLookupRuntime() (*lookupRuntime, error) {
	stateHome := config.GetXDGPaths().StateHome

	statePath, err := remote.PeerStatePath(stateHome)
	if err != nil {
		return nil, cliError("resolve_remote_peer_state", err)
	}

	stateDir, err := privatefs.Open(statePath, false)
	if err != nil {
		return nil, cliError("open_remote_peer_state", err)
	}

	peerID, idErr := remote.LoadPeerID(stateDir)

	closeErr := stateDir.Close()
	if err := errors.Join(idErr, closeErr); err != nil {
		return nil, cliError("load_remote_peer_identity", err)
	}

	receiptPath, err := remote.InvocationPath(stateHome)
	if err != nil {
		return nil, cliError("resolve_remote_invocations", err)
	}

	receipts, err := privatefs.Open(receiptPath, false)
	if errors.Is(err, fs.ErrNotExist) {
		return &lookupRuntime{peerID: peerID, receipts: nil}, nil
	}

	if err != nil {
		return nil, cliError("open_remote_invocations", err)
	}

	return &lookupRuntime{peerID: peerID, receipts: receipts}, nil
}

func runRemoteLookup(cmd *cobra.Command, args []string) (resultErr error) {
	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	connection, err := remote.FindConnection(cfg.Remote, args[0])
	if err != nil {
		return cliError("find_remote_lookup_connection", err)
	}

	catalog, dir, err := loadRemoteCatalog()
	if dir != nil {
		defer func() { resultErr = errors.Join(resultErr, cliError("close_remote_catalog", dir.Close())) }()
	}

	if err != nil {
		return err
	}

	state, ok := catalog.Connections[connection.ID]
	if !ok {
		return cliError("resolve_remote_target", errRemoteTargetMissing)
	}

	options := remote.ActionOriginOptions{Remote: cfg.Remote, Connection: connection, State: state}

	request, err := remote.NewInvocationLookupRequest(options, args[1])
	if err != nil {
		return cliError("build_remote_lookup", err)
	}

	result, runErr := remote.RunInvocationLookup(clibase.CommandContext(cmd), options, request)
	output := lookupOutput{
		InvocationID: result.InvocationID, Action: result.Action, OK: result.OK, Code: result.Code, Output: result.Output,
	}

	if runErr != nil {
		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResultError(cmd, output, cliError("run_remote_lookup", runErr))
		}

		return cliError("run_remote_lookup", runErr)
	}

	if clibase.JSONOutputEnabled(cmd) {
		return clibase.WriteJSONResult(cmd, output)
	}

	return clibase.WriteOutput(cmd, "%s\n", lookupReport(result))
}

func lookupReport(result remote.ActionResult) string {
	status := string(result.Code)
	if result.OK {
		status = "complete"
	}

	report := fmt.Sprintf("Remote invocation %s: %s", result.InvocationID, status)

	entry, ok := action.Lookup(result.Action)
	if !ok || !result.OK || len(result.Output) == 0 {
		return report
	}

	output, err := entry.DecodeOutput(result.Output)
	if err != nil {
		return report
	}

	return report + "\n" + entry.ReportAny(output)
}
