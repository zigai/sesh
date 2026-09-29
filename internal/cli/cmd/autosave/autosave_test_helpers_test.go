package cmdautosave_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zigai/sesh/internal/testsupport/clitest"
)

const (
	testSocketFlag   = "--socket"
	testJSONFlag     = "--json"
	testConfigFlag   = "--config"
	testNoConfigFlag = "--no-config"
	actionCmd        = "action"
)

// autosaveResultJSON is the autosave wire contract decoded from --json output.
type autosaveResultJSON struct {
	Status           string `json:"status"`
	Reason           string `json:"reason"`
	Attempt          string `json:"attempt,omitempty"`
	Generation       string `json:"generation,omitempty"`
	Snapshot         string `json:"snapshotStatus,omitempty"`
	HistoryState     string `json:"historyState,omitempty"`
	RepositoryState  string `json:"repositoryState,omitempty"`
	FinalSaveState   string `json:"finalSaveState,omitempty"`
	DefaultAction    string `json:"defaultSessionAction,omitempty"`
	DefaultFrom      string `json:"defaultSessionFrom,omitempty"`
	DefaultTo        string `json:"defaultSessionTo,omitempty"`
	Recorded         bool   `json:"recorded"`
	ElapsedSec       int64  `json:"elapsedSeconds"`
	IntervalM        int    `json:"intervalMinutes"`
	DaemonState      string `json:"daemonState"`
	DaemonPhase      string `json:"daemonPhase,omitempty"`
	InhibitorsHeld   bool   `json:"inhibitorsHeld"`
	LogindDelayMS    int64  `json:"logindDelayMillis,omitempty"`
	AttemptTimeoutMS int64  `json:"attemptTimeoutMillis,omitempty"`
	DrainGraceMS     int64  `json:"drainGraceMillis,omitempty"`
	ServerManagement string `json:"serverManagement,omitempty"`
}

type commandExecution = clitest.Execution

func executeJSON(t *testing.T, args ...string) commandExecution {
	t.Helper()

	return clitest.ExecuteJSON(t, args...)
}

func executeCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	exec := clitest.Execute(t, args...)

	return exec.Stdout, exec.Stderr, exec.Err
}

func isolatedAutosaveHome(t *testing.T) {
	t.Helper()

	t.Setenv("TMUX", "")

	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
}

func writeCorruptHistoryState(t *testing.T) {
	t.Helper()

	path := filepath.Join(filepath.Clean(os.Getenv("XDG_STATE_HOME")), "sesh", "history.json")

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
}
