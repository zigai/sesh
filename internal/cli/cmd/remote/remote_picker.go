//go:build linux || darwin

package remote

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/cli/clibase"
	cmdpicker "github.com/zigai/sesh/internal/cli/cmd/picker"
	"github.com/zigai/sesh/internal/config"
	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/remote"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui"
)

const (
	remoteAttachArgCount     = 2
	remoteAttachmentLifetime = 24 * time.Hour
)

var errOriginSocketNotAbsolute = errors.New("origin socket must be an absolute tmux socket")

type remotePickerAdapter struct {
	cfg          *config.Config
	originSocket string
	peer         *remote.Peer
}

type remoteAttachExec struct {
	command *exec.Cmd
	bridge  *remote.BridgeServer
	finish  func(remote.OriginSession) error
}

type originPickerAdapter struct{ socket string }

func DefaultPickerRemoteFactory(_ context.Context, cfg *config.Config, socket string) (cmdpicker.RemoteServices, error) {
	if cfg == nil {
		return cmdpicker.RemoteServices{Catalog: nil, Watcher: nil, Peer: nil, Attach: nil, OriginBridge: nil, Origin: nil}, nil
	}

	services := cmdpicker.RemoteServices{Catalog: nil, Watcher: nil, Peer: nil, Attach: nil, OriginBridge: nil, Origin: nil}

	if cfg.Remote.EnableReverseBridge {
		origin := &originPickerAdapter{socket: socket}
		services.OriginBridge, services.Origin = origin, origin
	}

	if !cfg.Remote.Enabled {
		return services, nil
	}

	adapter := &remotePickerAdapter{cfg: cfg.Clone(), originSocket: socket, peer: remote.NewCatalogPeer(cfg.Remote, config.GetXDGPaths().CacheHome)}

	services.Catalog, services.Watcher, services.Attach, services.Peer = adapter, adapter, adapter, adapter.peer

	return services, nil
}

func (adapter *remotePickerAdapter) LoadRemoteCatalog() (tui.RemoteCatalog, error) {
	catalog, dir, err := loadRemoteCatalog()
	if dir != nil {
		defer func() { _ = dir.Close() }()
	}

	if err != nil {
		return tui.RemoteCatalog{}, remoteCLIError("load_remote_catalog", err)
	}

	adapter.peer.Observe(catalog)

	return pickerCatalog(catalog), nil
}

func (adapter *remotePickerAdapter) WatchRemoteCatalog(ctx context.Context, publish func(tui.RemoteCatalog, error)) (resultErr error) {
	path, err := remote.Dir(config.GetXDGPaths().CacheHome)
	if err != nil {
		return remoteCLIError("resolve_remote_picker_cache", err)
	}

	dir, err := openRemotePrivate(path)
	if err != nil {
		return err
	}

	defer func() { resultErr = errors.Join(resultErr, dir.Close()) }()

	err = remote.WatchCatalog(ctx, dir, remoteWatchInterval, func(catalog remote.Catalog, watchErr error) error {
		if watchErr == nil {
			adapter.peer.Observe(catalog)
		}

		publish(pickerCatalog(catalog), watchErr)

		return nil
	})

	return remoteCLIError("watch_remote_picker_catalog", err)
}

func pickerCatalog(catalog remote.Catalog) tui.RemoteCatalog {
	connections := make([]tui.RemoteConnection, 0, len(catalog.Connections))
	for _, state := range catalog.Connections {
		connections = append(connections, tui.RemoteConnection{
			ID: state.ID, Label: state.Label,
			ConfigDigest: state.ConfigDigest, PeerID: state.PeerID, StreamInstance: state.StreamInstance,
			Connected: state.Connected, LastSeen: state.LastSeen, LastAttempt: state.LastAttempt,
			LastError: state.LastError, LastErrorAt: state.LastErrorAt,
			HealthTmux: string(state.Snapshot.Health.Tmux.Status), HealthAgents: string(state.Snapshot.Health.Agents.Status),
			HealthSnapshots: string(state.Snapshot.Health.Snapshots.Status), Sessions: remote.CatalogSessions(state),
		})
	}

	slices.SortFunc(connections, func(a, b tui.RemoteConnection) int {
		if a.ID < b.ID {
			return -1
		}

		if a.ID > b.ID {
			return 1
		}

		return 0
	})

	return tui.RemoteCatalog{GeneratedAt: catalog.GeneratedAt, Connections: connections}
}

