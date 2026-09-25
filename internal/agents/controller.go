//go:build linux || darwin

package agents

import (
	"context"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

const defaultLockTimeout = 3 * time.Second

type Controller interface {
	CurrentContext(ctx context.Context) (CurrentContext, error)
	Panes(ctx context.Context) ([]PaneLocation, error)
	SessionWindows(ctx context.Context, sessionID string) ([]SessionWindow, error)
	Activate(ctx context.Context, current CurrentContext, target PaneLocation) error
	History(ctx context.Context, clientKey string) (string, error)
	SetHistory(ctx context.Context, clientKey string, raw string) error
	Lock(ctx context.Context, channel string) error
	Unlock(ctx context.Context, channel string) error
}

type tmuxController struct {
	server    *gotmux.Server
	target    CurrentTarget
	getenv    func(string) string
	mu        sync.Mutex
	serverPID int
}

func NewTmuxController(server *gotmux.Server, target CurrentTarget) Controller {
	return &tmuxController{server: server, target: target, getenv: os.Getenv, mu: sync.Mutex{}, serverPID: 0}
}

func (c *tmuxController) CurrentContext(ctx context.Context) (CurrentContext, error) {
	if c.server == nil {
		return CurrentContext{}, oops.In("agents").Code("endpoint_required").With("operation", "current_context").Wrap(ErrServerRequired)
	}

	switch {
	case strings.TrimSpace(c.target.PaneID) != "":
		return c.contextFromPaneID(ctx, strings.TrimSpace(c.target.PaneID))
	case strings.TrimSpace(c.target.Client) != "":
		return c.contextFromClient(ctx, strings.TrimSpace(c.target.Client))
	case strings.TrimSpace(c.target.SessionID) != "":
		return c.contextFromSessionID(ctx, gotmux.SessionID(strings.TrimSpace(c.target.SessionID)))
	case strings.TrimSpace(c.target.Session) != "":
		return c.contextFromSessionName(ctx, strings.TrimSpace(c.target.Session))
	}

	if ambient, err := c.ambientContext(ctx); err == nil {
		return ambient, nil
	}

	return c.soleClientContext(ctx)
}

func (c *tmuxController) Panes(ctx context.Context) ([]PaneLocation, error) {
	return ObservePanes(ctx, c.server)
}

func (c *tmuxController) SessionWindows(ctx context.Context, sessionID string) ([]SessionWindow, error) {
	session, err := c.server.Session(ctx, gotmux.SessionID(sessionID))
	if err != nil {
		return nil, oops.In("agents").Code("session_not_found").With("operation", "session_windows").Wrap(err)
	}

	info, err := session.Info(ctx)
	if err != nil {
		return nil, oops.In("agents").Code("tmux_failed").With("operation", "session_info").Wrap(err)
	}

	links, err := session.Windows(ctx)
	if err != nil {
		return nil, oops.In("agents").Code("tmux_failed").With("operation", "session_windows").Wrap(err)
	}

	windows := make([]SessionWindow, 0, len(links))

	for _, link := range links {
		linkInfo, linkErr := link.Info(ctx)
		if linkErr != nil {
			return nil, oops.In("agents").Code("tmux_failed").With("operation", "window_info").Wrap(linkErr)
		}

		windows = append(windows, SessionWindow{
			SessionID:   string(linkInfo.SessionID),
			SessionName: info.Name,
			WindowID:    string(linkInfo.WindowID),
			WindowIndex: strconv.Itoa(linkInfo.Index),
		})
	}

	return windows, nil
}

func (c *tmuxController) Activate(ctx context.Context, current CurrentContext, target PaneLocation) error {
	if target.SessionID == "" || target.WindowID == "" || target.PaneID == "" {
		return oops.In("agents").Code("invalid_target").With("operation", "activate").Wrap(ErrPaneNotLive)
	}

	if err := c.switchSession(ctx, current, target); err != nil {
		return err
	}

	if err := c.selectWindow(ctx, current, target); err != nil {
		return err
	}

	return c.selectPane(ctx, target)
}

func (c *tmuxController) History(ctx context.Context, clientKey string) (string, error) {
	option := historyOptionName(clientKey)

	value, err := c.server.GlobalSessionOptions().User(ctx, option)
	if err != nil {
		return "", oops.In("agents").Code("tmux_failed").With("operation", "read_history").Wrap(err)
	}

	raw, ok := value.Effective.Get()
	if !ok {
		return "", nil
	}

	return raw, nil
}

func (c *tmuxController) SetHistory(ctx context.Context, clientKey string, raw string) error {
	option := historyOptionName(clientKey)
	options := c.server.GlobalSessionOptions()

	if raw == "" {
		if err := options.UnsetUser(ctx, option); err != nil {
			return oops.In("agents").Code("tmux_failed").With("operation", "write_history").Wrap(err)
		}

		return nil
	}

	if err := options.SetUser(ctx, option, raw); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "write_history").Wrap(err)
	}

	return nil
}

