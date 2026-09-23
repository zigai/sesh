//go:build linux || darwin

package automation

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

const DefaultSessionName = "0"

const BootstrapOption = "@sesh-bootstrap-receipt"

const (
	DefaultSessionRenamed     = "renamed"
	DefaultSessionSwitched    = "switched"
	DefaultSessionUnchanged   = "unchanged"
	DefaultSessionUnsupported = "unsupported"
)

const bootstrapTokenFields = 5

var (
	ErrDefaultSessionServerRequired = errors.New("tmux server is required to adopt the default session")
	ErrDefaultSessionRename         = errors.New("rename startup scratch session")
	ErrDefaultSessionSwitch         = errors.New("switch client to default session")
	ErrDefaultSessionKill           = errors.New("kill startup scratch session")
	ErrInvalidBootstrapReceipt      = errors.New("invalid bootstrap receipt token")
	ErrBootstrapInitialPaneNotFound = errors.New("initial pane not found in bootstrap session")
)

type BootstrapReceipt struct {
	ServerIdentity string           `json:"serverIdentity"`
	SessionID      gotmux.SessionID `json:"sessionId"`
	PaneID         gotmux.PaneID    `json:"paneId"`
	PanePID        int              `json:"panePid"`
	InitialCommand string           `json:"initialCommand"`
}

type EnsureDefaultSessionResult struct {
	Changed     bool
	Action      string
	FromSession string
	ToSession   string
}

type DefaultSessionRequest struct {
	Current gotmux.SessionID
	Client  gotmux.Client
	Receipt BootstrapReceipt
	Now     time.Time
}

func (r BootstrapReceipt) Token() string {
	return fmt.Sprintf("%s|%s|%s|%d|%s", r.ServerIdentity, r.SessionID, r.PaneID, r.PanePID, r.InitialCommand)
}

func ParseBootstrapReceipt(token string) (BootstrapReceipt, error) {
	fields := strings.Split(token, "|")
	if len(fields) < bootstrapTokenFields {
		return BootstrapReceipt{
			ServerIdentity: "",
			SessionID:      "",
			PaneID:         "",
			PanePID:        0,
			InitialCommand: "",
		}, oops.In("automation").Code("invalid_receipt").With("operation", "parse_bootstrap_receipt").Wrap(ErrInvalidBootstrapReceipt)
	}

	pid, err := strconv.Atoi(fields[3])
	if err != nil {
		return BootstrapReceipt{
			ServerIdentity: "",
			SessionID:      "",
			PaneID:         "",
			PanePID:        0,
			InitialCommand: "",
		}, oops.In("automation").Code("invalid_receipt").With("operation", "parse_bootstrap_receipt").Wrap(errors.Join(ErrInvalidBootstrapReceipt, err))
	}

	return BootstrapReceipt{
		ServerIdentity: fields[0],
		SessionID:      gotmux.SessionID(fields[1]),
		PaneID:         gotmux.PaneID(fields[2]),
		PanePID:        pid,
		InitialCommand: strings.Join(fields[4:], "|"),
	}, nil
}

func CreateBootstrapSession(
	ctx context.Context,
	server *gotmux.Server,
	options gotmux.NewSessionOptions,
) (gotmux.Session, BootstrapReceipt, error) {
	if server == nil {
		return gotmux.Session{}, emptyBootstrapReceipt(), oops.In("automation").Code("server_required").With("operation", "create_bootstrap_session").Wrap(ErrDefaultSessionServerRequired)
	}

	probe, err := server.Probe(ctx)
	if err != nil {
		return gotmux.Session{}, emptyBootstrapReceipt(), oops.In("automation").Code("probe_failed").With("operation", "probe_server").Wrap(err)
	}

	session, err := server.NewSession(ctx, options)
	if err != nil {
		return gotmux.Session{}, emptyBootstrapReceipt(), oops.In("automation").Code("session_create_failed").With("operation", "create_bootstrap_session").Wrap(err)
	}

	panes, err := server.Panes(ctx)
	if err != nil {
		return session, emptyBootstrapReceipt(), oops.In("automation").Code("list_panes_failed").With("operation", "list_bootstrap_panes").Wrap(err)
	}

	var (
		initialPaneID  gotmux.PaneID
		initialPID     int
		initialCommand string
		found          bool
	)

	for _, pane := range panes {
		sessionID, ok := pane.SessionID.Get()
		if ok && sessionID == session.ID() {
			initialPaneID = pane.ID
			initialPID = pane.PID
			initialCommand = pane.CurrentCommand
			found = true

			break
		}
	}

	if !found {
		return session, emptyBootstrapReceipt(), oops.In("automation").Code("initial_pane_not_found").With("operation", "create_bootstrap_session").Wrap(ErrBootstrapInitialPaneNotFound)
	}

	receipt := BootstrapReceipt{
		ServerIdentity: serverIdentityString(probe.Identity),
		SessionID:      session.ID(),
		PaneID:         initialPaneID,
		PanePID:        initialPID,
		InitialCommand: initialCommand,
	}

	if err := session.Options().SetUser(ctx, BootstrapOption, receipt.Token()); err != nil {
		return session, receipt, oops.In("automation").Code("stamp_receipt_failed").With("operation", "stamp_bootstrap_receipt").Wrap(err)
	}

	return session, receipt, nil
}

