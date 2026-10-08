//go:build linux && integration

package cli_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	integration "github.com/zigai/sesh/internal/integration/tmux"
	"github.com/zigai/sesh/internal/remote"
	"github.com/zigai/sesh/internal/snapshot/restore"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestJSONSnapshotWorkflowSuccessContracts(t *testing.T) {
	home := isolateJSONContractHome(t)
	server := newCaptureCLIServer(t)
	source := newAutosaveSession(t, server)
	socket := server.Endpoint().SocketPath
	repository := filepath.Join(home, "repository")
	lease := filepath.Join(home, "leases")
	args := []string{"snapshot", "save", testSocketFlag, socket, testRepositoryFlag, repository, testLeaseRootFlag, lease, string(source.ID())}
	saved := runJSONContract(t, "snapshot.save", args...)
	assertJSONContractFields(t, saved, map[string]any{contractStatusField: "best_effort"})
	generation := contractJSONValue[string](t, saved, "generation")

	attempt := contractJSONValue[string](t, saved, "attempt")
	if generation == "" || attempt == "" {
		t.Fatal("save omitted durable identities")
	}

	assertSnapshotReadContracts(t, repository, generation, attempt)

	if err := source.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	operation := store.NewOperationID()
	restored := runJSONContract(t, "snapshot.restore", "snapshot", "restore", testSocketFlag, socket, testRepositoryFlag, repository, testLeaseRootFlag, lease, testGenerationFlag, generation, "--operation", operation, "--no-processes")
	assertJSONContractFields(t, restored, map[string]any{"generation": generation, "operationId": operation, contractStatusField: "best_effort", "verified": true, "uncertain": false})
	_ = contractJSONItem(t, contractJSONValue[[]any](t, restored, "sessions"), "name", "autosave-alpha")
	_ = contractJSONValue[[]any](t, restored, "missingRequirements")
	reconciled := runJSONContract(t, "snapshot.recover.restore.show", snapshotCmd, "recover", "restore", "show", operation, testRepositoryFlag, repository)
	assertJSONContractFields(t, reconciled, map[string]any{"operationId": operation, "state": "complete", "settled": true})

	pruned := runJSONContract(t, "snapshot.prune", snapshotCmd, "prune", testRepositoryFlag, repository, "--retention-days", "0", "--max-count", "0", "--max-bytes", "0")
	assertJSONContractFields(t, pruned, map[string]any{"budgetUnmet": false, "protectedBytes": float64(0), "retained": []any{generation}})
	assertJSONContractFields(t, pruned, map[string]any{"removed": nil})
	collected := runJSONContract(t, "snapshot.gc", snapshotCmd, "gc", testRepositoryFlag, repository, "--max-generations", "1", "--max-bytes", "100000000")
	assertJSONContractFields(t, collected, map[string]any{"bytes": float64(0), "resumed": false})
	assertJSONContractFields(t, collected, map[string]any{"removed": nil})
}

func assertSnapshotReadContracts(t *testing.T, repository, generation, attempt string) {
	t.Helper()
	listed := runJSONContract(t, "snapshot.list", snapshotCmd, "list", testRepositoryFlag, repository)
	assertJSONContractFields(t, listed, map[string]any{"generation": generation})
	rows := contractJSONValue[[]any](t, listed, "sessions")
	row := contractJSONItem(t, rows, "name", "autosave-alpha")
	assertJSONContractFields(t, row, map[string]any{"windows": float64(1)})
	_ = contractJSONValue[string](t, row, "id")
	_ = contractJSONValue[string](t, row, "directory")

	history := runJSONContract(t, "snapshot.history", snapshotCmd, "history", testRepositoryFlag, repository)
	assertJSONContractFields(t, history, map[string]any{"current": generation})
	attempts := contractJSONValue[[]any](t, history, "attempts")
	_ = contractJSONItem(t, attempts, "id", attempt)
	generations := contractJSONValue[[]any](t, history, "generations")
	current := contractJSONItem(t, generations, "id", generation)
	assertJSONContractFields(t, current, map[string]any{"current": true, "complete": false})
	_ = contractJSONValue[float64](t, current, "sequence")
	_ = contractJSONValue[string](t, current, "manifestSha256")

	verified := runJSONContract(t, "snapshot.verify", snapshotCmd, "verify", testRepositoryFlag, repository, testGenerationFlag, generation)
	assertJSONContractFields(t, verified, map[string]any{"generation": generation, "byteIntegrity": true, "sourceEvidenceComplete": false, "restoreReady": true, "sessions": float64(1), "windows": float64(1), "panes": float64(1)})
	_ = contractJSONValue[float64](t, verified, "artifacts")
	_ = contractJSONValue[[]any](t, verified, "evidence")
}

