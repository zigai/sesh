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
	"github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/cli/clibase"
	cmdpicker "github.com/zigai/sesh/internal/cli/cmd/picker"
	"github.com/zigai/sesh/internal/config"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/remote"
	"github.com/zigai/sesh/internal/session"
)

const (
	remoteWatchInterval     = 250 * time.Millisecond
	remoteHeartbeatInterval = 15 * time.Second
	remoteLookupArgCount    = 2
)

var errRemoteTargetMissing = errors.New("remote target is not present in the cached catalog")

// remoteLookupOutput is a receipt as `remote lookup` reports it. Output is the action's own
// result.
type remoteLookupOutput struct {
	InvocationID string         `json:"invocationId"`
	Action       action.ID      `json:"action"`
	OK           bool           `json:"ok"`
	Code         action.Code    `json:"code,omitempty"`
	Output       jsontext.Value `json:"output,omitzero"`
}

type remoteLookupRuntime struct {
	peerID   string
	receipts *privatefs.Dir
}

type remotePeerRuntime struct {
	peerID, bridgeDir string
	server            *tmux.Server
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
		{Use: "stream", Args: clibase.UsageArgs(cobra.NoArgs), Annotations: remoteLiveAnnotations("remote_peer_stream"), RunE: runRemotePeerStream},
		{Use: "action", Args: clibase.UsageArgs(cobra.NoArgs), Annotations: remoteLiveAnnotations("remote_peer_action"), RunE: runRemotePeerAction},
		{Use: "lookup", Args: clibase.UsageArgs(cobra.NoArgs), Annotations: remoteLiveAnnotations("remote_peer_lookup"), RunE: runRemotePeerLookup},
		{Use: "attach-prepare", Args: clibase.UsageArgs(cobra.NoArgs), Annotations: remoteLiveAnnotations("remote_peer_attach_prepare"), RunE: runRemotePeerAttachPrepare},
		{Use: "attach <attachment-id>", Args: clibase.UsageArgs(cobra.ExactArgs(1)), Annotations: remoteLiveAnnotations("remote_peer_attach"), RunE: runRemotePeerAttach},
	} {
		if child.Name() == "action" {
			child.Annotations[clibase.AnnotationSuccessOwned] = "true"
		}

		cmd.AddCommand(child)
	}

	return cmd
}

func remoteLiveAnnotations(operation string) map[string]string {
	return map[string]string{
		clibase.AnnotationLiveIO:    "true",
		clibase.AnnotationOperation: operation,
	}
}

// newPeerLocal runs actions on this peer against its own snapshot repository and leases.
func newPeerLocal(workspace app.Workspace, server *tmux.Server) (*app.Local, *app.Repository) {
	repository := app.NewRepository(workspace.Repository)
	local := app.NewLocal(app.LocalOptions{
		Config:             workspace.Config,
		Server:             server,
		Repository:         repository,
		LeaseRoot:          workspace.LeaseRoot,
		HistoryPath:        workspace.History,
		AgentSource:        workspace.AgentSource(),
		Streams:            tmux.Streams{In: nil, Out: nil, Err: nil},
		Source:             "remote",
		DeferSwitchHistory: false,
	})

	return local, repository
}

// remoteActions lists the actions this peer accepts from other machines.
func remoteActions(cfg *config.Config) []action.ID {
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

func remoteCLIError(operation string, err error) error {
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
		return remoteCLIError("resolve_remote_cache", err)
	}

	dir, err := openRemotePrivate(path)
	if err != nil {
		return err
	}

	defer func() { resultErr = errors.Join(resultErr, remoteCLIError("close_remote_cache", dir.Close())) }()

	reloads, err := startRemoteMonitorReloads(clibase.CommandContext(cmd), cmd)
	if err != nil {
		return err
	}

	defer reloads.close()

	return remoteCLIError("run_remote_monitor", remote.RunMonitor(clibase.CommandContext(cmd), remote.MonitorOptions{
		CatalogDir: dir,
		Remote:     cfg.Remote,
		Command:    nil,
		Now:        nil,
		Reloads:    reloads.configs,
	}))
}

