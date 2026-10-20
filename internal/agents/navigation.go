//go:build linux || darwin

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/logging"
)

const (
	navigationHistoryOption                 = "@sesh-agent-navigation-history"
	navigationLockChannel                   = "sesh_nav_history"
	maxNavigationHistory                    = 64
	navigationAgent         navigationVisit = 0
	navigationPane          navigationVisit = 1
)

type navigationVisit uint8

type NavigatorOptions struct {
	Server     *gotmux.Server
	StatePath  string
	Target     CurrentTarget
	Controller Controller
	Source     SessionSource
}

type Navigator struct {
	controller Controller
	source     SessionSource
}

func NewNavigator(opts NavigatorOptions) (*Navigator, error) {
	controller := opts.Controller

	if controller == nil {
		if opts.Server == nil {
			return nil, oops.In("agents").Code("endpoint_required").With("operation", "new_navigator").Wrap(ErrServerRequired)
		}

		controller = NewTmuxController(opts.Server, opts.Target)
	}

	source := opts.Source
	if source == nil {
		source = NewAHTStore(opts.StatePath)
	}

	return &Navigator{controller: controller, source: source}, nil
}

func (n *Navigator) NextIdle(ctx context.Context, scope NavigationScope) (PaneLocation, error) {
	current, panes, err := n.currentAndLivePanes(ctx)
	if err != nil {
		return PaneLocation{}, err
	}

	agents, err := n.attentionCandidates(ctx, panes)
	if err != nil {
		return PaneLocation{}, err
	}

	sessionWindows, err := n.sessionWindows(ctx, current)
	if err != nil {
		return PaneLocation{}, err
	}

	target, ok := nextIdlePane(current, panes, agents, scope, sessionWindows)
	if !ok {
		return PaneLocation{}, oops.In("agents").Code("not_found").With("operation", "next_idle").Wrap(ErrNoTargetPane)
	}

	target = retargetToCurrentSession(current, target, sessionWindows)

	if err := n.activateWithHistory(ctx, current, target, navigationAgent); err != nil {
		return PaneLocation{}, err
	}

	logNavigation(ctx, "next_idle", current, target, navigationScopeName(scope))

	return target, nil
}

func (n *Navigator) LatestIdle(ctx context.Context, scope NavigationScope) (PaneLocation, error) {
	visit, err := n.rankedAgent(ctx, scope, agentSelection{kind: selectionLatest, visits: nil})
	return visit.Pane, err
}

func (n *Navigator) Waiting(ctx context.Context, scope NavigationScope) (PaneLocation, error) {
	visit, err := n.rankedAgent(ctx, scope, agentSelection{kind: selectionWaiting, visits: nil})
	return visit.Pane, err
}

func (n *Navigator) Failed(ctx context.Context, scope NavigationScope) (PaneLocation, error) {
	visit, err := n.rankedAgent(ctx, scope, agentSelection{kind: selectionFailed, visits: nil})
	return visit.Pane, err
}

func (n *Navigator) UnreadIdle(ctx context.Context, scope NavigationScope, visits []PaneVisit) (AgentVisit, error) {
	return n.rankedAgent(ctx, scope, agentSelection{kind: selectionUnread, visits: visits})
}

func (n *Navigator) Back(ctx context.Context) (PaneLocation, error) {
	current, panes, err := n.currentAndLivePanes(ctx)
	if err != nil {
		return emptyPaneLocation, err
	}

	clientKey := current.HistoryKey()

	lockCtx, cancel := context.WithTimeout(ctx, defaultLockTimeout)
	defer cancel()

	if err := n.controller.Lock(lockCtx, navigationLockChannel); err != nil {
		return emptyPaneLocation, oops.In("agents").Code("fencing_failed").With("operation", "navigation_lock").Wrap(err)
	}

	target, backErr := n.backLocked(ctx, current, panes, clientKey)

	return target, errors.Join(backErr, n.unlockNavigation(ctx))
}

