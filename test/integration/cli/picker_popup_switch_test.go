//go:build linux && integration

package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"
	"golang.org/x/sys/unix"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/session"
)

func TestTmuxPopupSelectionSwitchesOpeningClientAfterClose(t *testing.T) {
	fixture := newCLISessionFixture(t)
	artifact := newArtifactBinaryFixture(t)
	origin := fixture.fixtureSession(t)
	target := fixture.newLiveSession(t, "mobile-target")

	master, slave := openPickerPopupPTY(t)
	clientName := attachPickerPopupClient(t, fixture, origin, slave)
	panes, err := origin.Handle().Panes(t.Context())
	if err != nil || len(panes) == 0 {
		t.Fatalf("origin panes: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, artifact.binary,
		"popup", "--tmux", "--no-config", "--socket", fixture.socket,
		"--client", clientName, "--target-pane", string(panes[0].ID),
		"--current-session-id", string(origin.ID), "--history", fixture.historyPath,
		"--repository", fixture.repository, "--lease-root", fixture.leaseRoot,
	)
	command.Dir = artifact.workDir
	command.Env = artifact.env

	var commandOutput bytes.Buffer

	command.Stdout = &commandOutput
	command.Stderr = &commandOutput
	commandDone := runPickerPopupArtifact(command)
	waitForPickerPopupText(t, ctx, master, commandDone, "mobile-target")

	if _, err := master.WriteString("mobile-target\r"); err != nil {
		t.Fatal(err)
	}

	waitForPickerPopupExit(t, ctx, commandDone, commandOutput.String)

	if displayed := fixture.clientDisplayedSession(t, clientName); displayed != target.ID {
		t.Fatalf("opening client displays %q, want %q", displayed, target.ID)
	}
}

func TestAgentsPopupInitiallySelectsInvokingAgentPane(t *testing.T) {
	fixture := newCLISessionFixture(t)
	artifact := newArtifactBinaryFixture(t)
	origin, secondInfo, registryPath, handoffPath := prepareAgentPickerPopup(t, fixture)
	master, slave := openPickerPopupPTY(t)

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, artifact.binary,
		"popup", "--no-config", "--socket", fixture.socket,
		"--target-pane", string(secondInfo.ID), "--current-session-id", string(origin.ID),
		"--agent-state", registryPath, "--switch-handoff", handoffPath,
		"--history", fixture.historyPath, "--repository", fixture.repository, "--lease-root", fixture.leaseRoot,
	)
	command.Dir = artifact.workDir
	command.Env = artifact.env
	command.Stdin = slave
	command.Stdout = slave
	command.Stderr = slave
	commandDone := runPickerPopupArtifact(command)
	waitForPickerPopupText(t, ctx, master, commandDone, "alpha-agent", "zeta-agent")

	if _, err := master.WriteString("\r"); err != nil {
		t.Fatal(err)
	}

	waitForPickerPopupExit(t, ctx, commandDone, func() string { return "" })

	encoded, err := os.ReadFile(handoffPath)
	if err != nil {
		t.Fatal(err)
	}

	var handoff struct {
		SessionID string `json:"sessionId"`
		PaneID    string `json:"paneId"`
	}
	if err := json.Unmarshal(encoded, &handoff); err != nil {
		t.Fatalf("decode handoff: %v", err)
	}

	if handoff.SessionID != string(origin.ID) || handoff.PaneID != string(secondInfo.ID) {
		t.Fatalf("initial agent selection = %+v, want pane %s", handoff, secondInfo.ID)
	}
}

