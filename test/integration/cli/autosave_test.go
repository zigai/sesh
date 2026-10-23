//go:build (linux || darwin) && integration

package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/automation"
)

const (
	autosaveStatusSaved = "saved"
)

type autosaveResultJSON struct {
	Status           string `json:"status"`
	Reason           string `json:"reason"`
	Attempt          string `json:"attempt,omitempty"`
	Generation       string `json:"generation,omitempty"`
	Snapshot         string `json:"snapshotStatus,omitempty"`
	HistoryState     string `json:"historyState,omitempty"`
	RepositoryState  string `json:"repositoryState,omitempty"`
	FinalSaveState   string `json:"finalSaveState,omitempty"`
	Recorded         bool   `json:"recorded"`
	ElapsedSec       int64  `json:"elapsedSeconds"`
	IntervalM        int    `json:"intervalMinutes"`
	DaemonState      string `json:"daemonState"`
	DaemonPhase      string `json:"daemonPhase,omitempty"`
	InhibitorsHeld   bool   `json:"inhibitorsHeld"`
	LogindDelayMS    int64  `json:"logindDelayMillis,omitempty"`
	ServerManagement string `json:"serverManagement,omitempty"`
}

func isolateAutosaveEnv(t *testing.T) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("TMUX", "")
}

func dataHome(t *testing.T) string {
	t.Helper()

	home := os.Getenv("XDG_DATA_HOME")
	if home == "" {
		t.Fatal("XDG_DATA_HOME is not isolated")
	}

	return home
}