func (adapter *remotePickerAdapter) AttachRemoteSession(ctx context.Context, target session.Session, paneID string) (tea.ExecCommand, error) {
	if adapter == nil || adapter.cfg == nil {
		return nil, remoteCLIError("attach_remote_session", remote.ErrIncompleteDependencies)
	}

	connection, err := remote.FindConnection(adapter.cfg.Remote, target.RuntimeIdentity.ConnectionID)
	if err != nil {
		return nil, remoteCLIError("find_remote_attach_connection", err)
	}

	catalog, dir, err := loadRemoteCatalog()
	if dir != nil {
		defer func() { _ = dir.Close() }()
	}

	if err != nil {
		return nil, err
	}

	state, fresh, err := resolveRemoteTarget(catalog, connection.ID, target.ID)
	if err != nil {
		return nil, err
	}

	if fresh.RuntimeIdentity != target.RuntimeIdentity {
		return nil, remoteCLIError("attach_remote_session", remote.ErrTargetChanged)
	}

	return prepareRemoteAttachExec(ctx, adapter.cfg, adapter.originSocket, connection, state, fresh, paneID)
}

func (command *remoteAttachExec) SetStdin(reader io.Reader)  { command.command.Stdin = reader }
func (command *remoteAttachExec) SetStdout(writer io.Writer) { command.command.Stdout = writer }
func (command *remoteAttachExec) SetStderr(writer io.Writer) { command.command.Stderr = writer }

func (command *remoteAttachExec) Run() error {
	if command == nil || command.command == nil || command.bridge == nil {
		return remote.ErrIncompleteDependencies
	}

	runErr := command.command.Run()
	if target, requested := command.bridge.SwitchTarget(); requested && runErr == nil && command.finish != nil {
		runErr = command.finish(target)
	}

	closeErr := command.bridge.Close()

	return errors.Join(runErr, closeErr)
}

func prepareRemoteAttachExec(
	ctx context.Context,
	cfg *config.Config,
	originSocket string,
	connection config.RemoteConnectionConfig,
	state remote.ConnectionState,
	target session.Session,
	paneID string,
) (*remoteAttachExec, error) {
	localRoot, err := remote.Dir(config.GetXDGPaths().CacheHome)
	if err != nil {
		return nil, remoteCLIError("resolve_remote_attach_cache", err)
	}

	localRoot = filepath.Join(localRoot, "bridge")

	dir, err := privatefs.Ensure(localRoot)
	if err != nil {
		return nil, remoteCLIError("ensure_remote_attach_cache", err)
	}

	if err := dir.Close(); err != nil {
		return nil, remoteCLIError("close_remote_attach_cache", err)
	}

	id, err := remote.NewAttachmentID()
	if err != nil {
		return nil, remoteCLIError("create_remote_attachment_id", err)
	}

	localSocket := filepath.Join(localRoot, "origin-"+id+".sock")

	bootstrap, err := remote.PrepareAttachment(remote.AttachOriginOptions{
		Remote: cfg.Remote, Connection: connection,
		BridgeDir: state.BridgeDir, OriginSocket: localSocket, OriginLabel: "origin",
		Target: remoteAttachTarget(state, target, paneID), Timeout: remoteAttachmentLifetime, Now: nil,
	})
	if err != nil {
		return nil, remoteCLIError("prepare_remote_attachment", err)
	}

	bridge, err := remote.NewBridgeServer(ctx, remote.BridgeServerConfig{
		SocketPath: localSocket,
		Token:      bootstrap.Capability.Token, ExpiresAt: bootstrap.Capability.ExpiresAt, OriginID: "local", OriginLabel: "origin",
		ListSessions: func() ([]remote.OriginSession, error) {
			if !cfg.Remote.EnableReverseBridge {
				return nil, nil
			}

			return originSessions(ctx, originSocket)
		}, Now: nil,
	})
	if err != nil {
		return nil, remoteCLIError("start_remote_origin_bridge", err)
	}

	if _, err := remote.RunAttachPrepare(ctx, cfg.Remote, connection, bootstrap, remote.AttachCommandOptions{
		Streams: remote.AttachStreams{In: nil, Out: nil, Err: nil}, Command: nil,
	}); err != nil {
		return nil, errors.Join(remoteCLIError("run_remote_attach_prepare", err), bridge.Close())
	}

	attach, err := remote.NewAttachCommand(ctx, cfg.Remote, connection, bootstrap, remote.AttachCommandOptions{
		Streams: remote.AttachStreams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, Command: nil,
	})
	if err != nil {
		return nil, errors.Join(remoteCLIError("new_remote_attach_command", err), bridge.Close())
	}

	var finish func(remote.OriginSession) error
	if cfg.Remote.EnableReverseBridge {
		finish, err = originReturnAction(ctx, originSocket)
		if err != nil {
			return nil, errors.Join(remoteCLIError("origin_return_action", err), bridge.Close())
		}
	}

	return &remoteAttachExec{command: attach, bridge: bridge, finish: finish}, nil
}

