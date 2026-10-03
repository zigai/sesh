//go:build linux

package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

const otherBootID = "00000000-1111-2222-3333-444444444444"

type settleFixture struct {
	repository *store.Repository
	generation string
}

func newSettleFixture(t *testing.T) settleFixture {
	t.Helper()

	repository, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "repository"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}

	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close repository: %v", err)
		}
	})

	attempt, err := repository.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin snapshot: %v", err)
	}

	generation, err := repository.Commit(t.Context(), attempt.ID, observationFixture().Manifest, nil)
	if err != nil {
		t.Fatalf("commit snapshot: %v", err)
	}

	return settleFixture{repository: repository, generation: generation.ID}
}

// register records an active restore aimed at the daemon with pid and birth.
func (f settleFixture) register(t *testing.T, pid int, birth string) string {
	t.Helper()

	endpoint := "/tmp/settle.sock"
	daemon := restoreDaemonIdentity{
		Server: gotmux.ServerIdentity{
			Endpoint:       gotmux.Endpoint{SocketPath: endpoint},
			ReportedSocket: endpoint,
			PID:            pid,
			Started:        time.Unix(100, 0),
		},
		Birth: birth,
	}

	target, err := restoreTarget(endpoint, daemon, RestoreConfig{})
	if err != nil {
		t.Fatalf("encode restore target: %v", err)
	}

	actions := []store.Action{
		{ID: restore.ImportStateActionID, Kind: restore.KindConfigure, State: "planned"},
		{ID: restore.VerifyActionID, Kind: restore.KindVerify, State: "planned"},
	}

	id := store.NewOperationID()
	if _, err := f.repository.RegisterOperation(t.Context(), id, f.generation, target, actions); err != nil {
		t.Fatalf("register restore: %v", err)
	}

	return id
}

func (f settleFixture) states(t *testing.T) map[string]string {
	t.Helper()

	catalog, err := f.repository.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	states := make(map[string]string, len(catalog.Operations))
	for _, op := range catalog.Operations {
		states[op.ID] = op.State
	}

	return states
}

func exitedPID(t *testing.T) int {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run short-lived process: %v", err)
	}

	return cmd.Process.Pid
}

func TestSettleDeadRestoresAbandonsOnlyRestoresWhoseDaemonIsProvablyGone(t *testing.T) {
	fixture := newSettleFixture(t)

	self, err := process.ReadIdentity(t.Context(), os.Getpid())
	if err != nil {
		t.Fatalf("read own identity: %v", err)
	}

	earlierBoot := fixture.register(t, os.Getpid(), otherBootID+":1")
	exited := fixture.register(t, exitedPID(t), self.BootID+":1")
	alive := fixture.register(t, os.Getpid(), self.String())

	var output bytes.Buffer

	ctx := zerolog.New(&output).WithContext(t.Context())

	settled, err := SettleDeadRestores(ctx, fixture.repository)
	if err != nil {
		t.Fatalf("SettleDeadRestores: %v", err)
	}

	if len(settled) != 2 {
		t.Fatalf("settled %d restores, want the 2 with gone daemons: %+v", len(settled), settled)
	}

	states := fixture.states(t)
	if states[earlierBoot] != "abandoned" || states[exited] != "abandoned" || states[alive] != "active" {
		t.Fatalf("operation states = %v", states)
	}

	assertSettleRecords(t, output.Bytes(), 2)

	active, err := ActiveRestores(t.Context(), fixture.repository)
	if err != nil || len(active) != 1 || active[0].OperationID != alive {
		t.Fatalf("ActiveRestores = %+v, %v; want only the restore with a live daemon", active, err)
	}
}

func TestSettleDeadRestoresKeepsRestoresWithoutProofOfDeath(t *testing.T) {
	fixture := newSettleFixture(t)

	unrecorded := fixture.register(t, exitedPID(t), "")

	var output bytes.Buffer

	settled, err := SettleDeadRestores(zerolog.New(&output).WithContext(t.Context()), fixture.repository)
	if err != nil || len(settled) != 0 {
		t.Fatalf("SettleDeadRestores = %+v, %v; want nothing settled without a recorded birth", settled, err)
	}

	if fixture.states(t)[unrecorded] != "active" || output.Len() != 0 {
		t.Fatalf("a restore without proof must stay active and silent: %v %q", fixture.states(t), output.String())
	}
}

func assertSettleRecords(t *testing.T, data []byte, want int) {
	t.Helper()

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != want {
		t.Fatalf("expected %d abandonment records, got %d: %s", want, len(lines), data)
	}

	for _, line := range lines {
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}

		if record["level"] != "warn" || record["component"] != "snapshot" || record["operation"] != "restore" ||
			record["status"] != "abandoned" || record["reason"] != "target_daemon_gone" {
			t.Fatalf("unexpected record: %+v", record)
		}

		if strings.Contains(line, "/tmp/settle.sock") || strings.Contains(line, otherBootID) {
			t.Fatalf("record leaks the target: %s", line)
		}
	}
}
