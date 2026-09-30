//go:build linux || darwin

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/samber/oops"
)

var errToggleConcurrentMutation = errors.New("navigation toggle state changed concurrently")

type togglePair struct {
	Working HistoryEntry `json:"working"`
	Agent   HistoryEntry `json:"agent"`
}

func toggleHistoryKey(clientKey string) string {
	return "\x00sesh-agent-toggle\x00" + clientKey
}

func contextHistoryEntry(current CurrentContext) HistoryEntry {
	return HistoryEntry{PaneID: current.PaneID, PanePID: current.PanePID, WindowID: current.WindowID, SessionID: current.SessionID, ServerSocket: current.ServerSocket, ServerPID: current.ServerPID}
}

func paneHistoryEntry(pane PaneLocation, serverPID int) HistoryEntry {
	return HistoryEntry{PaneID: pane.PaneID, PanePID: pane.PanePID, WindowID: pane.WindowID, SessionID: pane.SessionID, ServerSocket: pane.ServerSocket, ServerPID: serverPID}
}

func validToggleEntry(entry HistoryEntry) bool {
	return entry.PaneID != "" && entry.PanePID > 0 && entry.WindowID != "" && entry.SessionID != "" && entry.ServerSocket != "" && entry.ServerPID > 0
}

func toggleEntryMatches(entry HistoryEntry, current CurrentContext) bool {
	return entry.PaneID == current.PaneID && entry.PanePID == current.PanePID && entry.WindowID == current.WindowID && entry.ServerPID == current.ServerPID && SameServerSocket(entry.ServerSocket, current.ServerSocket)
}

func togglePane(panes []PaneLocation, entry HistoryEntry, current CurrentContext) (PaneLocation, bool) {
	if entry.ServerPID != current.ServerPID || !SameServerSocket(entry.ServerSocket, current.ServerSocket) {
		return emptyPaneLocation, false
	}

	pane, ok := FindPaneForSession(panes, entry.PaneID, entry.ServerSocket, entry.SessionID)

	return pane, ok && pane.PanePID == entry.PanePID && pane.WindowID == entry.WindowID && pane.SessionID == entry.SessionID
}

func (p *togglePair) destination(panes []PaneLocation, current CurrentContext) (PaneLocation, bool) {
	agent, ok := togglePane(panes, p.Agent, current)
	if !ok {
		return emptyPaneLocation, false
	}

	if toggleEntryMatches(p.Agent, current) {
		return togglePane(panes, p.Working, current)
	}

	if !toggleEntryMatches(p.Working, current) {
		p.Working = contextHistoryEntry(current)
	}

	return agent, true
}

func (n *Navigator) loadTogglePair(ctx context.Context, clientKey string) (string, *togglePair, error) {
	raw, err := n.controller.History(ctx, toggleHistoryKey(clientKey))
	if err != nil {
		return "", nil, oops.In("agents").Code("tmux_failed").With("operation", "read_toggle").Wrap(err)
	}

	if strings.TrimSpace(raw) == "" {
		return raw, nil, nil
	}

	var pair togglePair
	if err := json.Unmarshal([]byte(raw), &pair); err != nil {
		return raw, nil, oops.In("agents").Code("corrupt_history").With("operation", "read_toggle").Wrap(errors.Join(ErrNavigationHistoryCorrupt, err))
	}

	if !validToggleEntry(pair.Working) || !validToggleEntry(pair.Agent) || pair.Working.PaneID == pair.Agent.PaneID || pair.Working.ServerPID != pair.Agent.ServerPID || !SameServerSocket(pair.Working.ServerSocket, pair.Agent.ServerSocket) {
		return raw, nil, oops.In("agents").Code("corrupt_history").With("operation", "read_toggle").Wrap(ErrNavigationHistoryCorrupt)
	}

	return raw, &pair, nil
}

func (n *Navigator) stageToggleAndActivate(ctx context.Context, current CurrentContext, target PaneLocation, previous string, pair togglePair) error {
	encoded, err := json.Marshal(pair)
	if err != nil {
		return oops.In("agents").Code("encode_history").With("operation", "write_toggle").Wrap(err)
	}

	key := toggleHistoryKey(current.HistoryKey())

	next := string(encoded)
	if err := n.controller.SetHistory(ctx, key, next); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "write_toggle").Wrap(err)
	}

	if err := n.activateHistoryLocked(ctx, current, target); err != nil {
		return errors.Join(err, n.rollbackToggle(ctx, key, previous, next))
	}

	return nil
}

func (n *Navigator) rollbackToggle(ctx context.Context, key, previous, expected string) error {
	raw, err := n.controller.History(ctx, key)
	if err != nil {
		return oops.In("agents").Code("history_read_failed").With("operation", "toggle_rollback").Wrap(err)
	}

	if raw != expected {
		return oops.In("agents").Code("concurrent_mutation").With("operation", "toggle_rollback").Wrap(errToggleConcurrentMutation)
	}

	if err := n.controller.SetHistory(ctx, key, previous); err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "toggle_rollback").Wrap(err)
	}

	return nil
}

func (n *Navigator) Toggle(ctx context.Context, scope NavigationScope) (PaneLocation, error) {
	lockCtx, cancel := context.WithTimeout(ctx, defaultLockTimeout)
	defer cancel()

	if err := n.controller.Lock(lockCtx, navigationLockChannel); err != nil {
		return emptyPaneLocation, oops.In("agents").Code("fencing_failed").With("operation", "navigation_lock").Wrap(err)
	}
	target, err := n.toggleLocked(ctx, scope)

	return target, errors.Join(err, n.unlockNavigation(ctx))
}

func (n *Navigator) toggleLocked(ctx context.Context, scope NavigationScope) (PaneLocation, error) {
	current, panes, err := n.currentAndLivePanes(ctx)
	if err != nil {
		return emptyPaneLocation, err
	}

	raw, pair, err := n.loadTogglePair(ctx, current.HistoryKey())
	if err != nil {
		return emptyPaneLocation, err
	}

	if pair == nil {
		return emptyPaneLocation, oops.In("agents").Code("not_found").With("operation", "toggle").Wrap(ErrNoTargetPane)
	}

	target, ok := pair.destination(panes, current)
	if !ok {
		return emptyPaneLocation, oops.In("agents").Code("not_found").With("operation", "toggle").Wrap(ErrNoTargetPane)
	}

	windows, err := n.sessionWindows(ctx, current)
	if err != nil {
		return emptyPaneLocation, err
	}

	if scope == NavigationCurrentSession && !paneInSessionWindows(target, windows) {
		return emptyPaneLocation, oops.In("agents").Code("not_found").With("operation", "toggle").Wrap(ErrNoTargetPane)
	}

	target = retargetToCurrentSession(current, target, windows)
	if err := n.stageToggleAndActivate(ctx, current, target, raw, *pair); err != nil {
		return emptyPaneLocation, err
	}

	logNavigation(ctx, "toggle", current, target, navigationScopeName(scope))

	return target, nil
}
