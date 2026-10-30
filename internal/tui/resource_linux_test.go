//go:build linux

package tui

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui/providers"
)

func TestResourceProviderSamplesWithoutBlockingRender(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	pid := 1234
	writeResourceProcFixture(t, root, pid)

	cfg := config.DefaultConfig()
	cfg.AgentSessions.Enabled = true
	cfg.AgentSessions.PopupCounts = true
	cfg.Display.AgentsAgentFormat = "{tree}{pane_cpu} {pane_mem}{right}{status}"

	model, err := NewPopupModel(context.Background(), cfg, testState(), testServices(Dependencies{
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) { return nil, nil },
	}))
	if err != nil {
		t.Fatal(err)
	}

	model.resourceProcRoot = root
	model.applySessionRows(sessionLoadResultMsg{sessions: []session.Session{{
		Name: "work", Kind: session.KindLive, Origin: session.OriginLocal,
		Agents: []session.AgentSession{{PaneID: "%1", PanePID: pid, Status: session.AgentStatusRunning}},
	}}, status: session.StatusComplete})

	if got := model.providerRunner.Cell(providers.Resource, "%1", display.FieldPaneCPU).State; got != layout.Pending {
		t.Fatalf("initial cpu state = %v", got)
	}

	command := model.loadResourceCommand()
	if command == nil {
		t.Fatal("resource command not scheduled")
	}

	msg, ok := command().(resourceResultMsg)
	if !ok || msg.err != nil {
		t.Fatalf("resource result = %+v", msg)
	}

	model.handleResource(msg)

	if got := model.providerRunner.Cell(providers.Resource, "%1", display.FieldPaneMem); got.State != layout.Present || got.Text == "" {
		t.Fatalf("memory cell = %+v", got)
	}

	if got := model.providerRunner.Cell(providers.Resource, "%1", display.FieldPaneCPU).State; got != layout.Pending {
		t.Fatalf("first cpu sample state = %v", got)
	}

	model.Close()
}

func writeResourceProcFixture(t *testing.T, root string, pid int) {
	t.Helper()

	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	fields := make([]string, 20)
	for i := range fields {
		fields[i] = "0"
	}

	fields[0], fields[1], fields[11], fields[12], fields[19] = "S", "1", "100", "0", "10"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte("1234 (test) "+strings.Join(fields, " ")), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "statm"), []byte("10 5 0"), 0o600); err != nil {
		t.Fatal(err)
	}
}