func EnsureDefaultSession(
	ctx context.Context,
	server *gotmux.Server,
	request DefaultSessionRequest,
) (EnsureDefaultSessionResult, error) {
	if server == nil {
		return unchangedDefaultSession(), oops.In("automation").Code("server_required").With("operation", "ensure_default_session").Wrap(ErrDefaultSessionServerRequired)
	}

	if request.Current == "" {
		return unchangedDefaultSession(), nil
	}

	probe, err := server.Probe(ctx)
	if err != nil {
		return unsupportedDefaultSession(), oops.In("automation").Code("probe_failed").With("operation", "probe_server").Wrap(err)
	}

	if !matchesReceiptTarget(request, serverIdentityString(probe.Identity)) {
		return unsupportedDefaultSession(), nil
	}

	current, info, err := observedSession(ctx, server, request.Current)
	if err != nil || current == nil {
		return unchangedDefaultSession(), err
	}

	if info.Name == DefaultSessionName {
		return unchangedDefaultSession(), nil
	}

	if !verifyBootstrapOwnership(ctx, server, *current, *info, request.Receipt) {
		return unsupportedDefaultSession(), nil
	}

	exists, err := defaultSessionExists(ctx, server)
	if err != nil {
		return unchangedDefaultSession(), err
	}

	if exists {
		return switchToDefaultAndKillScratch(ctx, server, *current, *info, request.Client)
	}

	return renameScratchToDefault(ctx, *current, info.Name)
}

func matchesReceiptTarget(request DefaultSessionRequest, serverIdentity string) bool {
	return hasReceipt(request.Receipt) &&
		serverIdentity == request.Receipt.ServerIdentity &&
		request.Current == request.Receipt.SessionID
}

func verifyBootstrapOwnership(
	ctx context.Context,
	server *gotmux.Server,
	session gotmux.Session,
	info gotmux.SessionInfo,
	receipt BootstrapReceipt,
) bool {
	if !verifyBootstrapReceiptOption(ctx, session, receipt) {
		return false
	}

	return verifyBootstrapSessionShape(ctx, server, session, info, receipt)
}

func hasReceipt(r BootstrapReceipt) bool {
	return r.ServerIdentity != "" && r.SessionID != "" && r.PaneID != ""
}

func verifyBootstrapReceiptOption(ctx context.Context, session gotmux.Session, receipt BootstrapReceipt) bool {
	optVal, err := session.Options().User(ctx, BootstrapOption)
	if err != nil {
		return false
	}

	token, ok := optVal.Local.Get()
	if !ok {
		token, ok = optVal.Effective.Get()
	}

	return ok && token == receipt.Token()
}

func verifyBootstrapSessionShape(
	ctx context.Context,
	server *gotmux.Server,
	session gotmux.Session,
	info gotmux.SessionInfo,
	receipt BootstrapReceipt,
) bool {
	if info.WindowCount != 1 {
		return false
	}

	panes, err := server.Panes(ctx)
	if err != nil {
		return false
	}

	count := 0
	matched := false

	for _, pane := range panes {
		sessionID, ok := pane.SessionID.Get()
		if ok && sessionID == session.ID() {
			count++

			if pane.ID == receipt.PaneID && pane.PID == receipt.PanePID {
				matched = true
			}
		}
	}

	return count == 1 && matched
}