func (c *tmuxController) Lock(ctx context.Context, channel string) error {
	if c.server == nil {
		return oops.In("agents").Code("endpoint_required").With("operation", "lock").Wrap(ErrServerRequired)
	}

	lockCtx := ctx

	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		lockCtx, cancel = context.WithTimeout(ctx, defaultLockTimeout)
		defer cancel()
	}

	if err := c.server.Lock(lockCtx, channel); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "lock").Wrap(err)
	}

	return nil
}

func (c *tmuxController) Unlock(ctx context.Context, channel string) error {
	if c.server == nil {
		return oops.In("agents").Code("endpoint_required").With("operation", "unlock").Wrap(ErrServerRequired)
	}

	if err := c.server.Unlock(ctx, channel); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "unlock").Wrap(err)
	}

	return nil
}

func (c *tmuxController) contextFromPaneID(ctx context.Context, paneID string) (CurrentContext, error) {
	panes, err := c.Panes(ctx)
	if err != nil {
		return CurrentContext{}, err
	}

	pane, ok := c.paneLinkForTarget(ctx, panes, paneID)
	if !ok {
		return CurrentContext{}, oops.In("agents").Code("pane_not_live").With("operation", "current_context").Wrap(ErrPaneNotLive)
	}

	return contextFromPane(pane, c.soleClientForSession(ctx, pane.SessionID), c.resolveServerPID(ctx)), nil
}

func (c *tmuxController) paneLinkForTarget(ctx context.Context, panes []PaneLocation, paneID string) (PaneLocation, bool) {
	links := FindPanes(panes, paneID, c.socketPath())
	if len(links) == 0 {
		return emptyPaneLocation, false
	}

	requested := strings.TrimSpace(c.target.SessionID)
	if requested == "" {
		requested = strings.TrimSpace(c.target.Session)
	}

	if requested == "" {
		return links[0], true
	}

	for _, link := range links {
		if link.SessionID == requested || link.SessionName == requested {
			return link, true
		}
	}

	sessionID, err := c.sessionIDFor(ctx, requested)
	if err != nil {
		return links[0], true
	}

	windows, err := c.SessionWindows(ctx, sessionID)
	if err != nil {
		return links[0], true
	}

	for _, window := range windows {
		if window.WindowID == links[0].WindowID {
			return retargetToWindow(links[0], window), true
		}
	}

	return links[0], true
}

func (c *tmuxController) sessionIDFor(ctx context.Context, requested string) (string, error) {
	if strings.HasPrefix(requested, "$") {
		return requested, nil
	}

	session, err := c.server.FindSession(ctx, requested)
	if err != nil {
		return "", oops.In("agents").Code("session_not_found").With("operation", "resolve_session").Wrap(err)
	}

	return string(session.ID()), nil
}

func (c *tmuxController) contextFromClient(ctx context.Context, name string) (CurrentContext, error) {
	client, err := c.server.Client(ctx, gotmux.ClientName(name))
	if err != nil {
		return CurrentContext{}, oops.In("agents").Code("client_not_found").With("operation", "current_context").Wrap(err)
	}

	info, err := client.Info(ctx)
	if err != nil {
		return CurrentContext{}, oops.In("agents").Code("tmux_failed").With("operation", "client_info").Wrap(err)
	}

	sessionID, ok := info.SessionID.Get()
	if !ok {
		return CurrentContext{}, oops.In("agents").Code("client_detached").With("operation", "current_context").Wrap(ErrCurrentContextUnknown)
	}

	context, err := c.contextFromSessionID(ctx, sessionID)
	if err != nil {
		return CurrentContext{}, err
	}

	context.ClientName = string(info.Name)
	context.ClientTTY = info.TTY

	return context, nil
}

func (c *tmuxController) contextFromSessionID(ctx context.Context, sessionID gotmux.SessionID) (CurrentContext, error) {
	session, err := c.server.Session(ctx, sessionID)
	if err != nil {
		return CurrentContext{}, oops.In("agents").Code("session_not_found").With("operation", "current_context").Wrap(err)
	}

	return c.contextFromSession(ctx, session)
}

