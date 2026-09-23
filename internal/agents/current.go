//go:build linux || darwin

package agents

type CurrentContext struct {
	ServerSocket string
	ClientName   string
	ClientTTY    string
	SessionID    string
	SessionName  string
	WindowID     string
	PaneID       string
	PanePID      int
	ServerPID    int
}

type CurrentTarget struct {
	PaneID    string
	Client    string
	SessionID string
	Session   string
}

type SessionWindow struct {
	SessionID   string
	SessionName string
	WindowID    string
	WindowIndex string
}

type FocusTarget struct {
	PaneID      string
	SessionID   string
	SessionName string
}

func (c CurrentContext) Valid() bool {
	return c.PaneID != "" && c.WindowID != "" && c.SessionID != ""
}

func (c CurrentContext) HistoryKey() string {
	if c.ClientName != "" {
		return c.ClientName
	}

	if c.ClientTTY != "" {
		return c.ClientTTY
	}

	if c.SessionID != "" {
		return c.SessionID
	}

	return "global"
}

func retargetToWindow(pane PaneLocation, window SessionWindow) PaneLocation {
	pane.SessionID = window.SessionID
	pane.SessionName = window.SessionName
	pane.WindowIndex = window.WindowIndex

	return pane
}

func contextFromPane(pane PaneLocation, clientName string, serverPID int) CurrentContext {
	return CurrentContext{
		ServerSocket: pane.ServerSocket,
		ClientName:   clientName,
		ClientTTY:    clientName,
		SessionID:    pane.SessionID,
		SessionName:  pane.SessionName,
		WindowID:     pane.WindowID,
		PaneID:       pane.PaneID,
		PanePID:      pane.PanePID,
		ServerPID:    serverPID,
	}
}
