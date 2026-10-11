//go:build linux || darwin

package restore

import (
	"context"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

type RuntimeIdentities struct {
	Sessions map[string]gotmux.SessionID
	Windows  map[string]gotmux.WindowID
	Panes    map[string]gotmux.PaneID
}

func ResolveRuntimeIdentities(ctx context.Context, server *gotmux.Server, plan Plan, receipts map[string]string) (RuntimeIdentities, error) {
	index := indexPlan(plan)

	identities := RuntimeIdentities{
		Sessions: make(map[string]gotmux.SessionID, len(plan.SourceGraph.Sessions)),
		Windows:  make(map[string]gotmux.WindowID, len(plan.SourceGraph.Windows)),
		Panes:    make(map[string]gotmux.PaneID, len(plan.SourceGraph.Panes)),
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

func InitialPaneID(ctx context.Context, server *gotmux.Server, windowID string, runtimeID gotmux.WindowID, produced map[gotmux.PaneID]bool) (gotmux.PaneID, error) {
	window, err := server.Window(ctx, runtimeID)
	if err != nil {
		return "", oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "resolve_window", "reason", "recorded_window_unavailable").
			Wrap(err)
	}

	return initialPaneIDOf(ctx, window, windowID, produced)
}

func resolvePaneIdentities(plan Plan, receipts map[string]string, identities *RuntimeIdentities) (map[string]map[gotmux.PaneID]bool, error) {
	produced := map[string]map[gotmux.PaneID]bool{}

	for _, step := range plan.Steps {
		split, ok := step.(*SplitPaneStep)
		if !ok {
			continue
		}

		windowID := split.WindowID
		if produced[windowID] == nil {
			produced[windowID] = map[gotmux.PaneID]bool{}
		}

		id := receipts[split.ID()]
		if id == "" {
			return nil, missingReceipt(split.ID())
		}

		produced[windowID][gotmux.PaneID(id)] = true
		identities.Panes[split.PaneID] = gotmux.PaneID(id)
	}

	return produced, nil
}

func resolveStructuralIdentities(plan Plan, index planIndex, receipts map[string]string, identities *RuntimeIdentities) error {
	for _, step := range plan.Steps {
		switch step := step.(type) {
		case *CreateSessionStep:
			if err := resolveCreatedSession(step, index, receipts, identities); err != nil {
				return err
			}
		case *CreateWindowStep:
			id := receipts[step.ID()]
			if id == "" {
				return missingReceipt(step.ID())
			}

			identities.Windows[step.WindowID] = gotmux.WindowID(id)
		case *PlaceInitialWindowStep, *SplitPaneStep, *LinkWindowStep, *CloseBootstrapWindowStep,
			*ApplyLayoutStep, *SelectPaneStep, *ApplyZoomStep, *SelectWindowStep, *SetPaneTitleStep:
		}
	}

	return nil
}

func resolveCreatedSession(step *CreateSessionStep, index planIndex, receipts map[string]string, identities *RuntimeIdentities) error {
	id := receipts[step.ID()]
	if id == "" {
		return missingReceipt(step.ID())
	}

	identities.Sessions[step.SessionID] = gotmux.SessionID(id)

	if step.GroupLeaderSessionID != "" || step.InitialWindowID == "" {
		return nil
	}

	placement, ok := index.placeStepBySession[step.SessionID]
	if !ok {
		return missingReceipt(step.SessionID)
	}

	windowID := receipts[placement.ID()]
	if windowID == "" {
		return missingReceipt(placement.ID())
	}

	identities.Windows[step.InitialWindowID] = gotmux.WindowID(windowID)

	return nil
}

func resolveInitialPaneIdentities(
	ctx context.Context,
	server *gotmux.Server,
	plan Plan,
	index planIndex,
	produced map[string]map[gotmux.PaneID]bool,
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

func initialPaneIDOf(ctx context.Context, window gotmux.Window, windowID string, produced map[gotmux.PaneID]bool) (gotmux.PaneID, error) {
	panes, err := window.Panes(ctx)
	if err != nil {
		return "", oops.In("snapshot.restore").
			Code("operation_uncertain").
			With("operation", "resolve_initial_pane", "window_id", windowID).
			Wrap(err)
	}

	var candidate gotmux.PaneID

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