func readRemoteInput(reader io.Reader, limit int) ([]byte, error) { //nolint:unparam // Action and bootstrap protocols keep independent limits.
	content, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, remoteCLIError("read_remote_input", err)
	}

	if len(content) > limit {
		return nil, remote.ErrFrameTooLarge
	}

	return content, nil
}

func openRemotePeerRuntime(socket string, needReceipts bool) (*remotePeerRuntime, error) {
	server, err := integration.Open(socket)
	if err != nil {
		return nil, remoteCLIError("open_remote_tmux", err)
	}

	stateHome := config.GetXDGPaths().StateHome

	statePath, err := remote.PeerStateDir(stateHome)
	if err != nil {
		return nil, remoteCLIError("resolve_remote_peer_state", err)
	}

	stateDir, err := openRemotePrivate(statePath)
	if err != nil {
		return nil, err
	}

	peerID, idErr := remote.LoadOrCreatePeerID(stateDir)

	closeErr := stateDir.Close()
	if err := errors.Join(
		remoteCLIError("load_remote_peer_identity", idErr),
		remoteCLIError("close_remote_peer_state", closeErr),
	); err != nil {
		return nil, err
	}

	bridgeDir, err := remote.BridgeDir(stateHome)
	if err != nil {
		return nil, remoteCLIError("resolve_remote_bridge", err)
	}

	runtime := &remotePeerRuntime{
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

func (runtime *remotePeerRuntime) openReceipts(stateHome string) error {
	receiptPath, err := remote.InvocationDir(stateHome)
	if err != nil {
		return remoteCLIError("resolve_remote_invocations", err)
	}

	runtime.receipts, err = openRemotePrivate(receiptPath)

	return err
}

func (runtime *remotePeerRuntime) close() error {
	if runtime == nil || runtime.receipts == nil {
		return nil
	}

	return remoteCLIError("close_remote_invocations", runtime.receipts.Close())
}

func runRemotePeerStream(cmd *cobra.Command, _ []string) error {
	payload, err := readRemoteInput(cmd.InOrStdin(), remote.MaxBootstrapBytes)
	if err != nil {
		return err
	}

	bootstrap, err := remote.DecodeStreamBootstrap(payload)
	if err != nil {
		return remoteCLIError("decode_remote_stream_bootstrap", err)
	}

	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return err
	}

	workspace.Socket = bootstrap.TmuxSocket

	runtime, err := openRemotePeerRuntime(workspace.Socket, false)
	if err != nil {
		return err
	}

	hostname, _ := os.Hostname()

	return remoteCLIError("run_remote_inventory", remote.RunInventoryStream(clibase.CommandContext(cmd), remote.InventoryOptions{
		PeerID: runtime.peerID, Hostname: hostname, BridgeDir: runtime.bridgeDir, TmuxSocket: bootstrap.TmuxSocket,
		PollInterval:      remote.ParsedDuration(bootstrap.PollInterval, time.Second),
		HeartbeatInterval: remote.ParsedDuration(bootstrap.HeartbeatInterval, remoteHeartbeatInterval),
		Sessions:          runtime.live.Sessions,
		Agents: func(ctx context.Context) ([]session.AgentSummary, error) {
			return remotePeerAgentSummaries(ctx, workspace)
		},
		SavedSessions: func(ctx context.Context) ([]session.Session, error) { return remotePeerSavedSessions(ctx, workspace) },
		Actions:       remoteActions(workspace.Config),
	}, cmd.OutOrStdout()))
}

func runRemotePeerAction(cmd *cobra.Command, _ []string) (resultErr error) {
	payload, err := readRemoteInput(cmd.InOrStdin(), remote.MaxActionBytes)
	if err != nil {
		return err
	}

	request, err := remote.DecodeActionRequest(payload)
	if err != nil {
		return remoteCLIError("decode_remote_action", err)
	}

	workspace, err := clibase.ResolveWorkspace(cmd)
	if err != nil {
		return err
	}

	workspace.Socket = request.Socket

	runtime, err := openRemotePeerRuntime(workspace.Socket, true)
	if err != nil {
		return err
	}

	defer func() { resultErr = errors.Join(resultErr, runtime.close()) }()

	local, repository := newPeerLocal(workspace, runtime.server)

	defer func() {
		resultErr = errors.Join(resultErr, remoteCLIError("close_remote_repository", repository.Close()))
	}()

	allowed := remoteActions(workspace.Config)
	result := remote.ExecutePeerAction(clibase.CommandContext(cmd), remote.PeerActionOptions{
		PeerID:     runtime.peerID,
		Receipts:   runtime.receipts,
		Allowed:    func(id action.ID) bool { return slices.Contains(allowed, id) },
		Dispatcher: local,
		Now:        nil,
	}, request)
	logPeerActionResult(clibase.CommandContext(cmd), result)

	return remoteCLIError("write_remote_action_result", remote.WriteActionResult(cmd.OutOrStdout(), result))
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

	return bootstrap, remoteCLIError("decode_remote_attach_bootstrap", err)
}

func runRemotePeerAttachPrepare(cmd *cobra.Command, _ []string) error {
	bootstrap, err := decodeRemoteAttachBootstrap(cmd)
	if err != nil {
		return err
	}

	runtime, err := openRemotePeerRuntime(bootstrap.Target.TmuxServerSocket, false)
	if err != nil {
		return err
	}

	result, err := remote.RunPeerAttachPrepare(clibase.CommandContext(cmd), remote.PeerAttachOptions{
		PeerID: runtime.peerID, BridgeDir: runtime.bridgeDir, Live: runtime.live, PaneOwner: runtime.live,
		Now: nil,
	}, bootstrap)
	if err != nil {
		return remoteCLIError("prepare_remote_peer_attach", err)
	}

	encoded, err := remote.EncodeAttachPrepareResult(result)
	if err != nil {
		return remoteCLIError("encode_remote_attach_result", err)
	}

	_, err = cmd.OutOrStdout().Write(append(encoded, '\n'))

	return remoteCLIError("write_remote_attach_result", err)
}

func runRemotePeerAttach(cmd *cobra.Command, args []string) error {
	capabilities, err := remote.CapabilityReader(config.GetXDGPaths().StateHome)
	if err != nil {
		return remoteCLIError("open_remote_capabilities", err)
	}

	record, err := remote.ReadCapability(capabilities, args[0], time.Now())

	closeErr := capabilities.Close()
	if err := errors.Join(
		remoteCLIError("read_remote_capability", err),
		remoteCLIError("close_remote_capabilities", closeErr),
	); err != nil {
		return err
	}

	runtime, err := openRemotePeerRuntime(record.Target.TmuxServerSocket, false)
	if err != nil {
		return err
	}

	in, out, errFile, streamErr := remoteTerminalFiles(cmd)
	if streamErr != nil {
		return streamErr
	}

	return remoteCLIError("run_remote_peer_attach", remote.RunPeerAttach(clibase.CommandContext(cmd), remote.PeerAttachOptions{
		PeerID: runtime.peerID, BridgeDir: runtime.bridgeDir, Live: runtime.live, PaneOwner: runtime.live,
		Now: nil,
	}, args[0], remote.AttachStreams{In: in, Out: out, Err: errFile}))
}

func remoteTerminalFiles(cmd *cobra.Command) (*os.File, *os.File, *os.File, error) {
	in, inOK := cmd.InOrStdin().(*os.File)
	out, outOK := remoteOutputFile(cmd.OutOrStdout())

	errFile, errOK := remoteOutputFile(cmd.ErrOrStderr())
	if !inOK || !outOK || !errOK {
		return nil, nil, nil, session.ErrTerminalRequired
	}

	return in, out, errFile, nil
}

func remoteOutputFile(writer io.Writer) (*os.File, bool) {
	if file, ok := writer.(*os.File); ok {
		return file, true
	}

	type unwrapper interface {
		Unwrap() io.Writer
	}
	if u, ok := writer.(unwrapper); ok {
		return remoteOutputFile(u.Unwrap())
	}

	type fileProvider interface {
		File() *os.File
	}
	if fp, ok := writer.(fileProvider); ok {
		return fp.File(), true
	}

	return nil, false
}

func remotePeerAgentSummaries(ctx context.Context, workspace app.Workspace) ([]session.AgentSummary, error) {
	observed, err := agents.LoadLiveAgents(ctx, workspace.AgentSource())
	if err != nil {
		return nil, oops.In("cli").With("operation", "load_remote_agents").Wrap(err)
	}

	return cmdpicker.AgentSummaries(observed), nil
}

func remotePeerSavedSessions(ctx context.Context, workspace app.Workspace) ([]session.Session, error) {
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
		Args:  clibase.UsageArgs(cobra.ExactArgs(remoteLookupArgCount)),
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
		return remoteCLIError("decode_remote_lookup", err)
	}

	runtime, err := openRemoteLookupRuntime()
	if err != nil {
		return err
	}

	if runtime.receipts != nil {
		defer func() {
			resultErr = errors.Join(resultErr, remoteCLIError("close_remote_lookup", runtime.receipts.Close()))
		}()
	}

	result := remote.ActionResult{
		InvocationID: request.InvocationID, Action: "", OK: false, Code: action.CodeNotFound, Output: nil,
	}
	if runtime.receipts != nil {
		result = remote.ExecutePeerLookup(runtime.receipts, runtime.peerID, request)
	}

	return remoteCLIError("write_remote_lookup_result", remote.WriteActionResult(cmd.OutOrStdout(), result))
}