func TestTmuxPopupSelectsInvokingAgentWithLinkedWindow(t *testing.T) {
	fixture := newCLISessionFixture(t)
	artifact := newArtifactBinaryFixture(t)
	origin, secondInfo, registryPath, _ := prepareAgentPickerPopup(t, fixture)
	linked := fixture.newLiveSession(t, "linked-session")
	window, err := secondInfo.Handle().Window(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	index := 10
	if _, err := window.Link(t.Context(), linked.Handle(), gotmux.LinkOptions{Index: &index}); err != nil {
		t.Fatal(err)
	}

	activeBefore, err := window.ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if activeBefore.ID() == secondInfo.ID {
		t.Fatal("fixture already focuses invoking agent pane")
	}

	master, slave := openPickerPopupPTY(t)
	clientName := attachPickerPopupClient(t, fixture, origin, slave)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, artifact.binary,
		"action", "picker-open", "--no-config", "--socket", fixture.socket,
		"--client", clientName, "--target-pane", string(secondInfo.ID),
		"--agent-state", registryPath, "--history", fixture.historyPath,
		"--repository", fixture.repository, "--lease-root", fixture.leaseRoot,
	)
	command.Dir = artifact.workDir
	command.Env = artifact.env

	var commandOutput bytes.Buffer

	command.Stdout = &commandOutput
	command.Stderr = &commandOutput
	commandDone := runPickerPopupArtifact(command)
	waitForPickerPopupText(t, ctx, master, commandDone, "linked-session", "Omp")

	if _, err := master.WriteString("\r"); err != nil {
		t.Fatal(err)
	}

	waitForPickerPopupExit(t, ctx, commandDone, commandOutput.String)

	active, err := window.ActivePane(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if active.ID() != secondInfo.ID {
		t.Fatalf("selected pane = %s, want invoking agent pane %s", active.ID(), secondInfo.ID)
	}
}

func prepareAgentPickerPopup(t *testing.T, fixture *cliSessionFixture) (gotmux.SessionInfo, gotmux.PaneInfo, string, string) {
	t.Helper()
	origin := fixture.fixtureSession(t)
	panes, err := origin.Handle().Panes(t.Context())
	if err != nil || len(panes) == 0 {
		t.Fatalf("origin panes: %v", err)
	}

	first := panes[0].Handle()
	second, err := first.Split(t.Context(), gotmux.SplitOptions{Direction: gotmux.Vertical})
	if err != nil {
		t.Fatal(err)
	}

	secondInfo, err := second.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if err := first.Select(t.Context()); err != nil {
		t.Fatal(err)
	}

	state := &session.State{Version: session.CurrentHistoryVersion, PopupDisplayMode: "agents"}
	if err := session.SaveState(t.Context(), fixture.historyPath, state); err != nil {
		t.Fatal(err)
	}

	registryPath := writePickerPopupAgentRegistry(t, fixture.socket, origin, []gotmux.PaneInfo{panes[0], secondInfo})
	if observed, err := agents.LoadLiveAgents(t.Context(), agents.NewAHTStore(registryPath)); err != nil || len(observed) != 2 {
		t.Fatalf("agent registry fixture = %d records, error %v", len(observed), err)
	}

	handoffPath := filepath.Join(t.TempDir(), "handoff.json")
	if err := os.WriteFile(handoffPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	return origin, secondInfo, registryPath, handoffPath
}

func writePickerPopupAgentRegistry(t *testing.T, socket string, origin gotmux.SessionInfo, panes []gotmux.PaneInfo) string {
	t.Helper()

	now := time.Now().UTC().Format(time.RFC3339Nano)

	records := make(map[string]any, len(panes))
	for index, pane := range panes {
		id := "alpha-agent"
		if index == 1 {
			id = "zeta-agent"
		}

		records[id] = map[string]any{
			"schema_version": 2, "id": id, "harness": "omp", "presence": "live", "activity": "running",
			"session_id": id, "created_at": now, "updated_at": now,
			"presence_changed_at": now, "activity_changed_at": now,
			"tmux": map[string]any{
				"inside": true, "server_socket": socket, "session_id": string(origin.ID),
				"session_name": origin.Name, "window_id": string(pane.WindowID),
				"pane_id": string(pane.ID), "pane_pid": pane.PID,
			},
		}
	}

	encoded, err := json.Marshal(map[string]any{"schema_version": 2, "revision": 1, "sessions": records})
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "agents.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func openPickerPopupPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, slave := openTestPTY(t)
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 30, Col: 100}); err != nil {
		t.Fatal(err)
	}

	return master, slave
}

func attachPickerPopupClient(t *testing.T, fixture *cliSessionFixture, origin gotmux.SessionInfo, slave *os.File) string {
	t.Helper()
	attachCtx, cancelAttach := context.WithCancel(context.WithoutCancel(t.Context()))

	attachDone := make(chan error, 1)
	go func() {
		attachDone <- origin.Handle().Attach(attachCtx, gotmux.TerminalStreams{In: slave, Out: slave, Err: slave}, gotmux.AttachOptions{})
	}()

	t.Cleanup(func() {
		cancelAttach()

		select {
		case <-attachDone:
		case <-time.After(5 * time.Second):
		}
	})

	return fixture.waitForNewCLIClient(t, origin.ID, nil)
}

func runPickerPopupArtifact(command *exec.Cmd) <-chan error {
	done := make(chan error, 1)
	go func() { done <- command.Run() }()

	return done
}

func waitForPickerPopupText(t *testing.T, ctx context.Context, master *os.File, commandDone <-chan error, needles ...string) {
	t.Helper()

	rendered := make(chan struct{})

	var outputMu sync.Mutex

	var captured []byte

	go func() {
		buffer := make([]byte, 4096)
		for {
			n, readErr := master.Read(buffer)
			if n > 0 {
				outputMu.Lock()

				captured = append(captured, buffer[:n]...)

				ready := true
				for _, needle := range needles {
					ready = ready && bytes.Contains(captured, []byte(needle))
				}
				outputMu.Unlock()

				if ready {
					close(rendered)
					return
				}
			}

			if readErr != nil {
				return
			}
		}
	}()

	select {
	case <-rendered:
		return
	case err := <-commandDone:
		outputMu.Lock()
		screen := string(captured)
		outputMu.Unlock()
		t.Fatalf("popup exited before rendering %v: %v: %q", needles, err, screen)
	case <-ctx.Done():
		outputMu.Lock()
		screen := string(captured)
		outputMu.Unlock()
		t.Fatalf("popup did not render %v: %q", needles, screen)
	}
}

func waitForPickerPopupExit(t *testing.T, ctx context.Context, commandDone <-chan error, diagnostic func() string) {
	t.Helper()

	select {
	case err := <-commandDone:
		if err != nil {
			t.Fatalf("popup failed: %v: %s", err, diagnostic())
		}
	case <-ctx.Done():
		t.Fatal("popup did not finish after selection")
	}
}
