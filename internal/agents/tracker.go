//go:build linux || darwin

package agents

import (
	"context"
	"errors"
	"maps"
	"sync"

	"github.com/rs/zerolog"
	"github.com/samber/oops"
	"github.com/zigai/aht/pkg/aht"
)

type TrackerOptions struct {
	Palette                  map[string]string
	ActiveBypass             bool
	ClearFailedOnVisit       bool
	SetWindowStatusStyle     bool
	SetWindowStatusBellStyle bool
	ClearOnShutdown          bool
}

type Tracker struct {
	mu   sync.Mutex
	tmux StyleController
	opts TrackerOptions

	paneActivity    map[string]string
	paneUnread      map[string]bool
	paneFailed      map[string]bool
	paneWindow      map[string]string
	windowStyles    map[string]string
	windowWasActive map[string]bool
}

func DefaultTrackerOptions() TrackerOptions {
	palette := make(map[string]string, len(DefaultPalette))
	maps.Copy(palette, DefaultPalette)

	return TrackerOptions{
		Palette:                  palette,
		ActiveBypass:             true,
		ClearFailedOnVisit:       true,
		SetWindowStatusStyle:     true,
		SetWindowStatusBellStyle: true,
		ClearOnShutdown:          true,
	}
}

func NewTracker(tmux StyleController, opts TrackerOptions) *Tracker {
	return &Tracker{
		mu:              sync.Mutex{},
		tmux:            tmux,
		opts:            opts,
		paneActivity:    make(map[string]string),
		paneUnread:      make(map[string]bool),
		paneFailed:      make(map[string]bool),
		paneWindow:      make(map[string]string),
		windowStyles:    make(map[string]string),
		windowWasActive: make(map[string]bool),
	}
}

func (t *Tracker) HandleSnapshot(ctx context.Context, sessions []aht.Session) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	activePanes, err := t.tmux.ActivePanes(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Warn().
			Str("component", "agents").
			Str("operation", "handle_snapshot").
			Str("status", "skipped").
			Str("reason", "tmux_query_failed").
			Msg("agent status cycle skipped")

		return nil
	}

	t.clearActiveUnread(activePanes)
	t.clearActiveFailed(activePanes)
	livePanes := t.reconcileSnapshotSessions(sessions, activePanes)
	t.cleanupDeadPanes(livePanes)

	if err := t.reconcileWindows(ctx, activePanes); err != nil {
		return oops.In("agents").Code("style_reconcile_failed").With("operation", "handle_snapshot").Wrap(err)
	}

	return nil
}

func (t *Tracker) ClearAll(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	err := t.tmux.ClearOwned(ctx)
	if err != nil {
		return oops.In("agents").Code("clear_styles").With("operation", "clear_all").Wrap(err)
	}

	clear(t.windowStyles)
	clear(t.windowWasActive)

	return nil
}

func (t *Tracker) WindowStyle(windowID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.windowStyles[windowID]
}

func (t *Tracker) PaneUnread(paneID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.paneUnread[paneID]
}

func (t *Tracker) PaneFailed(paneID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.paneFailed[paneID]
}

func (t *Tracker) PaneActivity(paneID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.paneActivity[paneID]
}

func (t *Tracker) clearActiveUnread(activePanes map[string]ActivePane) {
	for paneID, unread := range t.paneUnread {
		if !unread {
			continue
		}

		pane, exists := activePanes[paneID]
		if !exists {
			delete(t.paneUnread, paneID)

			continue
		}

		windowID := pane.WindowID
		if windowID == "" {
			windowID = t.paneWindow[paneID]
		}

		if windowID != "" && isWindowActive(windowID, activePanes) {
			t.paneUnread[paneID] = false
		}
	}
}

func (t *Tracker) clearActiveFailed(activePanes map[string]ActivePane) {
	if !t.opts.ClearFailedOnVisit {
		return
	}

	for paneID, failed := range t.paneFailed {
		if !failed {
			continue
		}

		pane, exists := activePanes[paneID]
		if !exists {
			delete(t.paneFailed, paneID)

			continue
		}

		windowID := pane.WindowID
		if windowID == "" {
			windowID = t.paneWindow[paneID]
		}

		if windowID != "" && isWindowActive(windowID, activePanes) {
			t.paneFailed[paneID] = false
		}
	}
}

func (t *Tracker) reconcileSnapshotSessions(sessions []aht.Session, activePanes map[string]ActivePane) map[string]bool {
	livePanes := make(map[string]bool, len(sessions))

	for _, session := range sessions {
		if session.Presence != aht.PresenceLive || !session.Tmux.Inside || session.Tmux.PaneID == "" {
			continue
		}

		if nonInteractiveAgent(session) {
			continue
		}

		paneID := session.Tmux.PaneID

		pane, paneExists := activePanes[paneID]
		if !paneExists {
			continue
		}

		windowID := pane.WindowID
		if windowID == "" {
			windowID = session.Tmux.WindowID
		}

		if windowID == "" {
			continue
		}

		livePanes[paneID] = true
		t.paneWindow[paneID] = windowID

		activity := string(aht.ActivityIdle)
		if session.Activity != nil && *session.Activity != "" {
			activity = string(*session.Activity)
		}

		t.reconcilePaneActivity(paneID, windowID, activity, activePanes)
	}

	return livePanes
}