func TestJSONRemoteActionAndLookupSuccessContracts(t *testing.T) {
	fixture := newRemoteSSHFixture(t)
	isolateJSONContractHome(t)

	for key, value := range remoteFixtureEnvironment(fixture) {
		t.Setenv(key, value)
	}

	configPath := filepath.Join(fixture.configHome, "sesh", "config.toml")
	writeTestConfig(t, configPath, fmt.Sprintf("[remote]\nenabled = true\nssh_path = %q\n\n", fixture.sshWrapper)+
		remoteConnectionsTOML(fixture.connection))

	peer, err := integration.Open(fixture.connection.TmuxSocket)
	if err != nil {
		t.Fatal(err)
	}

	publishRemoteReturnCatalog(t, fixture, peer)
	execution := executeJSON(t, sessionCmd, "create", "json-ssh-session", "--dir", fixture.root, "--connection", fixture.connection.ID, testConfigFlag, configPath, testJSONFlag)
	result := assertJSONContractSuccess(t, "session.create", execution)
	assertJSONContractFields(t, contractJSONValue[map[string]any](t, result, "session"), map[string]any{"kind": "live", "name": "json-ssh-session"})

	if _, err := peer.FindSession(t.Context(), "json-ssh-session"); err != nil {
		t.Fatalf("successful remote action did not create session: %v", err)
	}

	invocation := peerReceiptID(t, fixture.stateHome)
	lookup := executeJSON(t, "remote", "lookup", fixture.connection.ID, invocation, testConfigFlag, configPath, testJSONFlag)
	lookedUp := assertJSONContractSuccess(t, "remote.lookup", lookup)
	assertJSONContractFields(t, lookedUp, map[string]any{"ok": true, "invocationId": invocation, "action": "session.create"})
	assertJSONContractFields(t, contractJSONValue[map[string]any](t, lookedUp, "output"), map[string]any{"directory": fixture.root})
}

// peerReceiptID returns the one invocation the peer recorded.
func peerReceiptID(t *testing.T, stateHome string) string {
	t.Helper()

	receipts, err := remote.InvocationPath(stateHome)
	if err != nil {
		t.Fatal(err)
	}

	matches, err := filepath.Glob(filepath.Join(receipts, "*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("peer receipts = %v, %v; want one", matches, err)
	}

	return strings.TrimSuffix(filepath.Base(matches[0]), ".json")
}

func TestJSONSnapshotActionsAndAbandonSuccessContracts(t *testing.T) {
	home := isolateJSONContractHome(t)
	server := newCaptureCLIServer(t)
	sourceDir := filepath.Join(home, "source-directory")

	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}

	source := newAutosaveSessionAt(t, server, sourceDir)
	socket := server.Endpoint().SocketPath
	repository := filepath.Join(home, "repository")
	lease := filepath.Join(home, "leases")
	saved := runJSONContract(t, "snapshot.save", "snapshot", "save", testSocketFlag, socket, testRepositoryFlag, repository, testLeaseRootFlag, lease, string(source.ID()))
	assertJSONContractFields(t, saved, map[string]any{contractStatusField: "best_effort"})

	generation := contractJSONValue[string](t, saved, "generation")
	if generation == "" || contractJSONValue[string](t, saved, "attempt") == "" {
		t.Fatal("action save omitted durable identities")
	}

	// A restore that fails after admission leaves a pending operation to inspect and abandon.
	// A refused collision leaves none: it is refused before any intent is registered.
	if err := source.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(sourceDir); err != nil {
		t.Fatal(err)
	}

	operation := store.NewOperationID()

	failed := executeJSON(t, "snapshot", "restore", testSocketFlag, socket, testRepositoryFlag, repository, testLeaseRootFlag, lease, testGenerationFlag, generation, "--operation", operation, "--no-processes", testNoConfigFlag, testJSONFlag)
	if !errors.Is(failed.err, restore.ErrWorkingDirNotFound) {
		t.Fatalf("expected a missing working directory failure after admission: %v", failed.err)
	}

	pending := runJSONContract(t, "snapshot.recover.restore.show", snapshotCmd, "recover", "restore", "show", operation, testRepositoryFlag, repository)
	assertJSONContractFields(t, pending, map[string]any{"operationId": operation, "source": generation, "state": "active", "settled": false})
	abandoned := runJSONContract(t, "snapshot.recover.restore.abandon", snapshotCmd, "recover", "restore", "abandon", operation, testRepositoryFlag, repository)
	assertJSONContractFields(t, abandoned, map[string]any{"operationId": operation, "source": generation, "state": "abandoned", "settled": false})
	_ = contractJSONValue[string](t, abandoned, "target")

	_ = contractJSONValue[[]any](t, abandoned, "actions")

	if _, err := server.FindSession(t.Context(), "autosave-alpha"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("a failed restore and its abandonment must not leave a session behind: %v", err)
	}

	restored := runJSONContract(t, "snapshot.restore", "snapshot", "restore", testSocketFlag, socket, testRepositoryFlag, repository, testLeaseRootFlag, lease, testGenerationFlag, generation, "--no-processes", "--allow-missing-dir")
	assertJSONContractFields(t, restored, map[string]any{"generation": generation, contractStatusField: "best_effort", "verified": true, "uncertain": false})

	if contractJSONValue[string](t, restored, "operationId") == "" {
		t.Fatal("action restore omitted operation receipt")
	}

	if _, err := server.FindSession(t.Context(), "autosave-alpha"); err != nil {
		t.Fatalf("action restore did not recreate source: %v", err)
	}
}
