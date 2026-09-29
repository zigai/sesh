package session

import (
	"strconv"
	"strings"
	"time"

	"github.com/zigai/aht/v2/pkg/aht"
)

const (
	KindLive  Kind = "live"
	KindSaved Kind = "saved"
)

const (
	OriginLocal  Origin = "local"
	OriginSSH    Origin = "ssh"
	OriginBridge Origin = "bridge"
)

const (
	RemoteStatusAvailable    RemoteStatus = ""
	RemoteStatusConnecting   RemoteStatus = "connecting"
	RemoteStatusUnavailable  RemoteStatus = "unavailable"
	RemoteStatusDegraded     RemoteStatus = "degraded"
	RemoteStatusStale        RemoteStatus = "stale"
	RemoteStatusIncompatible RemoteStatus = "incompatible"
)

const (
	AgentStatusRunning     AgentStatus = "running"
	AgentStatusWaiting     AgentStatus = "waiting"
	AgentStatusIdle        AgentStatus = "idle"
	AgentStatusFailed      AgentStatus = "failed"
	AgentStatusInterrupted AgentStatus = "interrupted"
	AgentStatusUnknown     AgentStatus = "unknown"
)

type (
	Kind         string
	Origin       string
	RemoteStatus string
	AgentStatus  string
)

type RuntimeIdentity struct {
	ConnectionID         string    `json:"-"`
	ConnectionLabel      string    `json:"-"`
	ConfigDigest         string    `json:"-"`
	PeerID               string    `json:"-"`
	StreamInstance       string    `json:"-"`
	TmuxServerSocket     string    `json:"-"`
	TmuxServerPID        int       `json:"tmuxServerPid,omitempty"`
	TmuxServerStartedAt  time.Time `json:"tmuxServerStartedAt"`
	TmuxSessionCreatedAt time.Time `json:"tmuxSessionCreatedAt"`
}

type Session struct {
	ID                     string          `json:"id"`
	Name                   string          `json:"name"`
	TmuxServerSocket       string          `json:"tmuxServerSocket,omitempty"`
	Kind                   Kind            `json:"kind"`
	Origin                 Origin          `json:"-"`
	Generation             string          `json:"generation,omitempty"`
	RepositorySource       string          `json:"repositorySource,omitempty"`
	NameRaw                RawBytes        `json:"nameRaw,omitempty"`
	PathRaw                RawBytes        `json:"pathRaw,omitempty"`
	RuntimeIdentity        RuntimeIdentity `json:"runtimeIdentity"`
	RemoteStatus           RemoteStatus    `json:"-"`
	WindowCount            int             `json:"windowCount"`
	Attached               bool            `json:"attached"`
	Activity               time.Time       `json:"activity"`
	IsCurrent              bool            `json:"-"`
	ActivePaneID           string          `json:"activePaneId,omitempty"`
	ActiveWindowName       string          `json:"activeWindowName,omitempty"`
	ActiveWindowIndex      string          `json:"activeWindowIndex,omitempty"`
	ActivePanePath         string          `json:"activePanePath,omitempty"`
	ActivePaneCommand      string          `json:"activePaneCommand,omitempty"`
	ActivePaneTitle        string          `json:"activePaneTitle,omitempty"`
	AgentTotal             int             `json:"agentTotal"`
	AgentActive            int             `json:"agentActive"`
	AgentWaiting           int             `json:"agentWaiting"`
	AgentFailed            int             `json:"agentFailed"`
	AgentInterrupted       int             `json:"agentInterrupted"`
	AgentUnknown           int             `json:"agentUnknown"`
	AgentIdleSince         time.Time       `json:"agentIdleSince"`
	AgentWaitingSince      time.Time       `json:"agentWaitingSince"`
	AgentActivityChangedAt time.Time       `json:"agentActivityChangedAt"`
	Agents                 []AgentSession  `json:"agents,omitempty"`
}

type SavedSession struct {
	Name string
}

type AgentSummary struct {
	TmuxServerSocket  string
	TmuxSessionID     string
	TmuxSessionName   string
	Total             int
	Active            int
	Waiting           int
	Failed            int
	Interrupted       int
	Unknown           int
	IdleSince         time.Time
	WaitingSince      time.Time
	ActivityChangedAt time.Time
	Agents            []AgentSession
}