func (t *Tracker) reconcilePaneActivity(paneID, windowID string, currentActivity string, activePanes map[string]ActivePane) {
	previousActivity, hadPrevious := t.paneActivity[paneID]

	if hadPrevious {
		switch {
		case currentActivity == string(aht.ActivityRunning):
			t.paneUnread[paneID] = false
			t.paneFailed[paneID] = false
		case previousActivity == string(aht.ActivityRunning) && currentActivity == string(aht.ActivityIdle):
			t.paneUnread[paneID] = !t.opts.ActiveBypass || !isWindowActive(windowID, activePanes)
		case currentActivity == string(aht.ActivityFailed):
			t.paneUnread[paneID] = false
			t.paneFailed[paneID] = !t.opts.ActiveBypass || !isWindowActive(windowID, activePanes)
		case currentActivity == string(aht.ActivityWaiting),
			currentActivity == string(aht.ActivityInterrupted):
			t.paneUnread[paneID] = false
			t.paneFailed[paneID] = false
		}
	} else if currentActivity == string(aht.ActivityFailed) {
		t.paneFailed[paneID] = !t.opts.ActiveBypass || !isWindowActive(windowID, activePanes)
	}

	t.paneActivity[paneID] = currentActivity
}

func (t *Tracker) cleanupDeadPanes(livePanes map[string]bool) {
	for paneID := range t.paneWindow {
		if livePanes[paneID] {
			continue
		}

		delete(t.paneActivity, paneID)
		delete(t.paneUnread, paneID)
		delete(t.paneFailed, paneID)
		delete(t.paneWindow, paneID)
	}
}

func (t *Tracker) reconcileWindows(ctx context.Context, activePanes map[string]ActivePane) error {
	var failures []error

	windowPanes := make(map[string][]string)

	for paneID, windowID := range t.paneWindow {
		windowPanes[windowID] = append(windowPanes[windowID], paneID)
	}

	for windowID, panes := range windowPanes {
		if err := t.reconcileSingleWindow(ctx, windowID, panes, activePanes); err != nil {
			failures = append(failures, err)
		}
	}

	for windowID := range t.windowStyles {
		if _, exists := windowPanes[windowID]; exists {
			continue
		}

		if err := t.tmux.ClearWindowStyle(ctx, windowID); err != nil {
			failures = append(failures, err)

			continue
		}

		delete(t.windowStyles, windowID)
		delete(t.windowWasActive, windowID)
	}

	return errors.Join(failures...)
}

func (t *Tracker) reconcileSingleWindow(ctx context.Context, windowID string, panes []string, activePanes map[string]ActivePane) error {
	highestState := StateNormal

	for _, paneID := range panes {
		state := t.resolvePaneState(paneID)
		if state.Priority() > highestState.Priority() {
			highestState = state
		}
	}

	isActive := isWindowActive(windowID, activePanes)
	wasActive := t.windowWasActive[windowID]

	if wasActive && !isActive {
		delete(t.windowStyles, windowID)
	}

	t.windowWasActive[windowID] = isActive

	if highestState != StateNormal {
		return t.applyTargetStyle(ctx, windowID, StyleFor(t.opts.Palette, highestState))
	}

	return t.clearTargetStyle(ctx, windowID)
}

func (t *Tracker) applyTargetStyle(ctx context.Context, windowID string, targetStyle string) error {
	if t.windowStyles[windowID] == targetStyle {
		return nil
	}

	if err := t.tmux.SetWindowStyle(ctx, windowID, targetStyle); err != nil {
		return oops.In("agents").Code("style_update_failed").With("operation", "set_window_style").Wrap(err)
	}

	t.windowStyles[windowID] = targetStyle

	return nil
}

func (t *Tracker) clearTargetStyle(ctx context.Context, windowID string) error {
	if t.windowStyles[windowID] == "" {
		return nil
	}

	if err := t.tmux.ClearWindowStyle(ctx, windowID); err != nil {
		return oops.In("agents").Code("style_update_failed").With("operation", "clear_window_style").Wrap(err)
	}

	delete(t.windowStyles, windowID)

	return nil
}

func (t *Tracker) resolvePaneState(paneID string) State {
	activity := t.paneActivity[paneID]

	switch activity {
	case string(aht.ActivityWaiting):
		return StateWaiting
	case string(aht.ActivityFailed):
		if t.paneFailed[paneID] {
			return StateFailed
		}

		return StateNormal
	case string(aht.ActivityRunning), string(aht.ActivityIdle), string(aht.ActivityInterrupted), string(aht.ActivityUnknown):
		if t.paneUnread[paneID] {
			return StateUnread
		}

		return StateNormal
	default:
		if t.paneUnread[paneID] {
			return StateUnread
		}

		return StateNormal
	}
}

func isWindowActive(windowID string, activePanes map[string]ActivePane) bool {
	for _, pane := range activePanes {
		if pane.WindowID == windowID && pane.WindowActive {
			return true
		}
	}

	return false
}