func (n *Navigator) Focus(ctx context.Context, focus FocusTarget) (PaneLocation, error) {
	current, panes, err := n.currentAndLivePanes(ctx)
	if err != nil {
		return PaneLocation{}, err
	}

	target, ok := focusPaneLocation(panes, focus, current)
	if !ok {
		return PaneLocation{}, oops.In("agents").Code("pane_not_live").With("operation", "focus").Wrap(ErrPaneNotLive)
	}

	if focus.SessionID != "" && target.SessionID != focus.SessionID {
		return PaneLocation{}, oops.In("agents").Code("pane_not_live").With("operation", "focus").Wrap(ErrPaneNotLive)
	}

	if focus.SessionName != "" && target.SessionName != focus.SessionName {
		return PaneLocation{}, oops.In("agents").Code("pane_not_live").With("operation", "focus").Wrap(ErrPaneNotLive)
	}

	if target.PaneID == current.PaneID {
		logNavigation(ctx, "focus", current, target, "")

		return target, nil
	}

	sessionWindows, err := n.sessionWindows(ctx, current)
	if err != nil {
		return PaneLocation{}, err
	}

	if focus.SessionID == "" && focus.SessionName == "" {
		target = retargetToCurrentSession(current, target, sessionWindows)
	}

	if err := n.activateWithHistory(ctx, current, target, n.focusVisit(ctx, target, panes)); err != nil {
		return PaneLocation{}, err
	}

	logNavigation(ctx, "focus", current, target, "")

	return target, nil
}

func (n *Navigator) backLocked(
	ctx context.Context,
	current CurrentContext,
	panes []PaneLocation,
	clientKey string,
) (PaneLocation, error) {
	raw, history, err := n.loadHistory(ctx, clientKey)
	if err != nil {
		return emptyPaneLocation, err
	}

	if len(history) == 0 {
		return emptyPaneLocation, oops.In("agents").Code("empty_history").With("operation", "back").Wrap(ErrEmptyNavigationHistory)
	}

	sessionWindows, err := n.sessionWindows(ctx, current)
	if err != nil {
		return emptyPaneLocation, err
	}

	for len(history) > 0 {
		last := len(history) - 1
		entry := history[last]
		history = history[:last]

		target, ok := FindPaneForSession(panes, entry.PaneID, current.ServerSocket, entry.SessionID)
		if !ok || target.PaneID == current.PaneID {
			continue
		}

		if !n.matchHistoryEntry(ctx, entry, target, current) {
			continue
		}

		target = retargetToCurrentSession(current, target, sessionWindows)

		if err := n.replaceHistoryAndActivate(ctx, clientKey, raw, history, current, target); err != nil {
			return emptyPaneLocation, err
		}

		logNavigation(ctx, "back", current, target, "")

		return target, nil
	}

	if err := n.controller.SetHistory(ctx, clientKey, ""); err != nil {
		return emptyPaneLocation, oops.In("agents").Code("tmux_failed").With("operation", "write_history").Wrap(err)
	}

	return emptyPaneLocation, oops.In("agents").Code("not_found").With("operation", "back").Wrap(ErrNoTargetPane)
}

func (n *Navigator) matchHistoryEntry(ctx context.Context, entry HistoryEntry, target PaneLocation, current CurrentContext) bool {
	if entry.PanePID > 0 && entry.PanePID != target.PanePID {
		zerolog.Ctx(ctx).Warn().
			Str("component", "agents").
			Str("operation", "back").
			Str("status", "skipped").
			Str("reason", "process_incarnation_mismatch").
			Msg("agent navigation history entry rejected due to process incarnation mismatch")

		return false
	}

	if entry.ServerSocket != "" && !SameServerSocket(entry.ServerSocket, current.ServerSocket) {
		return false
	}

	if entry.ServerPID > 0 && entry.ServerPID != current.ServerPID {
		zerolog.Ctx(ctx).Warn().
			Str("component", "agents").
			Str("operation", "back").
			Str("status", "skipped").
			Str("reason", "server_incarnation_mismatch").
			Msg("agent navigation history entry rejected due to server incarnation mismatch")

		return false
	}

	return true
}