func (c *tmuxController) contextFromSessionName(ctx context.Context, name string) (CurrentContext, error) {
	session, err := c.server.FindSession(ctx, name)
	if err != nil {
		return CurrentContext{}, oops.In("agents").Code("session_not_found").With("operation", "current_context").Wrap(err)
	}

	return c.contextFromSession(ctx, session)
}

func (c *tmuxController) contextFromSession(ctx context.Context, session gotmux.Session) (CurrentContext, error) {
	links, err := session.Windows(ctx)
	if err != nil {
		return CurrentContext{}, oops.In("agents").Code("tmux_failed").With("operation", "session_windows").Wrap(err)
	}

	for _, link := range links {
		info, infoErr := link.Info(ctx)
		if infoErr != nil {
			return CurrentContext{}, oops.In("agents").Code("tmux_failed").With("operation", "window_info").Wrap(infoErr)
		}

		if !info.Active {
			continue
		}

		pane, paneErr := link.Window().ActivePane(ctx)
		if paneErr != nil {
			return CurrentContext{}, oops.In("agents").Code("tmux_failed").With("operation", "active_pane").Wrap(paneErr)
		}

		paneInfo, paneErr := pane.Info(ctx)
		if paneErr != nil {
			return CurrentContext{}, oops.In("agents").Code("tmux_failed").With("operation", "pane_info").Wrap(paneErr)
		}

		context := CurrentContext{
			ServerSocket: c.socketPath(),
			ClientName:   "",
			ClientTTY:    "",
			SessionID:    string(info.SessionID),
			SessionName:  "",
			WindowID:     string(info.WindowID),
			PaneID:       string(paneInfo.ID),
			PanePID:      paneInfo.PID,
			ServerPID:    c.resolveServerPID(ctx),
		}

		if sessionInfo, sessionErr := session.Info(ctx); sessionErr == nil {
			context.SessionName = sessionInfo.Name
		}

		context.ClientName = c.soleClientForSession(ctx, context.SessionID)

		return context, nil
	}

	return CurrentContext{}, oops.In("agents").Code("no_active_window").With("operation", "current_context").Wrap(ErrCurrentContextUnknown)
}

func (c *tmuxController) ambientContext(ctx context.Context) (CurrentContext, error) {
	getenv := c.getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	info, err := c.server.CurrentWithEnv(ctx, gotmux.Environment{TMUX: getenv("TMUX"), TMUXPane: getenv("TMUX_PANE")})
	if err != nil {
		return CurrentContext{}, oops.In("agents").Code("ambient_unverified").With("operation", "current_context").Wrap(err)
	}

	if session, ok := info.Session.Get(); ok && !c.paneIsLive(ctx, string(info.Pane.ID)) {
		return c.contextFromSessionID(ctx, session.ID)
	}

	context := CurrentContext{
		ServerSocket: c.socketPath(),
		ClientName:   "",
		ClientTTY:    "",
		SessionID:    "",
		SessionName:  "",
		WindowID:     string(info.Window.ID),
		PaneID:       string(info.Pane.ID),
		PanePID:      info.Pane.PID,
		ServerPID:    c.resolveServerPID(ctx),
	}

	if session, ok := info.Session.Get(); ok {
		context.SessionID = string(session.ID)
		context.SessionName = session.Name
	}

	if link, ok := info.Link.Get(); ok && context.SessionID == "" {
		context.SessionID = string(link.SessionID)
	}

	if !context.Valid() {
		return CurrentContext{}, oops.In("agents").Code("ambient_incomplete").With("operation", "current_context").Wrap(ErrCurrentContextUnknown)
	}

	context.ClientName = c.soleClientForSession(ctx, context.SessionID)

	return context, nil
}

func (c *tmuxController) paneIsLive(ctx context.Context, paneID string) bool {
	if paneID == "" {
		return false
	}

	panes, err := c.Panes(ctx)
	if err != nil {
		return false
	}

	_, ok := FindPane(panes, paneID, c.socketPath())

	return ok
}

