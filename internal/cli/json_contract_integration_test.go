//go:build integration

package cli

import (
	"path/filepath"
	"testing"

	"github.com/zigai/gotmux/tmuxtest"
)

func TestJSONDoctorWarningReportSuccessContract(t *testing.T) {
	home := isolateJSONContractHome(t)
	socket := tmuxtest.NewServer(t).Endpoint().SocketPath
	result := runJSONContract(
		t,
		"doctor",
		"doctor",
		testSocketFlag,
		socket,
		testRepositoryFlag,
		filepath.Join(home, "missing-repo"),
		"--history",
		filepath.Join(home, "missing-history"),
	)
	summary := contractJSONValue[map[string]any](t, result, "summary")
	assertJSONContractFields(t, summary, map[string]any{"fail": float64(0)})

	checks := contractJSONValue[[]any](t, result, "checks")
	server := contractJSONItem(t, checks, "id", "tmux.server")
	assertJSONContractFields(t, server, map[string]any{"group": "Tmux", "status": "ok"})
	_ = contractJSONValue[string](t, result, "version")
	_ = contractJSONValue[map[string]any](t, result, "paths")
}
