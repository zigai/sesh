//go:build linux || darwin

package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const (
	CollisionPolicyDefault CollisionPolicy = ""
	CollisionPolicySkip    CollisionPolicy = "skip"
	CollisionPolicyFail    CollisionPolicy = "fail"

	skipReasonLinkedCollision = "linked_session_collides"
)

var ErrNoServerRunning = errors.New("no tmux server is running on the selected socket; start tmux there first, then retry")

// CollisionPolicy selects what restore does with saved sessions whose names are already in use on
// the live server. The zero value skips them when every saved session was requested and fails when
// the caller named sessions explicitly.
type CollisionPolicy string

// SkippedSession is a saved session restore declined to create, with a fixed reason.
type SkippedSession struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func planFailureReason(current string, err error) string {
	if errors.Is(err, restore.ErrSessionCollision) || errors.Is(err, restore.ErrGroupCollision) {
		return "collision"
	}

	return current
}

// planSelection builds the plan for the selected sessions. A resumed operation keeps the session
// set it registered, so the executor's adoption-aware guard decides which live sessions are its
// own. A new operation is planned around live collisions instead.
func planSelection(
	ctx context.Context,
	repository *store.Repository,
	server *gotmux.Server,
	manifest model.Manifest,
	selected []string,
	cfg RestoreConfig,
) (restore.Plan, []SkippedSession, error) {
	resumed, err := restoreOperationExists(ctx, repository, cfg.OperationID)
	if err != nil {
		return restore.Plan{}, nil, err
	}

	if resumed {
		plan, err := restore.BuildPlan(manifest, selected)
		if err != nil {
			return restore.Plan{}, nil, fmt.Errorf("build restore plan: %w", err)
		}

		return plan, nil, nil
	}

	return planAroundCollisions(ctx, server, manifest, selected, effectiveCollisionPolicy(cfg))
}

func effectiveCollisionPolicy(cfg RestoreConfig) CollisionPolicy {
	switch {
	case cfg.OnCollision != CollisionPolicyDefault:
		return cfg.OnCollision
	case len(cfg.Sessions) > 0:
		return CollisionPolicyFail
	default:
		return CollisionPolicySkip
	}
}

func restoreOperationExists(ctx context.Context, repository *store.Repository, operationID string) (bool, error) {
	if operationID == "" {
		return false, nil
	}

	catalog, err := repository.Inspect(ctx)
	if err != nil {
		return false, fmt.Errorf("inspect operations: %w", err)
	}

	return slices.ContainsFunc(catalog.Operations, func(op store.Operation) bool { return op.ID == operationID }), nil
}

// planAroundCollisions builds the restore plan for the selected sessions. A session whose name or
// group is already live, or that is linked to one that is, is skipped under the skip policy and
// reported under the fail policy. Nothing is created or removed on the live server.
func planAroundCollisions(
	ctx context.Context,
	server *gotmux.Server,
	manifest model.Manifest,
	selected []string,
	policy CollisionPolicy,
) (restore.Plan, []SkippedSession, error) {
	var skipped []SkippedSession

	for len(selected) > 0 {
		plan, err := restore.BuildPlan(manifest, selected)
		if err != nil {
			return restore.Plan{}, skipped, fmt.Errorf("build restore plan: %w", err)
		}

		collisions, err := restore.FindCollisions(ctx, server, plan)
		if err != nil {
			return restore.Plan{}, skipped, fmt.Errorf("find session collisions: %w", err)
		}

		if len(collisions) == 0 {
			return plan, skipped, nil
		}

		kept, blocked, err := partitionSelection(manifest, selected, collisions)
		if err != nil {
			return restore.Plan{}, skipped, err
		}

		skipped = append(skipped, blocked...)

		if policy == CollisionPolicyFail || len(kept) == 0 {
			break
		}

		selected = kept
	}

	return restore.Plan{}, skipped, oops.In("snapshot").
		Code("restore_conflict").
		With("operation", "restore", "phase", "collision").
		Wrap(restore.ErrSessionCollision)
}

// partitionSelection splits the selection into sessions that can still be restored and sessions
// whose closure includes a colliding session.
func partitionSelection(manifest model.Manifest, selected []string, collisions []restore.Collision) ([]string, []SkippedSession, error) {
	colliding := make(map[string]string, len(collisions))
	for _, collision := range collisions {
		colliding[collision.SessionID] = collision.Reason
	}

	names := make(map[string]string, len(manifest.Sessions))
	for _, session := range manifest.Sessions {
		names[session.ID] = string(session.Name)
	}

	kept := make([]string, 0, len(selected))

	var blocked []SkippedSession

	for _, id := range selected {
		reason, isBlocked, err := selectionBlocker(manifest, id, colliding)
		if err != nil {
			return nil, nil, err
		}

		if isBlocked {
			blocked = append(blocked, SkippedSession{ID: id, Name: names[id], Reason: reason})
		} else {
			kept = append(kept, id)
		}
	}

	return kept, blocked, nil
}

// selectionBlocker reports whether restoring the selected session would need a colliding one.
func selectionBlocker(manifest model.Manifest, id string, colliding map[string]string) (string, bool, error) {
	if reason, ok := colliding[id]; ok {
		return reason, true, nil
	}

	plan, err := restore.BuildPlan(manifest, []string{id})
	if err != nil {
		return "", false, fmt.Errorf("build restore plan: %w", err)
	}

	for _, member := range plan.ClosureSessions {
		if _, ok := colliding[member]; ok {
			return skipReasonLinkedCollision, true, nil
		}
	}

	return "", false, nil
}