func originReturnAction(ctx context.Context, socket string) (func(remote.OriginSession) error, error) {
	server, err := integration.Open(socket)
	if err != nil {
		return nil, remoteCLIError("open_origin_return_endpoint", err)
	}

	var client gotmux.Client

	if os.Getenv("TMUX") != "" {
		info, err := cmdpicker.InvokingPickerClient(ctx, server)
		if err != nil {
			return nil, remoteCLIError("resolve_origin_return_client", err)
		}

		client = info.Handle()
	}

	return func(requested remote.OriginSession) error {
		rows, err := session.ListLive(ctx, server, "")
		if err != nil {
			return remoteCLIError("revalidate_origin_return_catalog", err)
		}

		for _, row := range rows {
			if !sameOriginTarget(action.RefOf(row), requested) {
				continue
			}

			handle, err := server.Session(ctx, gotmux.SessionID(row.ID))
			if err != nil {
				return remoteCLIError("resolve_origin_return_session", err)
			}

			if client.Valid() {
				return remoteCLIError("switch_origin_session", client.Switch(ctx, handle, gotmux.SwitchOptions{ToggleReadOnly: false, PreserveEnvironment: false}))
			}

			return remoteCLIError("attach_origin_session", handle.Attach(ctx, gotmux.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, gotmux.AttachOptions{ReadOnly: false, PreserveEnvironment: false, Detach: gotmux.DetachModeNone, Dir: "", Flags: nil}))
		}

		return remoteCLIError("origin_return_action", remote.ErrTargetChanged)
	}, nil
}

func (adapter *originPickerAdapter) LoadOriginSessions(ctx context.Context) (string, []session.Session, error) {
	client, err := adapter.client(ctx)
	if errors.Is(err, remote.ErrBridgeSocketRequired) {
		return "", nil, nil
	}

	if err != nil {
		return "", nil, remoteCLIError("origin_picker_adapter_client", err)
	}

	catalog, err := client.GetCatalog(ctx)
	if err != nil {
		return "", nil, remoteCLIError("load_origin_picker_catalog", err)
	}

	rows := make([]session.Session, 0, len(catalog.Sessions))
	for _, target := range catalog.Sessions {
		rows = append(rows, session.Session{
			ID: target.ID, Name: target.Name, Kind: session.KindLive, Origin: session.OriginBridge,
			WindowCount: target.WindowCount, Attached: target.Attached, RemoteStatus: session.RemoteStatusAvailable,
			TmuxServerSocket: "", Generation: "", RepositorySource: "", NameRaw: nil, PathRaw: nil,
			Activity: time.Time{}, IsCurrent: false, ActivePaneID: "", ActiveWindowName: "", ActiveWindowIndex: "",
			ActivePanePath: "", ActivePaneCommand: "", ActivePaneTitle: "",
			AgentFailed: 0, AgentInterrupted: 0, AgentUnknown: 0, AgentIdleSince: time.Time{},
			AgentWaitingSince: time.Time{}, AgentActivityChangedAt: time.Time{}, Agents: nil,
			AgentTotal: target.AgentTotal, AgentActive: target.AgentActive, AgentWaiting: target.AgentWaiting,
			RuntimeIdentity: session.RuntimeIdentity{
				ConfigDigest: "", PeerID: "", StreamInstance: "", TmuxServerSocket: "",
				ConnectionID: catalog.OriginID, ConnectionLabel: catalog.OriginLabel,
				TmuxServerPID: target.TmuxServerPID, TmuxServerStartedAt: target.TmuxServerStartedAt,
				TmuxSessionCreatedAt: target.SessionCreatedAt,
			},
		})
	}

	return catalog.OriginLabel, rows, nil
}