func (n *Navigator) currentAndLivePanes(ctx context.Context) (CurrentContext, []PaneLocation, error) {
	current, err := n.controller.CurrentContext(ctx)
	if err != nil {
		return CurrentContext{}, nil, oops.In("agents").Code("current_context").With("operation", "navigation").Wrap(err)
	}

	panes, err := n.controller.Panes(ctx)
	if err != nil {
		return CurrentContext{}, nil, oops.In("agents").Code("tmux_failed").With("operation", "list_panes").Wrap(err)
	}

	if current.PaneID != "" {
		pane, ok := FindPane(panes, current.PaneID, current.ServerSocket)
		if !ok {
			return CurrentContext{}, nil, oops.In("agents").Code("current_pane_not_live").With("operation", "current_context").Wrap(ErrCurrentPaneNotLive)
		}

		current = completeContext(current, pane)
	}

	return current, panes, nil
}

func focusPaneLocation(panes []PaneLocation, focus FocusTarget, current CurrentContext) (PaneLocation, bool) {
	sessionID := focus.SessionID
	if sessionID == "" && focus.SessionName == "" {
		sessionID = current.SessionID
	}

	for _, target := range panes {
		if target.PaneID != focus.PaneID || !SameServerSocket(target.ServerSocket, current.ServerSocket) {
			continue
		}

		if (sessionID == "" || target.SessionID == sessionID) &&
			(focus.SessionName == "" || target.SessionName == focus.SessionName) {
			return target, true
		}
	}

	if sessionID != "" || focus.SessionName != "" {
		return emptyPaneLocation, false
	}

	return FindPane(panes, focus.PaneID, current.ServerSocket)
}

func completeContext(current CurrentContext, pane PaneLocation) CurrentContext {
	if current.ServerSocket == "" {
		current.ServerSocket = pane.ServerSocket
	}

	if current.SessionID == "" {
		current.SessionID = pane.SessionID
	}

	if current.SessionName == "" {
		current.SessionName = pane.SessionName
	}

	if current.WindowID == "" {
		current.WindowID = pane.WindowID
	}

	if current.PanePID == 0 {
		current.PanePID = pane.PanePID
	}

	return current
}

func (n *Navigator) attentionCandidates(ctx context.Context, panes []PaneLocation) ([]AgentSession, error) {
	agents, err := LoadLiveAgents(ctx, n.source)
	if err != nil {
		return nil, err
	}

	return PruneVanishedPanes(agents, panes), nil
}

func nextIdlePane(
	current CurrentContext,
	panes []PaneLocation,
	agents []AgentSession,
	scope NavigationScope,
	sessionWindows map[string]SessionWindow,
) (PaneLocation, bool) {
	currentIndex := slices.IndexFunc(panes, func(pane PaneLocation) bool { return pane.PaneID == current.PaneID })
	if currentIndex < 0 {
		return emptyPaneLocation, false
	}

	shared := sharedPaneIDs(panes)

	for offset := 1; offset < len(panes); offset++ {
		pane := panes[(currentIndex+offset)%len(panes)]
		if scope == NavigationCurrentSession && !paneInSessionWindows(pane, sessionWindows) {
			continue
		}

		if paneHasAttentionAgent(pane, agents, shared[pane.PaneID]) {
			return pane, true
		}
	}

	return emptyPaneLocation, false
}

func (n *Navigator) sessionWindows(ctx context.Context, current CurrentContext) (map[string]SessionWindow, error) {
	if current.SessionID == "" {
		return map[string]SessionWindow{}, nil
	}

	windows, err := n.controller.SessionWindows(ctx, current.SessionID)
	if err != nil {
		return nil, oops.In("agents").Code("tmux_failed").With("operation", "session_windows").Wrap(err)
	}

	indexed := make(map[string]SessionWindow, len(windows))

	for _, window := range windows {
		indexed[window.WindowID] = window
	}

	return indexed, nil
}

func paneInSessionWindows(pane PaneLocation, sessionWindows map[string]SessionWindow) bool {
	if len(sessionWindows) == 0 {
		return false
	}

	_, ok := sessionWindows[pane.WindowID]

	return ok
}

func retargetToCurrentSession(current CurrentContext, target PaneLocation, sessionWindows map[string]SessionWindow) PaneLocation {
	if current.SessionID == "" || current.SessionID == target.SessionID {
		return target
	}

	window, ok := sessionWindows[target.WindowID]
	if !ok {
		return target
	}

	return retargetToWindow(target, window)
}