func (c *tmuxController) soleClientContext(ctx context.Context) (CurrentContext, error) {
	clients, err := c.server.Clients(ctx)
	if err != nil {
		return CurrentContext{}, oops.In("agents").Code("tmux_failed").With("operation", "list_clients").Wrap(err)
	}

	name := ""

	for _, client := range clients {
		if client.Control {
			continue
		}

		if name != "" {
			return CurrentContext{}, oops.In("agents").Code("ambiguous_context").With("operation", "current_context").Wrap(ErrCurrentContextUnknown)
		}

		name = string(client.Name)
	}

	if name == "" {
		return CurrentContext{}, oops.In("agents").Code("no_client").With("operation", "current_context").Wrap(ErrCurrentContextUnknown)
	}

	return c.contextFromClient(ctx, name)
}

func (c *tmuxController) soleClientForSession(ctx context.Context, sessionID string) string {
	if sessionID == "" {
		return ""
	}

	clients, err := c.server.Clients(ctx)
	if err != nil {
		return ""
	}

	name := ""

	for _, client := range clients {
		if client.Control {
			continue
		}

		clientSession, ok := client.SessionID.Get()
		if !ok || string(clientSession) != sessionID {
			continue
		}

		if name != "" {
			return ""
		}

		name = string(client.Name)
	}

	return name
}

func (c *tmuxController) switchSession(ctx context.Context, current CurrentContext, target PaneLocation) error {
	if current.SessionID == target.SessionID {
		return nil
	}

	session, err := c.server.Session(ctx, gotmux.SessionID(target.SessionID))
	if err != nil {
		return oops.In("agents").Code("invalid_target").With("operation", "switch_client").Wrap(err)
	}

	clientName := current.ClientName
	if clientName == "" {
		clientName = c.soleClientForSession(ctx, current.SessionID)
	}

	if clientName != "" {
		client, err := c.server.Client(ctx, gotmux.ClientName(clientName))
		if err != nil {
			return oops.In("agents").Code("client_not_found").With("operation", "switch_client").Wrap(err)
		}

		if err := client.Switch(ctx, session, gotmux.SwitchOptions{ToggleReadOnly: false, PreserveEnvironment: false}); err != nil {
			return oops.In("agents").Code("tmux_failed").With("operation", "switch_client").Wrap(err)
		}

		return nil
	}

	command, err := gotmux.NewCommand("switch-client", "-t", target.SessionID)
	if err != nil {
		return oops.In("agents").Code("invalid_command").With("operation", "switch_client").Wrap(err)
	}

	if err := c.runCommands(ctx, command); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "switch_client").Wrap(err)
	}

	return nil
}

func (c *tmuxController) selectWindow(ctx context.Context, current CurrentContext, target PaneLocation) error {
	if current.WindowID == target.WindowID {
		return nil
	}

	window, err := c.server.Window(ctx, gotmux.WindowID(target.WindowID))
	if err != nil {
		return oops.In("agents").Code("invalid_target").With("operation", "select_window").Wrap(err)
	}

	if err := window.SelectWith(ctx, gotmux.SelectWindowOptions{PreserveZoom: false, Last: false}); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "select_window").Wrap(err)
	}

	return nil
}

func (c *tmuxController) selectPane(ctx context.Context, target PaneLocation) error {
	pane, err := c.server.Pane(ctx, gotmux.PaneID(target.PaneID))
	if err != nil {
		return oops.In("agents").Code("invalid_target").With("operation", "select_pane").Wrap(err)
	}

	if err := pane.Select(ctx); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "select_pane").Wrap(err)
	}

	return nil
}

func (c *tmuxController) runCommands(ctx context.Context, commands ...gotmux.Command) error {
	if len(commands) == 0 {
		return nil
	}

	sequence, err := gotmux.Sequence(commands...)
	if err != nil {
		return oops.In("agents").Code("invalid_command").With("operation", "run_sequence").Wrap(err)
	}

	if _, err := c.server.RunSequence(ctx, sequence); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "run_sequence").Wrap(err)
	}

	return nil
}

func (c *tmuxController) socketPath() string {
	if c.server == nil {
		return ""
	}

	return NormalizeServerSocket(c.server.Endpoint().SocketPath)
}

func (c *tmuxController) resolveServerPID(ctx context.Context) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.serverPID > 0 {
		return c.serverPID
	}

	if c.server != nil {
		info, err := c.server.Probe(ctx)
		if err == nil && info.Identity.PID > 0 {
			c.serverPID = info.Identity.PID

			return c.serverPID
		}
	}

	return 0
}

func historyOptionName(clientKey string) string {
	if clientKey == "" || clientKey == "global" {
		return navigationHistoryOption
	}

	return "@sesh-agent-history-" + hex.EncodeToString([]byte(clientKey))
}