// Accepts lets the picker on a peer switch the origin client that attached to it.
func (adapter *originPickerAdapter) Accepts(at action.Location, id action.ID) bool {
	return at.Kind == action.LocationOrigin && id == action.SessionSwitch.ID
}

func (adapter *originPickerAdapter) Dispatch(ctx context.Context, invocation action.Invocation) (any, error) {
	if !adapter.Accepts(invocation.At, invocation.Action) {
		return nil, action.Wrap(action.CodeUnsupported, action.ErrLocationRejected)
	}

	input, err := action.SessionSwitch.Input(invocation.Input)
	if err != nil {
		return nil, action.Wrap(action.CodeInvalidArgument, err)
	}

	output := action.SwitchedSession(input)

	client, err := adapter.client(ctx)
	if err != nil {
		return output, action.Wrap(action.CodeUnavailable, err)
	}

	catalog, err := client.GetCatalog(ctx)
	if err != nil {
		return output, action.Wrap(action.CodeUnavailable, remoteCLIError("refresh_origin_switch_catalog", err))
	}

	for _, candidate := range catalog.Sessions {
		if sameOriginTarget(input.Session, candidate) {
			return output, remoteCLIError("request_origin_switch", client.RequestSwitch(ctx, catalog.CatalogGeneration, candidate))
		}
	}

	return output, action.Wrap(action.CodeTargetChanged, remoteCLIError("request_origin_switch", remote.ErrTargetChanged))
}

func (adapter *originPickerAdapter) client(ctx context.Context) (*remote.BridgeClient, error) {
	server, err := integration.Open(adapter.socket)
	if err != nil {
		return nil, remoteCLIError("open_origin_picker_endpoint", err)
	}

	client, err := cmdpicker.InvokingPickerClient(ctx, server)
	if err != nil {
		return nil, remoteCLIError("resolve_origin_picker_client", err)
	}

	terminal, err := os.Open(client.TTY)
	if err != nil {
		return nil, remoteCLIError("open_origin_picker_terminal", err)
	}

	name, nameErr := remote.OriginAttachmentOption(terminal)
	if err := errors.Join(nameErr, terminal.Close()); err != nil {
		return nil, remoteCLIError("resolve_origin_attachment_binding", err)
	}

	value, err := server.Options().User(ctx, name)
	if err != nil {
		return nil, remoteCLIError("read_origin_attachment_binding", err)
	}

	id, present := value.Local.Get()
	if !present {
		return nil, remoteCLIError("origin_picker_adapter_client", remote.ErrBridgeSocketRequired)
	}

	capabilities, err := remote.CapabilityReader(config.GetXDGPaths().StateHome)
	if err != nil {
		return nil, remoteCLIError("open_origin_picker_capability", err)
	}

	record, readErr := remote.ReadCapability(capabilities, id, time.Now())
	if err := errors.Join(readErr, capabilities.Close()); err != nil {
		return nil, remoteCLIError("read_origin_picker_capability", err)
	}

	identity := client.Handle().ServerIdentity()
	if record.Target.TmuxServerSocket != adapter.socket || record.Target.TmuxServerPID != identity.PID ||
		!record.Target.TmuxServerStartedAt.Equal(identity.Started) {
		return nil, remoteCLIError("origin_picker_adapter_client", remote.ErrTargetChanged)
	}

	return remote.NewBridgeClient(record.BridgeSocket, record.Grant.Token), nil
}

func sameOriginTarget(ref action.SessionRef, target remote.OriginSession) bool {
	return ref.ID == target.ID && ref.Name == target.Name &&
		ref.ServerPID == target.TmuxServerPID &&
		ref.ServerStartedAt.Equal(target.TmuxServerStartedAt) &&
		ref.CreatedAt.Equal(target.SessionCreatedAt)
}

func newRemoteAttachCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "attach <connection> <session>", Short: "Attach to an exact cached remote session incarnation",
		Args: clibase.UsageArgs(cobra.ExactArgs(remoteAttachArgCount)), Annotations: remoteLiveAnnotations("remote_attach"), RunE: runRemoteAttach,
	}
	cmd.Flags().String("pane", "", "Exact remote pane ID to select")
	cmd.Flags().String("origin-socket", "", "Explicit local tmux socket exposed through the scoped reverse bridge")
	cmd.Flags().String("origin-label", "", "Fixed origin label shown by the remote peer")

	return cmd
}

