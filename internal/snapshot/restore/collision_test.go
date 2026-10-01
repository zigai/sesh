//go:build linux || darwin

package restore

import (
	"testing"

	"github.com/zigai/sesh/internal/snapshot/model"
)

func collisionTestPlan() Plan {
	return Plan{
		SourceGraph: model.Manifest{
			Sessions: []model.Session{
				{ID: "s1", Name: model.Bytes("alpha")},
				{ID: "s2", Name: model.Bytes("beta")},
				{ID: "s3", Name: model.Bytes("gamma")},
			},
		},
		ClosureSessions: []string{"s1", "s2", "s3"},
		Steps: []Step{
			&CreateSessionStep{SessionID: "s2", GroupName: model.Bytes("shared")},
			&CreateSessionStep{SessionID: "s3", GroupName: model.Bytes("other")},
		},
	}
}

func TestNameCollisionsReportsOnlySessionsWhoseNameIsLive(t *testing.T) {
	t.Parallel()

	found := nameCollisions(collisionTestPlan(), map[string]bool{"alpha": true, "unrelated": true})

	if len(found) != 1 || found["s1"] != CollisionNameInUse {
		t.Fatalf("expected only s1 to collide by name, got %+v", found)
	}
}

func TestGroupCollisionsReportLiveGroupAndLiveNameButNotAlreadyCollidingSessions(t *testing.T) {
	t.Parallel()

	found := map[string]string{"s3": CollisionNameInUse}

	addGroupCollisions(found, collisionTestPlan(), map[string]bool{"other": true}, map[string]bool{"shared": true})

	if found["s2"] != CollisionGroupInUse {
		t.Fatalf("a live group with the saved group name must collide: %+v", found)
	}

	if found["s3"] != CollisionNameInUse {
		t.Fatalf("an existing name collision must keep its reason: %+v", found)
	}
}
