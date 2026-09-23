//go:build linux || darwin

package agents

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

var emptyPaneLocation PaneLocation

type PaneLocation struct {
	ServerSocket string
	SessionID    string
	SessionName  string
	WindowID     string
	WindowIndex  string
	PaneID       string
	PaneIndex    string
	PanePID      int
	Command      string
	Left         int
	Top          int
	PaneActive   bool
	WindowActive bool
}

func (l PaneLocation) SameWindow(other PaneLocation) bool {
	return l.WindowID == other.WindowID && SameServerSocket(l.ServerSocket, other.ServerSocket)
}

func NormalizeServerSocket(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	return filepath.Clean(value)
}

func SameServerSocket(left string, right string) bool {
	left = NormalizeServerSocket(left)
	right = NormalizeServerSocket(right)

	if left == "" || right == "" {
		return true
	}

	if filepath.Base(left) != left && filepath.Base(right) != right {
		return left == right
	}

	return filepath.Base(left) == filepath.Base(right)
}

func ObservePanes(ctx context.Context, server *gotmux.Server) ([]PaneLocation, error) {
	if server == nil {
		return nil, oops.In("agents").Code("endpoint_required").With("operation", "list_panes").Wrap(ErrServerRequired)
	}

	if err := ctx.Err(); err != nil {
		return nil, oops.In("agents").Code("operation_canceled").With("operation", "list_panes").Wrap(err)
	}

	panes, err := server.PanesWith(ctx, gotmux.QueryOptions{Filter: "", ExtraFields: []string{"window_active"}})
	if err != nil {
		return nil, oops.In("agents").Code("tmux_failed").With("operation", "list_panes").Wrap(err)
	}

	locations := make([]PaneLocation, 0, len(panes))
	for _, pane := range panes {
		locations = append(locations, PaneLocationFromInfo(pane))
	}

	return locations, nil
}

func PaneLocationFromInfo(pane gotmux.PaneInfo) PaneLocation {
	location := PaneLocation{
		ServerSocket: socketPathOf(pane),
		SessionID:    "",
		SessionName:  "",
		WindowID:     string(pane.WindowID),
		WindowIndex:  "",
		PaneID:       string(pane.ID),
		PaneIndex:    strconv.Itoa(pane.Index),
		PanePID:      pane.PID,
		Command:      pane.CurrentCommand,
		Left:         pane.Left,
		Top:          pane.Top,
		PaneActive:   pane.Active,
		WindowActive: rawFlag(pane, "window_active"),
	}

	if sessionID, ok := pane.SessionID.Get(); ok {
		location.SessionID = string(sessionID)
	}

	if sessionName, ok := pane.SessionName.Get(); ok {
		location.SessionName = sessionName
	}

	if windowIndex, ok := pane.WindowIndex.Get(); ok {
		location.WindowIndex = strconv.Itoa(windowIndex)
	}

	return location
}

func FindPane(panes []PaneLocation, paneID string, serverSocket string) (PaneLocation, bool) {
	links := FindPanes(panes, paneID, serverSocket)
	if len(links) == 0 {
		return emptyPaneLocation, false
	}

	return links[0], true
}

func FindPanes(panes []PaneLocation, paneID string, serverSocket string) []PaneLocation {
	links := make([]PaneLocation, 0, 1)

	for _, pane := range panes {
		if pane.PaneID == paneID && SameServerSocket(pane.ServerSocket, serverSocket) {
			links = append(links, pane)
		}
	}

	return links
}

func FindPaneForSession(panes []PaneLocation, paneID string, serverSocket string, sessionID string) (PaneLocation, bool) {
	links := FindPanes(panes, paneID, serverSocket)
	if len(links) == 0 {
		return emptyPaneLocation, false
	}

	if sessionID != "" {
		for _, link := range links {
			if link.SessionID == sessionID {
				return link, true
			}
		}
	}

	return links[0], true
}

func socketPathOf(pane gotmux.PaneInfo) string {
	raw, ok := pane.Raw("socket_path")
	if !ok {
		return ""
	}

	return NormalizeServerSocket(string(raw))
}

func rawFlag(pane gotmux.PaneInfo, name string) bool {
	raw, ok := pane.Raw(name)
	if !ok {
		return false
	}

	return strings.TrimSpace(string(raw)) == "1"
}