func loadRemoteCatalog() (remote.Catalog, *privatefs.Dir, error) {
	path, err := remote.Dir(config.GetXDGPaths().CacheHome)
	if err != nil {
		return remote.Catalog{}, nil, remoteCLIError("resolve_remote_cache", err)
	}

	dir, err := openRemotePrivate(path)
	if err != nil {
		return remote.Catalog{}, nil, err
	}

	catalog, err := remote.LoadCatalog(dir)
	if err != nil {
		return remote.Catalog{}, dir, remoteCLIError("load_remote_catalog", err)
	}

	return catalog, dir, nil
}

func resolveRemoteTarget(catalog remote.Catalog, connectionID, selector string) (remote.ConnectionState, session.Session, error) {
	state, ok := catalog.Connections[connectionID]
	if !ok || !state.Connected {
		return remote.ConnectionState{}, session.Session{}, remoteCLIError("resolve_remote_target", errRemoteTargetMissing)
	}

	for _, target := range remote.CatalogSessions(state) {
		if target.ID == selector || target.Name == selector {
			return state, target, nil
		}
	}

	return remote.ConnectionState{}, session.Session{}, remoteCLIError("resolve_remote_target", errRemoteTargetMissing)
}

func remoteAttachTarget(state remote.ConnectionState, target session.Session, paneID string) remote.AttachTarget {
	return remote.AttachTarget{
		PeerID: state.PeerID, TmuxServerSocket: target.TmuxServerSocket,
		TmuxServerPID: target.RuntimeIdentity.TmuxServerPID, TmuxServerStartedAt: target.RuntimeIdentity.TmuxServerStartedAt,
		SessionID: target.ID, SessionName: target.Name, SessionCreatedAt: target.RuntimeIdentity.TmuxSessionCreatedAt,
		PaneID: paneID, CatalogSequence: state.Sequence,
	}
}

func originSessions(ctx context.Context, socket string) ([]remote.OriginSession, error) {
	server, err := integration.Open(socket)
	if err != nil {
		return nil, remoteCLIError("open_origin_tmux", err)
	}

	rows, err := session.ListLive(ctx, server, "")
	if err != nil {
		return nil, remoteCLIError("list_origin_sessions", err)
	}

	result := make([]remote.OriginSession, 0, len(rows))
	for _, row := range rows {
		active, waiting := 0, 0

		for _, agent := range row.Agents {
			switch agent.Status {
			case session.AgentStatusRunning:
				active++
			case session.AgentStatusWaiting:
				waiting++
			case session.AgentStatusIdle, session.AgentStatusFailed, session.AgentStatusInterrupted, session.AgentStatusUnknown:
			}
		}

		result = append(result, remote.OriginSession{
			ID: row.ID, Name: row.Name, WindowCount: row.WindowCount,
			Attached: row.Attached, AgentTotal: len(row.Agents), AgentActive: active, AgentWaiting: waiting,
			TmuxServerPID: row.RuntimeIdentity.TmuxServerPID, TmuxServerStartedAt: row.RuntimeIdentity.TmuxServerStartedAt,
			SessionCreatedAt: row.RuntimeIdentity.TmuxSessionCreatedAt,
		})
	}

	return result, nil
}