func sharedPaneIDs(panes []PaneLocation) map[string]bool {
	counts := make(map[string]int, len(panes))

	for _, pane := range panes {
		counts[pane.PaneID]++
	}

	shared := make(map[string]bool)

	for paneID, count := range counts {
		if count > 1 {
			shared[paneID] = true
		}
	}

	return shared
}

func paneHasAttentionAgent(pane PaneLocation, agents []AgentSession, shared bool) bool {
	for _, agent := range agents {
		if agent.PaneID != pane.PaneID || !NeedsAttention(agent) {
			continue
		}

		if !SameServerSocket(agent.ServerSocket, pane.ServerSocket) {
			continue
		}

		if !shared && !paneMatchesAgentSession(agent, pane) {
			continue
		}

		return true
	}

	return false
}

func paneMatchesAgentSession(agent AgentSession, pane PaneLocation) bool {
	if agent.SessionID != "" && pane.SessionID != "" {
		return agent.SessionID == pane.SessionID
	}

	if agent.SessionName != "" && pane.SessionName != "" {
		return agent.SessionName == pane.SessionName
	}

	return true
}

func (n *Navigator) focusVisit(ctx context.Context, target PaneLocation, panes []PaneLocation) navigationVisit {
	agents, err := LoadLiveAgents(ctx, n.source)
	if err != nil {
		return navigationPane
	}

	for _, agent := range agents {
		for _, pane := range panes {
			if pane.PaneID == target.PaneID && pane.PanePID == target.PanePID && SameServerSocket(pane.ServerSocket, target.ServerSocket) && paneMatchesAgentOwner(&agent, pane) {
				return navigationAgent
			}
		}
	}

	return navigationPane
}

func (n *Navigator) activateWithHistory(ctx context.Context, current CurrentContext, target PaneLocation, visit navigationVisit) (retErr error) {
	if target.PaneID == current.PaneID {
		return nil
	}

	lockCtx, cancel := context.WithTimeout(ctx, defaultLockTimeout)
	defer cancel()

	if err := n.controller.Lock(lockCtx, navigationLockChannel); err != nil {
		return oops.In("agents").Code("fencing_failed").With("operation", "navigation_lock").Wrap(err)
	}
	defer func() { retErr = errors.Join(retErr, n.unlockNavigation(ctx)) }()

	if visit == navigationPane {
		return n.activateHistoryLocked(ctx, current, target)
	}

	panes, err := n.controller.Panes(ctx)
	if err != nil {
		return oops.In("agents").Code("tmux_failed").With("operation", "list_panes").Wrap(err)
	}

	previous, pair, err := n.loadTogglePair(ctx, current.HistoryKey())
	if err != nil {
		return err
	}

	working := contextHistoryEntry(current)
	if pair != nil && toggleEntryMatches(pair.Agent, current) {
		if _, ok := togglePane(panes, pair.Working, current); ok {
			working = pair.Working
		}
	}

	next := togglePair{Working: working, Agent: paneHistoryEntry(target, current.ServerPID)}
	if pair != nil && next.Agent.PaneID == pair.Working.PaneID && next.Agent.PanePID == pair.Working.PanePID {
		next = *pair
	}

	return n.stageToggleAndActivate(ctx, current, target, previous, next)
}

func (n *Navigator) activateHistoryLocked(ctx context.Context, current CurrentContext, target PaneLocation) error {
	clientKey := current.HistoryKey()

	raw, history, err := n.loadHistory(ctx, clientKey)
	if err != nil {
		return err
	}

	currentEntry := contextHistoryEntry(current)

	if len(history) == 0 || history[len(history)-1].PaneID != current.PaneID {
		history = append(history, currentEntry)
	} else if history[len(history)-1].PaneID == current.PaneID {
		history[len(history)-1] = currentEntry
	}

	if len(history) > maxNavigationHistory {
		history = history[len(history)-maxNavigationHistory:]
	}

	return n.replaceHistoryAndActivate(ctx, clientKey, raw, history, current, target)
}

