//go:build (linux || darwin) && integration

package remote

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/zigai/sesh/internal/app"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
)

func TestPeerLocalWorkspaceRouting(t *testing.T) {
	for _, environment := range [][]string{{"REMOTE_KEY", "VIRTUAL_ENV"}, {}} {
		cfg := config.DefaultConfig()
		cfg.Snapshot.ProcessEnvironment = environment
		cfg.AgentStatus.StorePath = "/custom/aht/remote.json"
		cfg.AgentSessions.Status = &config.AgentStatusConfig{StorePath: "/custom/aht/nested.json"}

		workspace, err := app.Resolve(
			cfg,
			app.Locations{Socket: filepath.Join(t.TempDir(), "absent.sock")},
		)
		if err != nil {
			t.Fatal(err)
		}

		server, err := tmuxserver.Open(workspace.Socket)
		if err != nil {
			t.Fatal(err)
		}

		local, repository := newPeerLocal(workspace, server)
		if local.Server() != server || local.Workflow() == nil {
			t.Fatal("peer local did not retain its workspace server and workflow")
		}

		if err := repository.Close(); err != nil {
			t.Fatal(err)
		}

		keys, err := workspace.ProcessEnvironment()
		if err != nil || !slices.Equal(keys, environment) {
			t.Fatalf("process environment = %#v, %v; want %#v", keys, err, environment)
		}

		if workspace.AgentSource().StorePath() != "/custom/aht/nested.json" {
			t.Fatalf(
				"peer source did not use the resolved nested store path: %q",
				workspace.AgentStore,
			)
		}
	}
}
