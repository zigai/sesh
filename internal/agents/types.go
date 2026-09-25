//go:build linux || darwin

package agents

import (
	"errors"
	"time"
)

const (
	NavigationGlobal NavigationScope = iota
	NavigationCurrentSession
)

const (
	StateNormal  State = "normal"
	StateUnread  State = "unread"
	StateFailed  State = "failed"
	StateWaiting State = "waiting"
)

const (
	priorityNormal  = 0
	priorityUnread  = 1
	priorityFailed  = 2
	priorityWaiting = 3
)

var (
	ErrNoTargetPane             = errors.New("no candidate agent pane found")
	ErrEmptyNavigationHistory   = errors.New("agent navigation history is empty")
	ErrNavigationHistoryCorrupt = errors.New("agent navigation history is corrupt")
	ErrCurrentContextUnknown    = errors.New("current tmux context could not be determined")
	ErrCurrentPaneNotLive       = errors.New("current pane is not a live tmux pane")
	ErrPaneNotLive              = errors.New("pane is not a live tmux pane")
	ErrServerRequired           = errors.New("tmux server required")
	ErrRegistryUnavailable      = errors.New("agent registry unavailable")
	ErrRegistryCorrupt          = errors.New("agent registry is corrupt")
	ErrRegistrySchema           = errors.New("unsupported agent registry schema")
	ErrRegistryTooLarge         = errors.New("agent registry exceeds its size limit")
	ErrStyleOwnershipCorrupt    = errors.New("agent style ownership record is corrupt")
)

type NavigationScope int

type State string

type AgentSession struct {
	ID               string    `json:"id"`
	Harness          string    `json:"harness"`
	Presence         string    `json:"presence"`
	Activity         string    `json:"activity"`
	Attention        string    `json:"attention"`
	ServerSocket     string    `json:"serverSocket"`
	SessionID        string    `json:"sessionId"`
	SessionName      string    `json:"sessionName"`
	WindowID         string    `json:"windowId"`
	WindowIndex      string    `json:"windowIndex"`
	PaneID           string    `json:"paneId"`
	PaneIndex        string    `json:"paneIndex"`
	PanePID          int       `json:"panePid"`
	Command          string    `json:"command"`
	Interactive      bool      `json:"interactive"`
	CWD              string    `json:"cwd"`
	ProjectRoot      string    `json:"projectRoot"`
	HarnessSessionID string    `json:"harnessSessionId"`
	SessionPath      string    `json:"sessionPath"`
	ResumeCommand    []string  `json:"resumeCommand"`
	ActivityChanged  time.Time `json:"activityChangedAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type StatusSummary struct {
	Total       int `json:"total"`
	Live        int `json:"live"`
	Waiting     int `json:"waiting"`
	Failed      int `json:"failed"`
	Unread      int `json:"unread"`
	Idle        int `json:"idle"`
	Running     int `json:"running"`
	Interrupted int `json:"interrupted"`
	Unknown     int `json:"unknown"`
}

type HistoryEntry struct {
	PaneID       string `json:"paneId"`
	PanePID      int    `json:"panePid,omitempty"`
	WindowID     string `json:"windowId,omitempty"`
	SessionID    string `json:"sessionId,omitempty"`
	ServerSocket string `json:"serverSocket,omitempty"`
	ServerPID    int    `json:"serverPid,omitempty"`
}

func (s State) Priority() int {
	switch s {
	case StateWaiting:
		return priorityWaiting
	case StateFailed:
		return priorityFailed
	case StateUnread:
		return priorityUnread
	case StateNormal:
		return priorityNormal
	default:
		return priorityNormal
	}
}

func (s State) String() string { return string(s) }
