//go:build linux || darwin

package restore

import (
	"context"
	"sort"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
)

const (
	CollisionNameInUse  = "name_in_use"
	CollisionGroupInUse = "group_in_use"
)

type Collision struct {
	SessionID string
	Reason    string
}

// FindCollisions checks the plan's whole closure and only observes: the executor still refuses to
// create anything over a collision.
func FindCollisions(ctx context.Context, server *gotmux.Server, plan Plan) ([]Collision, error) {
	if server == nil {
		return nil, oops.In("snapshot.restore").Code("endpoint_required").Wrap(tmuxserver.ErrEndpoint)
	}

	live, err := server.Sessions(ctx)
	if err != nil {
		return nil, oops.In("snapshot.restore").Code("endpoint_required").With("operation", "find_collisions").Wrapf(err, "query live sessions")
	}

	liveNames, liveGroups := liveSessionNamespaces(live)
	found := nameCollisions(plan, liveNames)
	addGroupCollisions(found, plan, liveNames, liveGroups)

	collisions := make([]Collision, 0, len(found))
	for id, reason := range found {
		collisions = append(collisions, Collision{SessionID: id, Reason: reason})
	}

	sort.Slice(collisions, func(i, j int) bool { return collisions[i].SessionID < collisions[j].SessionID })

	return collisions, nil
}

func liveSessionNamespaces(live []gotmux.SessionInfo) (map[string]bool, map[string]bool) {
	names := make(map[string]bool, len(live))
	groups := make(map[string]bool, len(live))

	for _, session := range live {
		names[session.Name] = true

		if group, ok := session.Group.Get(); ok {
			groups[group] = true
		}
	}

	return names, groups
}

func nameCollisions(plan Plan, liveNames map[string]bool) map[string]string {
	names := make(map[string]string, len(plan.SourceGraph.Sessions))
	for _, session := range plan.SourceGraph.Sessions {
		names[session.ID] = string(session.Name)
	}

	found := make(map[string]string)

	for _, id := range plan.ClosureSessions {
		if name := names[id]; name != "" && liveNames[name] {
			found[id] = CollisionNameInUse
		}
	}

	return found
}

func addGroupCollisions(found map[string]string, plan Plan, liveNames, liveGroups map[string]bool) {
	for _, step := range plan.Steps {
		create, ok := step.(*CreateSessionStep)
		if !ok || len(create.GroupName) == 0 {
			continue
		}

		if _, already := found[create.SessionID]; already {
			continue
		}

		if liveNames[string(create.GroupName)] || liveGroups[string(create.GroupName)] {
			found[create.SessionID] = CollisionGroupInUse
		}
	}
}
