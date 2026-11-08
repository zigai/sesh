//go:build linux || darwin

package picker

import (
	"testing"

	"github.com/zigai/aht/v2/pkg/aht"
	"github.com/zigai/aht/v2/pkg/registry"

	"github.com/zigai/sesh/internal/agents"
)

func TestTitleLookupPreservesNativeIdentityWithCurrentSchema(t *testing.T) {
	t.Parallel()

	agent := agents.AgentSession{
		ID: "rediscovered-agent", Harness: string(aht.HarnessClaude),
		HarnessSessionID: "native-conversation", SessionPath: "/sessions/native-conversation.jsonl",
	}

	lookup, ok := titleLookupSession(agent)
	if !ok {
		t.Fatal("native identity must enable title lookup")
	}

	if lookup.SchemaVersion != registry.StoreSchemaVersion ||
		lookup.SessionID != agent.HarnessSessionID || lookup.SessionPath != agent.SessionPath {
		t.Fatalf("title lookup lost current schema or native identity: %#v", lookup)
	}
}