func disableFixtureRename(t *testing.T, server *gotmux.Server) {
	t.Helper()

	windows, err := server.Windows(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, window := range windows {
		options := window.Handle().Options()

		if err := options.SetAutomaticRename(t.Context(), false); err != nil {
			t.Fatal(err)
		}

		if err := options.SetAllowRename(t.Context(), false); err != nil {
			t.Fatal(err)
		}
	}
}

func newAutosaveSession(t *testing.T, server *gotmux.Server) gotmux.Session {
	t.Helper()

	return newAutosaveSessionAt(t, server, "")
}

func newAutosaveSessionAt(t *testing.T, server *gotmux.Server, dir string) gotmux.Session {
	t.Helper()

	ctx := t.Context()

	sess, err := server.NewSession(ctx, gotmux.NewSessionOptions{
		Name:    "autosave-alpha",
		Dir:     dir,
		Window:  "main",
		Program: gotmux.Exec("sleep", "300"),
		Env:     nil,
		TmuxEnv: nil,
		Size:    gotmux.Size{Width: 100, Height: 30},
		Start:   gotmux.StartPolicyExistingOnly,
		Group:   "",
	})
	if err != nil {
		t.Fatalf("NewSession failed: %v", err)
	}

	disableFixtureRename(t, server)

	return sess
}

func runDueSave(t *testing.T, socketPath, sessionID string) autosaveResultJSON {
	t.Helper()

	exec := executeJSON(
		t, "autosave", "save-if-due",
		testSocketFlag, socketPath,
		testSessionFlag, sessionID,
		testNoConfigFlag, testJSONFlag,
	)
	if !exec.envelope.OK {
		t.Fatalf("first autosave failed: %+v\nstderr: %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[autosaveResultJSON](t, exec.envelope.Result)
	if result.Status != "saved" {
		t.Fatalf("first status = %q, want saved", result.Status)
	}

	if result.Generation == "" {
		t.Fatal("first autosave returned an empty generation")
	}

	if result.Snapshot != "best_effort" {
		t.Fatalf("snapshot status = %q, want best_effort", result.Snapshot)
	}

	return result
}

func runSecondSave(t *testing.T, socketPath string) {
	t.Helper()

	exec := executeJSON(t, "autosave", "save-if-due", testSocketFlag, socketPath, testNoConfigFlag, testJSONFlag)
	if !exec.envelope.OK {
		t.Fatalf("second autosave failed: %s", exec.stderr)
	}

	result := decodeJSONPayload[autosaveResultJSON](t, exec.envelope.Result)
	if result.Status != "not_due" {
		t.Fatalf("second status = %q, want not_due", result.Status)
	}

	if result.Generation != "" {
		t.Fatalf("second autosave must not publish a generation, got %q", result.Generation)
	}
}

func assertAutomaticPracticalRestore(t *testing.T, socketPath string, server *gotmux.Server) {
	t.Helper()

	exec := executeJSON(t, "autosave", "restore-on-start", testSocketFlag, socketPath, testNoConfigFlag, testJSONFlag)
	if exec.err != nil || !exec.envelope.OK {
		t.Fatalf("automatic practical restore failed: %v, %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[autosaveResultJSON](t, exec.envelope.Result)
	if result.Status != "best_effort" || result.Reason != "fidelity_limited" {
		t.Fatalf("result = %#v, want best_effort/fidelity_limited", result)
	}

	if result.Generation == "" {
		t.Fatal("restore must carry the durable generation identity")
	}

	if _, err := server.FindSession(t.Context(), "autosave-alpha"); err != nil {
		t.Fatalf("automatic restore did not reconstruct session: %v", err)
	}
}

func assertExplicitRestore(t *testing.T, socketPath, generation string, server *gotmux.Server) {
	t.Helper()

	exec := executeJSON(
		t, "snapshot", "restore",
		testRepositoryFlag, filepath.Join(dataHome(t), "sesh", "snapshots"),
		testSocketFlag, socketPath,
		testLeaseRootFlag, filepath.Join(dataHome(t), "sesh", "leases"),
		testGenerationFlag, generation,
		testJSONFlag,
	)

	if exec.err != nil || !exec.envelope.OK || exec.envelope.Error != nil {
		t.Fatalf("practical restore failed: %v, %s", exec.err, exec.stderr)
	}

	result := decodeJSONPayload[snapshotRestoreResultJSON](t, exec.envelope.Result)
	if result.OperationID == "" {
		t.Fatal("restore result must preserve the durable operation ID")
	}

	if result.Generation != generation {
		t.Fatalf("restored generation = %q, want %q", result.Generation, generation)
	}

	restored, err := server.FindSession(t.Context(), "autosave-alpha")
	if err != nil {
		t.Fatalf("restored session missing: %v", err)
	}

	info, err := restored.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if info.Name != "autosave-alpha" {
		t.Fatalf("restored session name = %q, want autosave-alpha", info.Name)
	}
}

func TestAutosaveSaveIfDueAndRestoreOnStart(t *testing.T) {
	server := tmuxtest.NewServer(t)
	sess := newAutosaveSession(t, server)

	socketPath := server.Endpoint().SocketPath

	isolateAutosaveEnv(t)

	first := runDueSave(t, socketPath, string(sess.ID()))
	runSecondSave(t, socketPath)

	if err := sess.Kill(t.Context()); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	assertAutomaticPracticalRestore(t, socketPath, server)

	restored, err := server.FindSession(t.Context(), "autosave-alpha")
	if err != nil {
		t.Fatal(err)
	}

	if err := restored.Kill(t.Context()); err != nil {
		t.Fatal(err)
	}

	assertExplicitRestore(t, socketPath, first.Generation, server)
}

func TestAutosaveFinalSaveRecordsPracticalRecovery(t *testing.T) {
	server := tmuxtest.NewServer(t)
	sess := newAutosaveSession(t, server)

	socketPath := server.Endpoint().SocketPath

	isolateAutosaveEnv(t)

	statusPath := filepath.Join(dataHome(t), "final-save-status.json")

	exec := executeJSON(
		t, "autosave", "final-save",
		testSocketFlag, socketPath,
		testSessionFlag, string(sess.ID()),
		testNoConfigFlag, testJSONFlag,
		"--deadline", "20s",
		"--status-file", statusPath,
	)
	result := assertPracticalFinalSaveResult(t, exec)

	assertPracticalFinalSaveStatus(t, statusPath, result.Generation)
	assertAutosaveNotDueAfterFinalSave(t, socketPath)
}

func assertPracticalFinalSaveResult(t *testing.T, exec commandExecution) autosaveResultJSON {
	t.Helper()

	if !exec.envelope.OK || exec.err != nil {
		t.Fatalf("final-save = (%+v, %v), want practical success", exec.envelope, exec.err)
	}

	result := decodeJSONPayload[autosaveResultJSON](t, exec.envelope.Result)
	if result.Status != autosaveStatusSaved || result.Reason != automation.ReasonDurableGeneration || result.Snapshot != "best_effort" {
		t.Fatalf("result = %#v, want saved durable best_effort generation", result)
	}

	if result.Generation == "" || !result.Recorded {
		t.Fatalf("result = %#v, want a recorded durable generation", result)
	}

	if result.HistoryState != "recorded" {
		t.Fatalf("historyState = %q, want recorded after a durable final save", result.HistoryState)
	}

	return result
}

func assertPracticalFinalSaveStatus(t *testing.T, statusPath, generation string) {
	t.Helper()

	status, err := automation.ReadFinalSaveStatus(statusPath)
	if err != nil {
		t.Fatalf("ReadFinalSaveStatus failed: %v", err)
	}

	if status.Result != automation.FinalSaveSaved || status.Incomplete || !status.Recorded || !status.Bookkeeping || status.SnapshotStatus != "best_effort" {
		t.Fatalf("status artifact = %#v, want a recorded practical recovery save", status)
	}

	if status.Generation != generation {
		t.Fatalf("status generation = %q, want %q", status.Generation, generation)
	}
}

func assertAutosaveNotDueAfterFinalSave(t *testing.T, socketPath string) {
	t.Helper()

	due := executeJSON(t, "autosave", "save-if-due", testSocketFlag, socketPath, testNoConfigFlag, testJSONFlag)
	if !due.envelope.OK {
		t.Fatalf("save-if-due failed: %s", due.stderr)
	}

	skipped := decodeJSONPayload[autosaveResultJSON](t, due.envelope.Result)
	if skipped.Status != "not_due" {
		t.Fatalf("status = %q, want not_due after the final save recorded completion", skipped.Status)
	}
}

func TestAutosaveSkipsAServerWithoutSessions(t *testing.T) {
	server := tmuxtest.NewServer(t)

	if err := server.Options().SetExitEmpty(t.Context(), false); err != nil {
		t.Fatal(err)
	}

	sessions, err := server.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, info := range sessions {
		sess, err := server.Session(t.Context(), info.ID)
		if err != nil {
			t.Fatal(err)
		}

		if err := sess.Kill(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	socketPath := server.Endpoint().SocketPath

	isolateAutosaveEnv(t)

	statusPath := filepath.Join(dataHome(t), "final-save-status.json")

	runs := [][]string{
		{"autosave", "final-save", "--deadline", "5s", "--status-file", statusPath},
		{"autosave", "save-if-due"},
	}
	for _, args := range runs {
		exec := executeJSON(t, append(args, testSocketFlag, socketPath, testNoConfigFlag, testJSONFlag)...)
		if !exec.envelope.OK || exec.err != nil {
			t.Fatalf("%v = (%+v, %v), want a successful skip", args, exec.envelope, exec.err)
		}

		result := decodeJSONPayload[autosaveResultJSON](t, exec.envelope.Result)
		if result.Status != "skipped" || result.Reason != "no_sessions" || result.Recorded {
			t.Fatalf("%v result = %#v, want an unrecorded no_sessions skip", args, result)
		}
	}

	status, err := automation.ReadFinalSaveStatus(statusPath)
	if err != nil {
		t.Fatalf("ReadFinalSaveStatus failed: %v", err)
	}

	if status.Reason != "no_sessions" || status.Attempt != "" {
		t.Fatalf("status artifact = %#v, want a no_sessions result without a save attempt", status)
	}

	history := executeJSON(t, "snapshot", "history", testNoConfigFlag, testJSONFlag)
	if !history.envelope.OK {
		t.Fatalf("snapshot history failed: %+v", history.envelope)
	}

	attempts := decodeJSONPayload[struct {
		Attempts []json.RawMessage `json:"attempts"`
	}](t, history.envelope.Result)
	if len(attempts.Attempts) != 0 {
		t.Fatalf("history attempts = %s, want none for a server without sessions", attempts.Attempts)
	}
}

func TestAutosaveFinalSaveReportsAnUnavailableServer(t *testing.T) {
	server := tmuxtest.NewServer(t)
	socketPath := server.Endpoint().SocketPath

	if err := server.Kill(t.Context()); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	isolateAutosaveEnv(t)

	statusPath := filepath.Join(dataHome(t), "final-save-status.json")

	exec := executeJSON(
		t, "autosave", "final-save",
		testSocketFlag, socketPath,
		testNoConfigFlag, testJSONFlag,
		"--deadline", "5s",
		"--status-file", statusPath,
	)
	if exec.err == nil {
		t.Fatal("final-save against a stopped server must report an error")
	}

	result := decodeJSONPayload[autosaveResultJSON](t, exec.envelope.Result)
	if result.Status != "failed" || result.Recorded {
		t.Fatalf("result = %#v, want a failed unrecorded outcome", result)
	}

	status, err := automation.ReadFinalSaveStatus(statusPath)
	if err != nil {
		t.Fatalf("ReadFinalSaveStatus failed: %v", err)
	}

	if !status.Incomplete {
		t.Fatalf("status artifact = %#v, want an incomplete result", status)
	}
}