type AgentSession struct {
	PositionKnown    bool            `json:"positionKnown,omitempty"`
	WindowIndex      int             `json:"windowIndex,omitempty"`
	PaneLeft         int             `json:"paneLeft,omitempty"`
	PaneTop          int             `json:"paneTop,omitempty"`
	RegistryID       string          `json:"registryId,omitempty"`
	Harness          string          `json:"harness,omitempty"`
	HarnessSession   string          `json:"harnessSession,omitempty"`
	PaneID           string          `json:"paneId,omitempty"`
	PanePID          int             `json:"panePid,omitempty"`
	CWD              string          `json:"cwd,omitempty"`
	ProjectRoot      string          `json:"projectRoot,omitempty"`
	GitBranch        string          `json:"gitBranch,omitempty"`
	Worktree         string          `json:"worktree,omitempty"`
	GitRepo          string          `json:"gitRepo,omitempty"`
	GitHasRemote     bool            `json:"gitHasRemote,omitempty"`
	GitChanges       string          `json:"gitChanges,omitempty"`
	LinkedWorktree   bool            `json:"linkedWorktree,omitempty"`
	GitAhead         int             `json:"gitAhead,omitempty"`
	GitBehind        int             `json:"gitBehind,omitempty"`
	GitTrackingKnown bool            `json:"gitTrackingKnown,omitempty"`
	GitStash         int             `json:"gitStash,omitempty"`
	GitConflicts     int             `json:"gitConflicts,omitempty"`
	GitDirty         bool            `json:"gitDirty,omitempty"`
	SessionPath      string          `json:"sessionPath,omitempty"`
	Executable       string          `json:"executable,omitempty"`
	PID              int             `json:"pid,omitempty"`
	Status           AgentStatus     `json:"status,omitempty"`
	StatusSince      time.Time       `json:"statusSince"`
	Model            string          `json:"model,omitempty"`
	WaitingReason    string          `json:"waitingReason,omitempty"`
	FailureReason    string          `json:"failureReason,omitempty"`
	Detail           aht.StateDetail `json:"detail,omitzero"`
	LastTool         string          `json:"lastTool,omitempty"`
	Task             string          `json:"task,omitempty"`
	PromptAt         time.Time       `json:"promptAt,omitzero"`
}

func (s AgentStatus) DisplayStatus() AgentStatus {
	if s == AgentStatusInterrupted {
		return AgentStatusIdle
	}

	return s
}

func (s Session) IsSaved() bool {
	return s.Kind == KindSaved
}

func (s Session) SemanticIdentity() Identity {
	socket := s.TmuxServerSocket
	if socket == "" && s.RuntimeIdentity.TmuxServerSocket != "" {
		socket = s.RuntimeIdentity.TmuxServerSocket
	}

	return Identity{
		Origin:           string(s.Origin),
		ConnectionID:     s.RuntimeIdentity.ConnectionID,
		Socket:           socket,
		RepositorySource: s.RepositorySource,
		Name:             s.Name,
	}
}

func (s Session) RuntimeKey() string {
	identity := s.RuntimeIdentity

	return strings.Join([]string{
		string(s.Origin),
		identity.ConnectionID,
		s.TmuxServerSocket,
		s.Name,
		s.ID,
		identity.ConfigDigest,
		identity.PeerID,
		identity.StreamInstance,
		strconv.Itoa(identity.TmuxServerPID),
		strconv.FormatInt(identity.TmuxServerStartedAt.UnixNano(), 10),
		strconv.FormatInt(identity.TmuxSessionCreatedAt.UnixNano(), 10),
	}, "\x00")
}

func (s Session) EntityKey() string {
	parts := []string{
		string(s.Kind),
		string(s.Origin),
		s.RuntimeIdentity.ConnectionID,
		s.TmuxServerSocket,
		s.RepositorySource,
		s.Generation,
		s.ID,
		s.Name,
	}

	return strings.Join(parts, "\x00")
}
