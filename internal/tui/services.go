package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

const (
	EnterCompleted EnterAction = iota
	EnterNeedsAttach
	EnterNeedsSwitch
)

var (
	errIncompleteServices    = errors.New("incomplete TUI services")
	errDeleteUnavailable     = errors.New("delete session is unavailable: no session deleter configured")
	errRenameUnavailable     = errors.New("rename session is unavailable: no session renamer configured")
	errKillUnavailable       = errors.New("kill session is unavailable: no session killer configured")
	errEnterUnavailable      = errors.New("enter session is unavailable: no session enterer configured")
	errSnapshotUnavailable   = errors.New("snapshot session is unavailable: no snapshot service configured")
	errRemoteActionsDisabled = errors.New("remote actions are not available: no remote action service configured")
	errOriginBridgeDisabled  = errors.New("origin bridge is not available: no origin bridge service configured")
)

type EnterAction uint8

type EnterOutcome struct {
	SessionName string
	Action      EnterAction
	Restored    bool
	Target      session.Session
}

type DeleteResult struct {
	LiveKilled   bool
	SavedDeleted bool
}

type OutcomeOwnedError interface {
	error
	PickerOutcomeOwned() bool
}

type DegradedOutcomeError interface {
	OutcomeOwnedError
	PickerDegradedOutcome() bool
}

type CatalogSource interface {
	LoadCatalog(ctx context.Context) (session.CatalogResult, error)
}

type TmuxDetector interface {
	TmuxIsInsideTmux() bool
}

type SessionEnterer interface {
	EnterSession(ctx context.Context, target session.Session) (EnterOutcome, error)
	CreateAndEnterSession(ctx context.Context, name, dir string) (EnterOutcome, error)
	RestoreAndEnterSession(ctx context.Context, target session.Session) (EnterOutcome, error)
}

type SessionRenamer interface {
	RenameSession(ctx context.Context, target session.Session, newName string) error
}

type SessionKiller interface {
	KillSession(ctx context.Context, target session.Session) error
}

type SessionDeleter interface {
	DeleteSessionEverywhere(ctx context.Context, target session.Session, force bool) (DeleteResult, error)
}

type TerminalAttacher interface {
	AttachSession(ctx context.Context, target session.Session) (tea.ExecCommand, error)
}

type PaneService interface {
	TmuxActivatePane(ctx context.Context, target session.Session, paneID string) error
	TmuxPaneLocations(ctx context.Context) ([]PaneLocation, error)
}

type SnapshotService interface {
	SnapshotSession(ctx context.Context, target session.Session) error
}

type AgentSource interface {
	LoadAgentSummaries(ctx context.Context) ([]session.AgentSummary, error)
}

type AgentWatcher interface {
	WatchAgentSummaries(
		ctx context.Context,
		publish func(revision uint64, summaries []session.AgentSummary, err error) error,
	) error
}

type RemoteCatalogSource interface {
	LoadRemoteCatalog() (RemoteCatalog, error)
}

type RemoteCatalogWatcher interface {
	WatchRemoteCatalog(ctx context.Context, publish func(RemoteCatalog, error)) error
}

type RemoteActionService interface {
	RunRemoteAction(ctx context.Context, cfg *config.Config, target session.Session, request RemoteActionRequest) error
	IsActionPermitted(cfg *config.Config, target session.Session, action string) bool
}

type RemoteActionRequest struct {
	Action    string
	NewName   string
	Directory string
}

type RemoteAttacher interface {
	AttachRemoteSession(ctx context.Context, target session.Session, paneID string) (tea.ExecCommand, error)
}

type OriginBridgeService interface {
	LoadOriginSessions(ctx context.Context) (originLabel string, sessions []session.Session, err error)
	RequestOriginSwitch(ctx context.Context, target session.Session) error
}

type WorkingDirectoryService interface {
	GetWorkingDirectory() (string, error)
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

type Services struct {
	Catalog          CatalogSource
	Detector         TmuxDetector
	Enterer          SessionEnterer
	Renamer          SessionRenamer
	Killer           SessionKiller
	Deleter          SessionDeleter
	Panes            PaneService
	TerminalAttach   TerminalAttacher
	Agents           AgentSource
	AgentWatcher     AgentWatcher
	Snapshots        SnapshotService
	Remote           RemoteCatalogSource
	RemoteWatcher    RemoteCatalogWatcher
	RemoteActions    RemoteActionService
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
