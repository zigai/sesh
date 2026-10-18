package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/inbox"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/work"
)

const (
	EnterCompleted EnterAction = iota
	EnterNeedsAttach
	EnterNeedsSwitch
)

var (
	errIncompleteServices = errors.New("incomplete TUI services")
	errEnterUnavailable   = errors.New("enter session is unavailable: no session enterer configured")
)

type EnterAction uint8

type EnterOutcome struct {
	SessionName string
	Action      EnterAction
	Restored    bool
	Target      session.Session
}

type CatalogSource interface {
	LoadCatalog(ctx context.Context) (session.CatalogResult, error)
}

type InitialLoadFinisher interface {
	FinishInitialLoad()
}

type TmuxDetector interface {
	TmuxIsInsideTmux() bool
}

type SessionEnterer interface {
	EnterSession(ctx context.Context, target session.Session) (EnterOutcome, error)
}

type TerminalAttacher interface {
	AttachSession(ctx context.Context, target session.Session) (tea.ExecCommand, error)
}

type PaneService interface {
	TmuxActivatePane(ctx context.Context, target session.Session, paneID string) error
	TmuxPaneLocations(ctx context.Context) ([]PaneLocation, error)
}

type AgentSnapshot struct {
	Summaries     []session.AgentSummary
	Panes         []PaneLocation
	PanesObserved bool
}

type AgentSource interface {
	LoadAgentSummaries(ctx context.Context) (AgentSnapshot, error)
}

type AgentWatcher interface {
	WatchAgentSummaries(
		ctx context.Context,
		publish func(revision uint64, snapshot AgentSnapshot, err error) error,
	) error
}

type RemoteCatalogSource interface {
	LoadRemoteCatalog() (RemoteCatalog, error)
}

type RemoteCatalogWatcher interface {
	WatchRemoteCatalog(ctx context.Context, publish func(RemoteCatalog, error)) error
}

type RemoteAttacher interface {
	AttachRemoteSession(ctx context.Context, target session.Session, paneID string) (tea.ExecCommand, error)
}

type OriginBridgeService interface {
	LoadOriginSessions(ctx context.Context) (originLabel string, sessions []session.Session, err error)
}

type WorkingDirectoryService interface {
	GetWorkingDirectory() (string, error)
}

// InboxStore keeps what the inbox remembers between pickers: the rows the user snoozed.
type InboxStore interface {
	LoadInbox(ctx context.Context) (inbox.State, error)
	UpdateInbox(ctx context.Context, change func(*inbox.State) error) (inbox.State, error)
}

type StateStore interface {
	UpdateModelState(ctx context.Context, update session.StateUpdate) error
}

// AgentViewDeferrer is an optional StateStore capability. A store that returns true takes
// ownership of persisting the views after the model exits, so a popup never holds its
// close on a history write.
type AgentViewDeferrer interface {
	DeferAgentViews(views []AgentView) bool
}

// AgentView records that the agent pane of a session was on screen.
type AgentView struct {
	Session session.Identity
	PaneID  string
}

// WorkSource lists the conversations recorded on this machine and brings their branch and pull
// request up to date, reporting whether anything changed.
type WorkSource interface {
	LoadWork(ctx context.Context) ([]work.Item, error)
	SyncWork(ctx context.Context) (bool, error)
}

type Services struct {
	Catalog          CatalogSource
	Detector         TmuxDetector
	Enterer          SessionEnterer
	Actions          action.Dispatcher
	Panes            PaneService
	TerminalAttach   TerminalAttacher
	Agents           AgentSource
	Tasks            TaskSource
	Work             WorkSource
	Inbox            InboxStore
	Evidence         EvidenceSource
	AgentWatcher     AgentWatcher
	Remote           RemoteCatalogSource
	RemoteWatcher    RemoteCatalogWatcher
	RemoteAttach     RemoteAttacher
	OriginBridge     OriginBridgeService
	WorkingDirectory WorkingDirectoryService
	State            StateStore
}

func validateServices(services Services) error {
	if services.Catalog == nil ||
		services.Detector == nil ||
		services.Panes == nil ||
		services.WorkingDirectory == nil ||
		services.State == nil {
		return errIncompleteServices
	}

	return nil
}