func unchangedDefaultSession() EnsureDefaultSessionResult {
	return EnsureDefaultSessionResult{
		Changed:     false,
		Action:      DefaultSessionUnchanged,
		FromSession: "",
		ToSession:   "",
	}
}

func unsupportedDefaultSession() EnsureDefaultSessionResult {
	return EnsureDefaultSessionResult{
		Changed:     false,
		Action:      DefaultSessionUnsupported,
		FromSession: "",
		ToSession:   "",
	}
}

func observedSession(
	ctx context.Context,
	server *gotmux.Server,
	id gotmux.SessionID,
) (*gotmux.Session, *gotmux.SessionInfo, error) {
	current, err := server.Session(ctx, id)
	if err != nil {
		if errors.Is(err, gotmux.ErrNotFound) || errors.Is(err, gotmux.ErrNoServer) {
			return nil, nil, nil
		}

		return nil, nil, oops.In("automation").Code("session_read_failed").With("operation", "read_current_session").Wrap(err)
	}

	info, err := current.Info(ctx)
	if err != nil {
		if errors.Is(err, gotmux.ErrNotFound) || errors.Is(err, gotmux.ErrNoServer) {
			return nil, nil, nil
		}

		return nil, nil, oops.In("automation").Code("session_inspect_failed").With("operation", "inspect_current_session").Wrap(err)
	}

	return &current, &info, nil
}

func defaultSessionExists(ctx context.Context, server *gotmux.Server) (bool, error) {
	sessions, err := server.Sessions(ctx)
	if err != nil {
		return false, oops.In("automation").Code("list_sessions_failed").With("operation", "list_sessions").Wrap(err)
	}

	for _, session := range sessions {
		if session.Name == DefaultSessionName {
			return true, nil
		}
	}

	return false, nil
}

func switchToDefaultAndKillScratch(
	ctx context.Context,
	server *gotmux.Server,
	scratch gotmux.Session,
	info gotmux.SessionInfo,
	client gotmux.Client,
) (EnsureDefaultSessionResult, error) {
	if client.Valid() {
		if err := switchClientToDefault(ctx, server, client); err != nil {
			return unchangedDefaultSession(), err
		}
	}

	if err := scratch.Kill(ctx); err != nil {
		return unchangedDefaultSession(), oops.In("automation").Code("session_kill_failed").With("operation", "kill_scratch_session").Wrap(errors.Join(ErrDefaultSessionKill, err))
	}

	return EnsureDefaultSessionResult{
		Changed:     true,
		Action:      DefaultSessionSwitched,
		FromSession: info.Name,
		ToSession:   DefaultSessionName,
	}, nil
}

func switchClientToDefault(ctx context.Context, server *gotmux.Server, client gotmux.Client) error {
	defaultSession, err := server.FindSession(ctx, DefaultSessionName)
	if err != nil {
		return oops.In("automation").Code("session_not_found").With("operation", "find_default_session").Wrap(err)
	}

	options := gotmux.SwitchOptions{ToggleReadOnly: false, PreserveEnvironment: false}

	if err := client.Switch(ctx, defaultSession, options); err != nil {
		return oops.In("automation").Code("switch_client_failed").With("operation", "switch_client_to_default").Wrap(errors.Join(ErrDefaultSessionSwitch, err))
	}

	return nil
}

func renameScratchToDefault(
	ctx context.Context,
	session gotmux.Session,
	name string,
) (EnsureDefaultSessionResult, error) {
	if err := session.Rename(ctx, DefaultSessionName); err != nil {
		return unchangedDefaultSession(), oops.In("automation").Code("session_rename_failed").With("operation", "rename_scratch_session").Wrap(errors.Join(ErrDefaultSessionRename, err))
	}

	return EnsureDefaultSessionResult{
		Changed:     true,
		Action:      DefaultSessionRenamed,
		FromSession: name,
		ToSession:   DefaultSessionName,
	}, nil
}

func serverIdentityString(id gotmux.ServerIdentity) string {
	return fmt.Sprintf("%d:%d:%s", id.PID, id.Started.Unix(), id.ReportedSocket)
}

func emptyBootstrapReceipt() BootstrapReceipt {
	return BootstrapReceipt{
		ServerIdentity: "",
		SessionID:      "",
		PaneID:         "",
		PanePID:        0,
		InitialCommand: "",
	}
}
