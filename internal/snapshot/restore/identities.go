//go:build linux || darwin

package restore

import (
	"context"

	"github.com/samber/oops"
	"github.com/zigai/gotmux/tmux"
)

type RuntimeIdentities struct {
	Sessions map[string]tmux.SessionID
	Windows  map[string]tmux.WindowID
	Panes    map[string]tmux.PaneID
}

func ResolveRuntimeIdentities(ctx context.Context, server *tmux.Server, plan Plan, receipts map[string]string) (RuntimeIdentities, error) {
	index := indexPlan(plan)

	identities := RuntimeIdentities{
		Sessions: make(map[string]tmux.SessionID, len(plan.SourceGraph.Sessions)),
		Windows:  make(map[string]tmux.WindowID, len(plan.SourceGraph.Windows)),
		Panes:    make(map[string]tmux.PaneID, len(plan.SourceGraph.Panes)),
	}

	produced, err := resolvePaneIdentities(plan, receipts, &identities)
	if err != nil {
		return RuntimeIdentities{}, err
	}

	if err := resolveStructuralIdentities(plan, index, receipts, &identities); err != nil {
		return RuntimeIdentities{}, err
	}

	if err := resolveInitialPaneIdentities(ctx, server, plan, index, produced, &identities); err != nil {
		return RuntimeIdentities{}, err
	}

	return identities, nil
}

func InitialPaneID(ctx context.Context, server *tmux.Server, windowID string, runtimeID tmux.WindowID, produced map[tmux.PaneID]bool) (tmux.PaneID, error) {
	window, err := server.Window(ctx, runtimeID)
	if err != nil {
		return "", oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "resolve_window", "reason", "recorded_window_unavailable").
			Wrap(err)
	}

	return initialPaneIDOf(ctx, window, windowID, produced)
}

func resolvePaneIdentities(plan Plan, receipts map[string]string, identities *RuntimeIdentities) (map[string]map[tmux.PaneID]bool, error) {
	produced := map[string]map[tmux.PaneID]bool{}

	for _, step := range plan.Steps {
		if step.Kind != StepSplitPane {
			continue
		}

		windowID := step.SplitPane.WindowID
		if produced[windowID] == nil {
			produced[windowID] = map[tmux.PaneID]bool{}
		}

		id := receipts[step.ID]
		if id == "" {
			return nil, missingReceipt(step.ID)
		}

		produced[windowID][tmux.PaneID(id)] = true
		identities.Panes[step.SplitPane.PaneID] = tmux.PaneID(id)
	}

	return produced, nil
}

func resolveStructuralIdentities(plan Plan, index planIndex, receipts map[string]string, identities *RuntimeIdentities) error {
	for _, step := range plan.Steps {
		switch step.Kind {
		case StepCreateSession:
			if err := resolveCreatedSession(step, index, receipts, identities); err != nil {
				return err
			}
		case StepCreateWindow:
			id := receipts[step.ID]
			if id == "" {
				return missingReceipt(step.ID)
			}

			identities.Windows[step.CreateWindow.WindowID] = tmux.WindowID(id)
		case StepPlaceInitialWindow, StepSplitPane, StepLinkWindow, StepCloseBootstrapWindow,
			StepApplyLayout, StepSelectPane, StepApplyZoom, StepSelectWindow, StepSetPaneTitle:
		}
	}

	return nil
}

func resolveCreatedSession(step Step, index planIndex, receipts map[string]string, identities *RuntimeIdentities) error {
	id := receipts[step.ID]
	if id == "" {
		return missingReceipt(step.ID)
	}

	identities.Sessions[step.CreateSession.SessionID] = tmux.SessionID(id)

	if step.CreateSession.GroupLeaderSessionID != "" || step.CreateSession.InitialWindowID == "" {
		return nil
	}

	placement, ok := index.placeStepBySession[step.CreateSession.SessionID]
	if !ok {
		return missingReceipt(step.CreateSession.SessionID)
	}

	windowID := receipts[placement.ID]
	if windowID == "" {
		return missingReceipt(placement.ID)
	}

	identities.Windows[step.CreateSession.InitialWindowID] = tmux.WindowID(windowID)

	return nil
}

func resolveInitialPaneIdentities(
	ctx context.Context,
	server *tmux.Server,
	plan Plan,
	index planIndex,
	produced map[string]map[tmux.PaneID]bool,
	identities *RuntimeIdentities,
) error {
	for _, window := range plan.SourceGraph.Windows {
		initialSource := index.initialPaneByWindow[window.ID]
		if initialSource == "" {
			continue
		}

		runtimeID, ok := identities.Windows[window.ID]
		if !ok {
			return missingReceipt(window.ID)
		}

		initial, err := InitialPaneID(ctx, server, window.ID, runtimeID, produced[window.ID])
		if err != nil {
			return err
		}

		identities.Panes[initialSource] = initial
	}

	return nil
}

func initialPaneIDOf(ctx context.Context, window tmux.Window, windowID string, produced map[tmux.PaneID]bool) (tmux.PaneID, error) {
	panes, err := window.Panes(ctx)
	if err != nil {
		return "", oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "resolve_initial_pane", "window_id", windowID).
			Wrap(err)
	}

	var candidate tmux.PaneID

	found := 0

	for _, info := range panes {
		if produced[info.ID] {
			continue
		}

		candidate = info.ID
		found++
	}

	if found != 1 {
		return "", oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "resolve_initial_pane", "reason", "initial_pane_ambiguous").
			Wrapf(errAdoptedLayoutUnresolved, "window %s", windowID)
	}

	return candidate, nil
}

func missingReceipt(entity string) error {
	return oops.In("snapshot.restore").
		Code("operation_uncertain").
		With("operation", "resolve_identity", "reason", "runtime_receipt_missing").
		Wrapf(errRuntimeHandleUnusable, "%s", entity)
}