func (n *Navigator) replaceHistoryAndActivate(
	ctx context.Context,
	clientKey string,
	previousRaw string,
	history []HistoryEntry,
	current CurrentContext,
	target PaneLocation,
) error {
	newRaw, err := n.saveHistory(ctx, clientKey, history)
	if err != nil {
		return err
	}

	activationErr := n.controller.Activate(ctx, current, target)
	if activationErr == nil {
		return nil
	}

	enriched := oops.In("agents").Code("activate_failed").With("operation", "activate").Wrap(activationErr)

	currentRaw, readErr := n.controller.History(ctx, clientKey)
	if readErr != nil {
		return errors.Join(enriched, oops.In("agents").Code("history_read_failed").With("operation", "history_rollback").Wrap(readErr))
	}

	if currentRaw != newRaw {
		zerolog.Ctx(ctx).Warn().
			Str("component", "agents").
			Str("operation", "history_rollback").
			Str("status", "skipped").
			Str("reason", "concurrent_mutation_detected").
			Msg("agent navigation history rollback skipped to preserve concurrent state")

		return enriched
	}

	rollbackErr := n.controller.SetHistory(ctx, clientKey, previousRaw)
	if rollbackErr == nil {
		return enriched
	}

	zerolog.Ctx(ctx).Warn().
		Str("component", "agents").
		Str("operation", "history_rollback").
		Str("status", "failed").
		Str("reason", "rollback_failed").
		Func(logging.Cause(rollbackErr)).Msg("agent navigation history rollback failed")

	return errors.Join(enriched, rollbackErr)
}

func (n *Navigator) loadHistory(ctx context.Context, clientKey string) (string, []HistoryEntry, error) {
	raw, err := n.controller.History(ctx, clientKey)
	if err != nil {
		return "", nil, oops.In("agents").Code("tmux_failed").With("operation", "read_history").Wrap(err)
	}

	if strings.TrimSpace(raw) == "" {
		return raw, nil, nil
	}

	var history []HistoryEntry

	if err := json.Unmarshal([]byte(raw), &history); err != nil {
		return "", nil, oops.In("agents").Code("corrupt_history").With("operation", "read_history").Wrap(errors.Join(ErrNavigationHistoryCorrupt, err))
	}

	for _, entry := range history {
		if entry.PaneID == "" || entry.PanePID <= 0 || entry.WindowID == "" || entry.SessionID == "" ||
			entry.ServerSocket == "" || entry.ServerPID <= 0 {
			return "", nil, oops.In("agents").Code("corrupt_history").With("operation", "read_history").Wrap(ErrNavigationHistoryCorrupt)
		}
	}

	return raw, history, nil
}

func (n *Navigator) unlockNavigation(ctx context.Context) error {
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultLockTimeout)
	defer cancel()

	if err := n.controller.Unlock(unlockCtx, navigationLockChannel); err != nil {
		return oops.In("agents").Code("fencing_failed").With("operation", "navigation_unlock").Wrap(err)
	}

	return nil
}

func (n *Navigator) saveHistory(ctx context.Context, clientKey string, history []HistoryEntry) (string, error) {
	encoded, err := json.Marshal(history)
	if err != nil {
		return "", oops.In("agents").Code("encode_history").With("operation", "write_history").Wrap(err)
	}

	newRaw := string(encoded)

	if err := n.controller.SetHistory(ctx, clientKey, newRaw); err != nil {
		return "", oops.In("agents").Code("tmux_failed").With("operation", "write_history").Wrap(err)
	}

	return newRaw, nil
}

func logNavigation(ctx context.Context, operation string, current CurrentContext, target PaneLocation, scope string) {
	event := zerolog.Ctx(ctx).Info().
		Str("component", "agents").
		Str("operation", operation).
		Str("status", "ok").
		Bool("cross_session", current.SessionID != target.SessionID).
		Bool("cross_window", current.WindowID != target.WindowID)

	if scope != "" {
		event = event.Str("scope", scope)
	}

	event.Msg("agent pane activated")
}

func navigationScopeName(scope NavigationScope) string {
	if scope == NavigationCurrentSession {
		return "current_session"
	}

	return "global"
}