func openRemoteOriginBridge(
	ctx context.Context,
	cfg *config.Config,
	connection config.RemoteConnectionConfig,
	state remote.ConnectionState,
	target session.Session,
	paneID string,
	originSocket string,
	originLabel string,
) (remote.AttachBootstrap, *remote.BridgeServer, error) {
	localDirPath, err := remote.Dir(config.GetXDGPaths().CacheHome)
	if err != nil {
		return remote.AttachBootstrap{}, nil, remoteCLIError("resolve_remote_cache", err)
	}

	localDirPath = filepath.Join(localDirPath, "bridge")

	localDir, err := privatefs.Ensure(localDirPath)
	if err != nil {
		return remote.AttachBootstrap{}, nil, remoteCLIError("ensure_origin_bridge", err)
	}

	if err := localDir.Close(); err != nil {
		return remote.AttachBootstrap{}, nil, remoteCLIError("close_origin_bridge_dir", err)
	}

	localID, err := remote.NewAttachmentID()
	if err != nil {
		return remote.AttachBootstrap{}, nil, remoteCLIError("create_remote_attachment_id", err)
	}

	localSocket := filepath.Join(localDirPath, "origin-"+localID+".sock")

	bootstrap, err := remote.PrepareAttachment(remote.AttachOriginOptions{
		Remote:       cfg.Remote,
		Connection:   connection,
		BridgeDir:    state.BridgeDir,
		OriginSocket: localSocket,
		OriginLabel:  originLabel,
		Target:       remoteAttachTarget(state, target, paneID),
		Timeout:      remoteAttachmentLifetime,
		Now:          nil,
	})
	if err != nil {
		return remote.AttachBootstrap{}, nil, remoteCLIError("prepare_remote_attachment", err)
	}

	bridge, err := remote.NewBridgeServer(ctx, remote.BridgeServerConfig{
		SocketPath:  localSocket,
		Token:       bootstrap.Capability.Token,
		ExpiresAt:   bootstrap.Capability.ExpiresAt,
		OriginID:    "local",
		OriginLabel: originLabel,
		ListSessions: func() ([]remote.OriginSession, error) {
			if !cfg.Remote.EnableReverseBridge {
				return nil, nil
			}

			return originSessions(ctx, originSocket)
		},
		Now: nil,
	})
	if err != nil {
		return remote.AttachBootstrap{}, nil, remoteCLIError("open_origin_bridge", err)
	}

	return bootstrap, bridge, nil
}

func runRemoteAttachSession(
	cmd *cobra.Command,
	cfg *config.Config,
	connection config.RemoteConnectionConfig,
	bootstrap remote.AttachBootstrap,
	bridge *remote.BridgeServer,
	originSocket string,
) error {
	var finish func(remote.OriginSession) error

	if cfg.Remote.EnableReverseBridge {
		var err error

		finish, err = originReturnAction(clibase.CommandContext(cmd), originSocket)
		if err != nil {
			return err
		}
	}

	if _, err := remote.RunAttachPrepare(clibase.CommandContext(cmd), cfg.Remote, connection, bootstrap, remote.AttachCommandOptions{
		Streams: remote.AttachStreams{In: nil, Out: nil, Err: nil},
		Command: nil,
	}); err != nil {
		return remoteCLIError("run_remote_attach_prepare", err)
	}

	in, out, errFile, err := remoteTerminalFiles(cmd)
	if err != nil {
		return err
	}

	attach, err := remote.NewAttachCommand(clibase.CommandContext(cmd), cfg.Remote, connection, bootstrap, remote.AttachCommandOptions{
		Streams: remote.AttachStreams{In: in, Out: out, Err: errFile},
		Command: nil,
	})
	if err != nil {
		return remoteCLIError("build_remote_attach", err)
	}

	if err := attach.Run(); err != nil {
		return remoteCLIError("run_remote_attach", err)
	}

	requested, ok := bridge.SwitchTarget()
	if !ok || finish == nil {
		return nil
	}

	return finish(requested)
}

func runRemoteAttach(cmd *cobra.Command, args []string) (resultErr error) {
	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	connection, err := remote.FindConnection(cfg.Remote, args[0])
	if err != nil {
		return remoteCLIError("find_remote_connection", err)
	}

	catalog, dir, err := loadRemoteCatalog()
	if dir != nil {
		defer func() { resultErr = errors.Join(resultErr, dir.Close()) }()
	}

	if err != nil {
		return err
	}

	state, target, err := resolveRemoteTarget(catalog, args[0], args[1])
	if err != nil {
		return err
	}

	paneID, _ := cmd.Flags().GetString("pane")

	originSocket, _ := cmd.Flags().GetString("origin-socket")
	if !filepath.IsAbs(originSocket) {
		return clibase.NewExitError(clibase.ExitCodeUsage, remoteCLIError("validate_origin_socket", errOriginSocketNotAbsolute))
	}

	originLabel, _ := cmd.Flags().GetString("origin-label")
	if originLabel == "" {
		originLabel = "origin"
	}

	bootstrap, bridge, err := openRemoteOriginBridge(
		clibase.CommandContext(cmd), cfg, connection, state, target, paneID, originSocket, originLabel,
	)
	if err != nil {
		return err
	}

	defer func() { resultErr = errors.Join(resultErr, remoteCLIError("close_origin_bridge", bridge.Close())) }()

	return runRemoteAttachSession(cmd, cfg, connection, bootstrap, bridge, originSocket)
}