func openRemoteLookupRuntime() (*remoteLookupRuntime, error) {
	stateHome := config.GetXDGPaths().StateHome

	statePath, err := remote.PeerStatePath(stateHome)
	if err != nil {
		return nil, remoteCLIError("resolve_remote_peer_state", err)
	}

	stateDir, err := privatefs.Open(statePath, false)
	if err != nil {
		return nil, remoteCLIError("open_remote_peer_state", err)
	}

	peerID, idErr := remote.LoadPeerID(stateDir)

	closeErr := stateDir.Close()
	if err := errors.Join(idErr, closeErr); err != nil {
		return nil, remoteCLIError("load_remote_peer_identity", err)
	}

	receiptPath, err := remote.InvocationPath(stateHome)
	if err != nil {
		return nil, remoteCLIError("resolve_remote_invocations", err)
	}

	receipts, err := privatefs.Open(receiptPath, false)
	if errors.Is(err, fs.ErrNotExist) {
		return &remoteLookupRuntime{peerID: peerID, receipts: nil}, nil
	}

	if err != nil {
		return nil, remoteCLIError("open_remote_invocations", err)
	}

	return &remoteLookupRuntime{peerID: peerID, receipts: receipts}, nil
}

func runRemoteLookup(cmd *cobra.Command, args []string) (resultErr error) {
	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	connection, err := remote.FindConnection(cfg.Remote, args[0])
	if err != nil {
		return remoteCLIError("find_remote_lookup_connection", err)
	}

	catalog, dir, err := loadRemoteCatalog()
	if dir != nil {
		defer func() { resultErr = errors.Join(resultErr, remoteCLIError("close_remote_catalog", dir.Close())) }()
	}

	if err != nil {
		return err
	}

	state, ok := catalog.Connections[connection.ID]
	if !ok {
		return remoteCLIError("resolve_remote_target", errRemoteTargetMissing)
	}

	options := remote.ActionOriginOptions{Remote: cfg.Remote, Connection: connection, State: state}

	request, err := remote.NewInvocationLookupRequest(options, args[1])
	if err != nil {
		return remoteCLIError("build_remote_lookup", err)
	}

	result, runErr := remote.RunInvocationLookup(clibase.CommandContext(cmd), options, request)
	output := remoteLookupOutput{
		InvocationID: result.InvocationID, Action: result.Action, OK: result.OK, Code: result.Code, Output: result.Output,
	}

	if runErr != nil {
		if clibase.JSONOutputEnabled(cmd) {
			return clibase.WriteJSONResultError(cmd, output, remoteCLIError("run_remote_lookup", runErr))
		}

		return remoteCLIError("run_remote_lookup", runErr)
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
