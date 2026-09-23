//go:build (linux || darwin) && integration

package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/cli"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

type sessionJSON struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	NameRaw     model.Bytes `json:"nameRaw,omitempty"`
	WindowCount int         `json:"windowCount"`
	Attached    bool        `json:"attached"`
	ActivityAt  *string     `json:"activityAt"`
	Current     bool        `json:"current"`
}

type sessionListJSONResult struct {
	Sessions []sessionJSON `json:"sessions"`
}

type sessionHistorySummaryJSON struct {
	Name      string      `json:"name"`
	NameRaw   model.Bytes `json:"nameRaw,omitempty"`
	PaneCount int         `json:"paneCount"`
	LineCount int         `json:"lineCount"`
}

type sessionHistoryJSONResult struct {
	Mode     string                      `json:"mode"`
	Sessions []sessionHistorySummaryJSON `json:"sessions"`
}

type paneHistoryJSON struct {
	SessionName string      `json:"sessionName"`
	NameRaw     model.Bytes `json:"nameRaw,omitempty"`
	WindowIndex string      `json:"windowIndex"`
	PaneIndex   string      `json:"paneIndex"`
	LineCount   int         `json:"lineCount"`
	HistorySize int         `json:"historySize"`
}

type paneHistoryJSONResult struct {
	Mode  string            `json:"mode"`
	Panes []paneHistoryJSON `json:"panes"`
}

type sessionNewJSONResult struct {
	Session   string `json:"session"`
	Directory string `json:"directory"`
	Created   bool   `json:"created"`
}

type sessionKillJSONResult struct {
	Session string `json:"session"`
	Killed  bool   `json:"killed"`
}

type sessionDeleteJSONResult struct {
	Session      string `json:"session"`
	LiveKilled   bool   `json:"liveKilled"`
	SavedDeleted bool   `json:"savedDeleted"`
}

type sessionSwitchJSONResult struct {
	Session  string `json:"session"`
	Switched bool   `json:"switched"`
}

func TestCLIIntegrationListLiveSessions(t *testing.T) {
	server := newCaptureCLIServer(t)
	socket := server.Endpoint().SocketPath

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	sess1, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "live-first"})
	if err != nil {
		t.Fatalf("creating session live-first: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	_, err = server.NewSession(ctx, gotmux.NewSessionOptions{Name: "live-second"})
	if err != nil {
		t.Fatalf("creating session live-second: %v", err)
	}

	t.Run("human table output", func(t *testing.T) {
		verifyListHumanTable(t, socket, false)
	})

	t.Run("human full table output", func(t *testing.T) {
		verifyListHumanTable(t, socket, true)
	})

	t.Run("json output", func(t *testing.T) {
		verifyListJSONOutput(t, socket, sess1.ID())
	})
}

func verifyListHumanTable(t *testing.T, socket string, full bool) {
	t.Helper()

	root := newRootCommand(t.Context())
	args := []string{testListCmd, testSocketFlag, socket, testNoConfigFlag}

	if full {
		args = append(args, "--full")
	}

	stdout, stderr, err := executeCommand(root, args...)
	if err != nil {
		t.Fatalf("list command error: %v", err)
	}

	if stderr != "" {
		t.Fatalf("list command stderr: %q", stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected header + 2 session rows, got:\n%s", stdout)
	}

	if !strings.Contains(lines[0], "Name") || !strings.Contains(lines[0], "Windows") || !strings.Contains(lines[0], "Attached") || !strings.Contains(lines[0], "Activity") {
		t.Fatalf("missing table headers in: %s", lines[0])
	}

	if !strings.Contains(stdout, "live-first") || !strings.Contains(stdout, "live-second") {
		t.Fatalf("output missing session names:\n%s", stdout)
	}
}

func verifyListJSONOutput(t *testing.T, socket string, expectedID gotmux.SessionID) {
	t.Helper()

	exec := executeJSON(t, testListCmd, testSocketFlag, socket, testNoConfigFlag, testJSONFlag)
	if exec.err != nil {
		t.Fatalf("list --json error: %v", exec.err)
	}

	if !exec.envelope.OK || exec.envelope.Command != testListCmd {
		t.Fatalf("unexpected envelope: %+v", exec.envelope)
	}

	res := decodeJSONPayload[sessionListJSONResult](t, exec.envelope.Result)
	if len(res.Sessions) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(res.Sessions))
	}

	var foundFirst, foundSecond bool

	for _, s := range res.Sessions {
		if s.Name == "live-first" {
			foundFirst = true

			verifyFirstSessionProperties(t, s, expectedID)
		}

		if s.Name == "live-second" {
			foundSecond = true
		}
	}

	if !foundFirst || !foundSecond {
		t.Fatalf("missing sessions in JSON: %+v", res.Sessions)
	}

	assertAllKeysCamelCase(t, res, "list.result")
}

func verifyFirstSessionProperties(t *testing.T, s sessionJSON, expectedID gotmux.SessionID) {
	t.Helper()

	if s.ID != string(expectedID) {
		t.Fatalf("session ID = %q, want %q", s.ID, expectedID)
	}

	if s.WindowCount < 1 {
		t.Fatalf("windowCount = %d, want >= 1", s.WindowCount)
	}

	if s.ActivityAt == nil {
		t.Fatal("expected non-nil activityAt")
	}
}

func TestCLIIntegrationListCurrentPaneResolution(t *testing.T) {
	server := newCaptureCLIServer(t)
	socket := server.Endpoint().SocketPath

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	sessTarget, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "target-sess"})
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}

	_, err = server.NewSession(ctx, gotmux.NewSessionOptions{Name: "other-sess"})
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}

	panes, err := sessTarget.Panes(ctx)
	if err != nil || len(panes) == 0 {
		t.Fatalf("getting panes: %v", err)
	}

	paneID := string(panes[0].ID)

	exec := executeJSON(t, testListCmd, testSocketFlag, socket, "--current-pane", paneID, testNoConfigFlag, testJSONFlag)
	if exec.err != nil {
		t.Fatalf("list --current-pane error: %v", exec.err)
	}

	res := decodeJSONPayload[sessionListJSONResult](t, exec.envelope.Result)

	var foundCurrent bool

	for _, s := range res.Sessions {
		if s.Name == "target-sess" {
			if !s.Current {
				t.Fatalf("expected target-sess to have current=true, got false")
			}

			foundCurrent = true
		} else if s.Current {
			t.Fatalf("expected other session to have current=false, got true")
		}
	}

	if !foundCurrent {
		t.Fatal("target-sess not found in result")
	}
}

func TestCLIIntegrationListAmbiguousCurrentPane(t *testing.T) {
	server := newCaptureCLIServer(t)
	socket := server.Endpoint().SocketPath

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	sharedPaneID := setupSharedWindowFixture(t, ctx, server)

	exec := executeJSON(t, testListCmd, testSocketFlag, socket, "--current-pane", sharedPaneID, testNoConfigFlag, testJSONFlag)
	if exec.envelope.OK {
		t.Fatal("expected OK=false for ambiguous current pane")
	}

	if exec.envelope.Error == nil || exec.envelope.Error.Code != "currentSessionAmbiguous" {
		t.Fatalf("error code = %+v, want currentSessionAmbiguous", exec.envelope.Error)
	}

	res := decodeJSONPayload[sessionListJSONResult](t, exec.envelope.Result)
	if len(res.Sessions) < 2 {
		t.Fatalf("expected sessions retained in partial result, got %d", len(res.Sessions))
	}

	for _, s := range res.Sessions {
		if s.Current {
			t.Fatalf("expected session %s current=false under ambiguous link, got true", s.Name)
		}
	}
}

func setupSharedWindowFixture(t *testing.T, ctx context.Context, server *gotmux.Server) string {
	t.Helper()

	sessA, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "shared-a"})
	if err != nil {
		t.Fatalf("creating session A: %v", err)
	}

	sessB, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "shared-b"})
	if err != nil {
		t.Fatalf("creating session B: %v", err)
	}

	winLinksA, err := sessA.Windows(ctx)
	if err != nil || len(winLinksA) == 0 {
		t.Fatalf("getting windows in sessA: %v", err)
	}

	sharedWin := winLinksA[0].Window()
	idx := 10

	if _, err := sharedWin.Link(ctx, sessB, gotmux.LinkOptions{Index: &idx}); err != nil {
		t.Fatalf("linking window: %v", err)
	}

	panes, err := sessA.Panes(ctx)
	if err != nil || len(panes) == 0 {
		t.Fatalf("getting panes: %v", err)
	}

	return string(panes[0].ID)
}

func TestCLIIntegrationHistorySessionTotals(t *testing.T) {
	server := newCaptureCLIServer(t)
	socket := server.Endpoint().SocketPath

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	setupHistoryTotalsFixture(t, ctx, server)

	t.Run("table output", func(t *testing.T) {
		verifyHistorySessionTotalsTable(t, socket)
	})

	t.Run("json output", func(t *testing.T) {
		verifyHistorySessionTotalsJSON(t, socket)
	})
}

func setupHistoryTotalsFixture(t *testing.T, ctx context.Context, server *gotmux.Server) {
	t.Helper()

	sessAlpha, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "hist-alpha"})
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "hist-beta"}); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	panesA, err := sessAlpha.Panes(ctx)
	if err != nil || len(panesA) == 0 {
		t.Fatalf("getting panes: %v", err)
	}

	if _, err := panesA[0].Handle().Split(ctx, gotmux.SplitOptions{Direction: gotmux.Vertical}); err != nil {
		t.Fatalf("splitting pane: %v", err)
	}
}

func verifyHistorySessionTotalsTable(t *testing.T, socket string) {
	t.Helper()

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, testHistoryCmd, testSocketFlag, socket, testNoConfigFlag)
	if err != nil {
		t.Fatalf("history command error: %v", err)
	}

	if stderr != "" {
		t.Fatalf("history command stderr: %q", stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected header + 2 rows, got:\n%s", stdout)
	}

	if !strings.Contains(lines[0], "Session") || !strings.Contains(lines[0], "Panes") || !strings.Contains(lines[0], "Lines") {
		t.Fatalf("unexpected header: %s", lines[0])
	}

	if !strings.Contains(stdout, "hist-alpha") || !strings.Contains(stdout, "hist-beta") {
		t.Fatalf("missing sessions in table:\n%s", stdout)
	}
}

func verifyHistorySessionTotalsJSON(t *testing.T, socket string) {
	t.Helper()

	exec := executeJSON(t, testHistoryCmd, testSocketFlag, socket, testNoConfigFlag, testJSONFlag)
	if exec.err != nil {
		t.Fatalf("history --json error: %v", exec.err)
	}

	if !exec.envelope.OK || exec.envelope.Command != testHistoryCmd {
		t.Fatalf("unexpected envelope: %+v", exec.envelope)
	}

	res := decodeJSONPayload[sessionHistoryJSONResult](t, exec.envelope.Result)
	if res.Mode != "sessions" {
		t.Fatalf("mode = %q, want sessions", res.Mode)
	}

	var foundAlpha, foundBeta bool

	for _, s := range res.Sessions {
		if s.Name == "hist-alpha" {
			foundAlpha = true

			if s.PaneCount != 2 {
				t.Fatalf("alpha paneCount = %d, want 2", s.PaneCount)
			}
		}

		if s.Name == "hist-beta" {
			foundBeta = true

			if s.PaneCount != 1 {
				t.Fatalf("beta paneCount = %d, want 1", s.PaneCount)
			}
		}
	}

	if !foundAlpha || !foundBeta {
		t.Fatalf("missing session totals in JSON: %+v", res.Sessions)
	}

	assertAllKeysCamelCase(t, res, "history.result")
}

func TestCLIIntegrationHistoryPanesDetail(t *testing.T) {
	server := newCaptureCLIServer(t)
	socket := server.Endpoint().SocketPath

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	sess, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "detail-sess"})
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}

	panes, err := sess.Panes(ctx)
	if err != nil || len(panes) == 0 {
		t.Fatalf("getting panes: %v", err)
	}

	if _, err := panes[0].Handle().Split(ctx, gotmux.SplitOptions{Direction: gotmux.Vertical}); err != nil {
		t.Fatalf("splitting pane: %v", err)
	}

	t.Run("table output", func(t *testing.T) {
		verifyHistoryPanesTable(t, socket)
	})

	t.Run("json output", func(t *testing.T) {
		verifyHistoryPanesJSON(t, socket)
	})
}

func verifyHistoryPanesTable(t *testing.T, socket string) {
	t.Helper()

	root := newRootCommand(t.Context())

	stdout, stderr, err := executeCommand(root, testHistoryCmd, "--panes", testSocketFlag, socket, testNoConfigFlag)
	if err != nil {
		t.Fatalf("history --panes error: %v", err)
	}

	if stderr != "" {
		t.Fatalf("history --panes stderr: %q", stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected header + 2 pane rows, got:\n%s", stdout)
	}

	if !strings.Contains(lines[0], "Session") || !strings.Contains(lines[0], "Pane") || !strings.Contains(lines[0], "Lines") {
		t.Fatalf("unexpected header: %s", lines[0])
	}

	if !strings.Contains(stdout, "detail-sess") {
		t.Fatalf("missing session name in table:\n%s", stdout)
	}
}

func verifyHistoryPanesJSON(t *testing.T, socket string) {
	t.Helper()

	exec := executeJSON(t, testHistoryCmd, "--panes", testSocketFlag, socket, testNoConfigFlag, testJSONFlag)
	if exec.err != nil {
		t.Fatalf("history --panes --json error: %v", exec.err)
	}

	if !exec.envelope.OK || exec.envelope.Command != testHistoryCmd {
		t.Fatalf("unexpected envelope: %+v", exec.envelope)
	}

	res := decodeJSONPayload[paneHistoryJSONResult](t, exec.envelope.Result)
	if res.Mode != "panes" {
		t.Fatalf("mode = %q, want panes", res.Mode)
	}

	if len(res.Panes) < 2 {
		t.Fatalf("expected at least 2 panes, got %d", len(res.Panes))
	}

	var foundDetailPanes int

	for _, p := range res.Panes {
		if p.SessionName == "detail-sess" {
			foundDetailPanes++

			if p.WindowIndex == "" || p.PaneIndex == "" {
				t.Fatalf("invalid pane indexes: window %q, pane %q", p.WindowIndex, p.PaneIndex)
			}
		}
	}

	if foundDetailPanes < 2 {
		t.Fatalf("expected at least 2 detail-sess panes, got %d (all panes: %+v)", foundDetailPanes, res.Panes)
	}

	assertAllKeysCamelCase(t, res, "history_panes.result")
}

func TestCLIIntegrationHistorySharedWindows(t *testing.T) {
	server := newCaptureCLIServer(t)
	socket := server.Endpoint().SocketPath

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	setupSharedWindowHistoryFixture(t, ctx, server)

	exec := executeJSON(t, testHistoryCmd, "--panes", testSocketFlag, socket, testNoConfigFlag, testJSONFlag)
	if exec.err != nil {
		t.Fatalf("history --panes error: %v", exec.err)
	}

	res := decodeJSONPayload[paneHistoryJSONResult](t, exec.envelope.Result)

	var foundInA, foundInB bool

	for _, p := range res.Panes {
		if p.SessionName == "shared-a" {
			foundInA = true
		}

		if p.SessionName == "shared-b" && p.WindowIndex == "10" {
			foundInB = true
		}
	}

	if !foundInA {
		t.Fatalf("expected pane reported for shared-a, got %+v", res.Panes)
	}

	if !foundInB {
		t.Fatalf("expected pane reported for shared-b at window index 10, got %+v", res.Panes)
	}
}

func setupSharedWindowHistoryFixture(t *testing.T, ctx context.Context, server *gotmux.Server) {
	t.Helper()

	sessA, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "shared-a"})
	if err != nil {
		t.Fatalf("creating session A: %v", err)
	}

	sessB, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "shared-b"})
	if err != nil {
		t.Fatalf("creating session B: %v", err)
	}

	winLinksA, err := sessA.Windows(ctx)
	if err != nil || len(winLinksA) == 0 {
		t.Fatalf("getting windows in sessA: %v", err)
	}

	sharedWin := winLinksA[0].Window()
	idx10 := 10

	if _, err := sharedWin.Link(ctx, sessB, gotmux.LinkOptions{Index: &idx10}); err != nil {
		t.Fatalf("linking window to sessB: %v", err)
	}
}

func TestCLIIntegrationHistoryNumericSortIndex2vs10(t *testing.T) {
	server := newCaptureCLIServer(t)
	socket := server.Endpoint().SocketPath

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	setupSparseWindowFixture(t, ctx, server)

	exec := executeJSON(t, testHistoryCmd, "--panes", testSocketFlag, socket, testNoConfigFlag, testJSONFlag)
	if exec.err != nil {
		t.Fatalf("history error: %v", exec.err)
	}

	res := decodeJSONPayload[paneHistoryJSONResult](t, exec.envelope.Result)

	assertSparsePanesSorted(t, res.Panes)
}

func setupSparseWindowFixture(t *testing.T, ctx context.Context, server *gotmux.Server) {
	t.Helper()

	sess, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "sparse-sort"})
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}

	idx10 := 10
	if _, err := sess.NewWindow(ctx, gotmux.NewWindowOptions{Index: &idx10}); err != nil {
		t.Fatalf("creating window 10: %v", err)
	}

	idx2 := 2
	if _, err := sess.NewWindow(ctx, gotmux.NewWindowOptions{Index: &idx2}); err != nil {
		t.Fatalf("creating window 2: %v", err)
	}
}

func assertSparsePanesSorted(t *testing.T, allPanes []paneHistoryJSON) {
	t.Helper()

	var sparsePanes []paneHistoryJSON

	for _, p := range allPanes {
		if p.SessionName == "sparse-sort" {
			sparsePanes = append(sparsePanes, p)
		}
	}

	seenWin2 := false
	seenWin10 := false

	for _, p := range sparsePanes {
		if p.WindowIndex == "2" {
			seenWin2 = true

			if seenWin10 {
				t.Fatalf("window 10 sorted before window 2: %+v", sparsePanes)
			}
		}

		if p.WindowIndex == "10" {
			seenWin10 = true

			if !seenWin2 {
				t.Fatalf("window 10 sorted before window 2 was seen: %+v", sparsePanes)
			}
		}
	}

	if !seenWin2 || !seenWin10 {
		t.Fatalf("expected windows 2 and 10 in sparse-sort, got %+v", sparsePanes)
	}
}

func TestCLIIntegrationKilledServerEmptyResult(t *testing.T) {
	server := newCaptureCLIServer(t)
	socket := server.Endpoint().SocketPath

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	if _, err := server.NewSession(ctx, gotmux.NewSessionOptions{Name: "temp"}); err != nil {
		t.Fatal(err)
	}

	if err := server.Kill(ctx); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	t.Run("list on killed server", func(t *testing.T) {
		exec := executeJSON(t, testListCmd, testSocketFlag, socket, testNoConfigFlag, testJSONFlag)
		if exec.err != nil {
			t.Fatalf("list on killed server returned error: %v", exec.err)
		}

		if !exec.envelope.OK {
			t.Fatalf("expected OK=true on absent server, got %+v", exec.envelope)
		}

		res := decodeJSONPayload[sessionListJSONResult](t, exec.envelope.Result)
		if len(res.Sessions) != 0 {
			t.Fatalf("expected 0 sessions on killed server, got %d", len(res.Sessions))
		}
	})

	t.Run("history on killed server", func(t *testing.T) {
		exec := executeJSON(t, testHistoryCmd, testSocketFlag, socket, testNoConfigFlag, testJSONFlag)
		if exec.err != nil {
			t.Fatalf("history on killed server returned error: %v", exec.err)
		}

		if !exec.envelope.OK {
			t.Fatalf("expected OK=true on absent server, got %+v", exec.envelope)
		}

		res := decodeJSONPayload[sessionHistoryJSONResult](t, exec.envelope.Result)
		if len(res.Sessions) != 0 {
			t.Fatalf("expected 0 sessions on killed server, got %d", len(res.Sessions))
		}
	})
}

func TestCLIIntegrationCanceledContextFailsClosed(t *testing.T) {
	server := newCaptureCLIServer(t)
	socket := server.Endpoint().SocketPath

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	root := newRootCommand(t.Context())
	root.SetArgs([]string{testListCmd, testSocketFlag, socket, testNoConfigFlag, testJSONFlag})
	root.SetContext(ctx)

	var stdout, stderr strings.Builder

	root.SetOut(&stdout)
	root.SetErr(&stderr)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}

	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout on fatal failure, got %q", stdout.String())
	}
}

type cliSessionFixture struct {
	server      *gotmux.Server
	socket      string
	historyPath string
	repository  string
	leaseRoot   string
	dataHome    string
}

func newCLISessionFixture(t *testing.T) *cliSessionFixture {
	t.Helper()

	server := newCaptureCLIServer(t)
	dir := t.TempDir()
	dataHome := filepath.Join(dir, "data")
	t.Setenv("XDG_DATA_HOME", dataHome)

	historyDir := filepath.Join(dir, "history")
	if err := os.Mkdir(historyDir, 0o700); err != nil {
		t.Fatal(err)
	}

	return &cliSessionFixture{
		server:      server,
		socket:      server.Endpoint().SocketPath,
		historyPath: filepath.Join(historyDir, "history.json"),
		repository:  filepath.Join(dir, "repository"),
		leaseRoot:   filepath.Join(dir, "leases"),
		dataHome:    dataHome,
	}
}

func (f *cliSessionFixture) sessionArgs() []string {
	return []string{testSocketFlag, f.socket, testNoConfigFlag, "--history", f.historyPath}
}

func (f *cliSessionFixture) deleteArgs() []string {
	return []string{testSocketFlag, f.socket, testNoConfigFlag, testLeaseRootFlag, f.leaseRoot, "--history", f.historyPath}
}

func (f *cliSessionFixture) assertNoDefaultLeaseRoot(t *testing.T) {
	t.Helper()

	leaseRoot := filepath.Join(f.dataHome, "sesh", "leases")
	if _, err := os.Stat(leaseRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default lease root was accessed by a non-delete command: %s: %v", leaseRoot, err)
	}
}

func (f *cliSessionFixture) setAmbientTmux(t *testing.T, session gotmux.SessionInfo) {
	t.Helper()

	probe, err := f.server.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	panes, err := session.Handle().Panes(t.Context())
	if err != nil || len(panes) == 0 {
		t.Fatalf("session panes: %v", err)
	}

	t.Setenv("TMUX", f.socket+","+strconv.Itoa(probe.Identity.PID)+","+strings.TrimPrefix(string(session.ID), "$"))
	t.Setenv("TMUX_PANE", string(panes[0].ID))
}

func clearAmbientTmux(t *testing.T) {
	t.Helper()

	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
}

func (f *cliSessionFixture) fixtureSession(t *testing.T) gotmux.SessionInfo {
	t.Helper()

	sessions, err := f.server.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, session := range sessions {
		if session.Name == "fixture" {
			return session
		}
	}

	t.Fatal("fixture session missing")

	return gotmux.SessionInfo{}
}

func (f *cliSessionFixture) newLiveSession(t *testing.T, name string) gotmux.SessionInfo {
	t.Helper()

	session, err := f.server.NewSession(t.Context(), gotmux.NewSessionOptions{
		Name:    name,
		Program: gotmux.Exec("/bin/sh"),
		Start:   gotmux.ExistingOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	info, err := session.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	return info
}

func (f *cliSessionFixture) saveLiveSession(t *testing.T, id gotmux.SessionID) {
	t.Helper()

	exec := executeJSON(
		t,
		actionCmd, "snapshot-save",
		testRepositoryFlag, f.repository,
		testSocketFlag, f.socket,
		testLeaseRootFlag, f.leaseRoot,
		testSessionFlag, string(id),
		testNoConfigFlag,
		testJSONFlag,
	)
	if exec.err != nil && !errors.Is(exec.err, cli.ErrPartialSnapshot) {
		t.Fatalf("snapshot save failed: %v", exec.err)
	}

	result := decodeJSONPayload[snapshotSaveResultJSON](t, exec.envelope.Result)
	if result.Generation == "" {
		t.Fatalf("snapshot save published no generation: %+v", exec.envelope)
	}
}

func (f *cliSessionFixture) savedGenerationNames(t *testing.T) []string {
	t.Helper()

	repo, err := store.OpenExisting(t.Context(), f.repository)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()

	catalog, err := repo.Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(catalog.Generations))

	for _, generation := range catalog.Generations {
		manifest, err := repo.Read(t.Context(), generation.ID)
		if err != nil {
			t.Fatal(err)
		}

		for _, stored := range manifest.Sessions {
			names = append(names, string(stored.Name))
		}
	}

	return names
}

func (f *cliSessionFixture) beginPendingSave(t *testing.T) {
	t.Helper()

	repo, err := store.Open(t.Context(), f.repository)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := repo.Close(); err != nil {
			t.Error(err)
		}
	}()

	if _, err := repo.Begin(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (f *cliSessionFixture) attachCLIClient(tb testing.TB, id gotmux.SessionID) string {
	tb.Helper()

	before, err := f.server.Clients(tb.Context())
	if err != nil {
		tb.Fatal(err)
	}

	existing := make(map[gotmux.ClientName]bool, len(before))
	for _, client := range before {
		existing[client.Name] = true
	}

	handle, err := f.server.Session(tb.Context(), id)
	if err != nil {
		tb.Fatal(err)
	}

	_, slave := openTestPTY(tb)

	attachCtx, cancelAttach := context.WithCancel(context.WithoutCancel(tb.Context()))

	done := make(chan error, 1)

	go func() {
		done <- handle.Attach(attachCtx, gotmux.TerminalStreams{In: slave, Out: slave, Err: slave}, gotmux.AttachOptions{
			ReadOnly:            false,
			PreserveEnvironment: true,
			Detach:              gotmux.DetachNone,
			Dir:                 "",
			Flags:               nil,
		})
	}()

	tb.Cleanup(func() {
		cancelAttach()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			tb.Logf("attach client for %s did not exit in time", id)
		}
	})

	return f.waitForNewCLIClient(tb, id, existing)
}

func (f *cliSessionFixture) waitForNewCLIClient(tb testing.TB, id gotmux.SessionID, existing map[gotmux.ClientName]bool) string {
	tb.Helper()

	deadline := time.Now().Add(15 * time.Second)

	for time.Now().Before(deadline) {
		clients, err := f.server.Clients(tb.Context())
		if err != nil {
			tb.Fatal(err)
		}

		for _, client := range clients {
			displayed, ok := client.SessionID.Get()
			if ok && displayed == id && !client.Control && !existing[client.Name] {
				return string(client.Name)
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	tb.Fatal("attached client never appeared")

	return ""
}

func (f *cliSessionFixture) clientDisplayedSession(t *testing.T, name string) gotmux.SessionID {
	t.Helper()

	clients, err := f.server.Clients(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	for _, client := range clients {
		if string(client.Name) != name {
			continue
		}

		displayed, ok := client.SessionID.Get()
		if !ok {
			t.Fatalf("client %s reported no session", name)
		}

		return displayed
	}

	t.Fatalf("client %s is not attached", name)

	return ""
}

func TestCLIIntegrationSessionSwitchWithoutClientFailsClosed(t *testing.T) {
	fixture := newCLISessionFixture(t)

	session := fixture.fixtureSession(t)
	fixture.setAmbientTmux(t, session)

	target := fixture.newLiveSession(t, "switch-target")

	exec := executeJSON(t, append([]string{actionCmd, "session-switch", "switch-target"}, append(fixture.sessionArgs(), testJSONFlag)...)...)
	if exec.err == nil {
		t.Fatal("switch reported success without an invoking client")
	}

	if exec.envelope.OK {
		t.Fatalf("envelope reported success: %+v", exec.envelope)
	}

	result := decodeJSONPayload[sessionSwitchJSONResult](t, exec.envelope.Result)
	if result.Switched {
		t.Fatal("partial result claimed a switch that never happened")
	}

	if _, err := fixture.server.FindSession(t.Context(), target.Name); err != nil {
		t.Fatalf("target session changed: %v", err)
	}
}

func TestCLIIntegrationSessionSwitchSwitchesInvokingClient(t *testing.T) {
	fixture := newCLISessionFixture(t)

	origin := fixture.fixtureSession(t)
	fixture.setAmbientTmux(t, origin)

	target := fixture.newLiveSession(t, "switch-target")

	client := fixture.attachCLIClient(t, origin.ID)

	exec := executeJSON(t, append([]string{actionCmd, "session-switch", "switch-target"}, append(fixture.sessionArgs(), testJSONFlag)...)...)
	if exec.err != nil {
		t.Fatalf("switch failed: %v", exec.err)
	}

	if !exec.envelope.OK {
		t.Fatalf("switch envelope: %+v", exec.envelope)
	}

	result := decodeJSONPayload[sessionSwitchJSONResult](t, exec.envelope.Result)
	if !result.Switched {
		t.Fatal("verified switch was not reported")
	}

	if displayed := fixture.clientDisplayedSession(t, client); displayed != target.ID {
		t.Fatalf("client displays %s, want %s", displayed, target.ID)
	}

	fixture.assertNoDefaultLeaseRoot(t)
}

func TestCLIIntegrationSessionSwitchUsesAmbientSocket(t *testing.T) {
	fixture := newCLISessionFixture(t)

	origin := fixture.fixtureSession(t)
	fixture.setAmbientTmux(t, origin)

	target := fixture.newLiveSession(t, "switch-target")

	client := fixture.attachCLIClient(t, origin.ID)

	exec := executeJSON(t, actionCmd, "session-switch", "switch-target", testNoConfigFlag, "--history", fixture.historyPath, testJSONFlag)
	if exec.err != nil || !exec.envelope.OK {
		t.Fatalf("ambient switch failed: %v / %+v", exec.err, exec.envelope)
	}

	result := decodeJSONPayload[sessionSwitchJSONResult](t, exec.envelope.Result)
	if !result.Switched {
		t.Fatal("ambient switch was not reported")
	}

	if displayed := fixture.clientDisplayedSession(t, client); displayed != target.ID {
		t.Fatalf("client displays %s, want %s", displayed, target.ID)
	}
}

func TestCLIIntegrationSessionNewCreatesWithoutAttachInJSONMode(t *testing.T) {
	fixture := newCLISessionFixture(t)

	origin := fixture.fixtureSession(t)
	fixture.setAmbientTmux(t, origin)

	client := fixture.attachCLIClient(t, origin.ID)

	created := executeJSON(t, append([]string{actionCmd, "session-new", "created-session", "--dir", "/tmp"}, append(fixture.sessionArgs(), testJSONFlag)...)...)
	if created.err != nil || !created.envelope.OK {
		t.Fatalf("session new failed: %v / %+v", created.err, created.envelope)
	}

	result := decodeJSONPayload[sessionNewJSONResult](t, created.envelope.Result)
	if !result.Created {
		t.Fatal("new session was not reported as created")
	}

	if _, err := fixture.server.FindSession(t.Context(), "created-session"); err != nil {
		t.Fatalf("created session missing on server: %v", err)
	}

	if displayed := fixture.clientDisplayedSession(t, client); displayed != origin.ID {
		t.Fatalf("client displays %s, want origin %s for create-only --json", displayed, origin.ID)
	}

	again := executeJSON(t, append([]string{actionCmd, "session-new", "created-session", "--dir", "/tmp"}, append(fixture.sessionArgs(), testJSONFlag)...)...)
	if again.err == nil || again.envelope.OK {
		t.Fatal("expected already-exists failure for duplicate create-only new")
	}

	if again.envelope.Error == nil || again.envelope.Error.Code != "sessionAlreadyExists" {
		t.Fatalf("error code = %v, want sessionAlreadyExists", again.envelope.Error)
	}

	fixture.assertNoDefaultLeaseRoot(t)
}

func TestCLIIntegrationSessionNewInteractiveEnters(t *testing.T) {
	fixture := newCLISessionFixture(t)

	origin := fixture.fixtureSession(t)
	fixture.setAmbientTmux(t, origin)

	client := fixture.attachCLIClient(t, origin.ID)

	target := fixture.newLiveSession(t, "target-session")

	root := newRootCommand(t.Context())
	root.SetArgs(append([]string{actionCmd, "session-new", "target-session", "--dir", "/tmp"}, fixture.sessionArgs()...))

	var stdout, stderr strings.Builder
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	if err := root.Execute(); err != nil {
		t.Fatalf("interactive new failed: %v\nstderr: %s", err, stderr.String())
	}

	if displayed := fixture.clientDisplayedSession(t, client); displayed != target.ID {
		t.Fatalf("client displays %s, want target %s after interactive enter", displayed, target.ID)
	}

	fixture.assertNoDefaultLeaseRoot(t)
}

func TestCLIIntegrationSessionNewOutsideTmuxCreatesWithoutAttach(t *testing.T) {
	fixture := newCLISessionFixture(t)

	clearAmbientTmux(t)

	exec := executeJSON(t, append([]string{actionCmd, "session-new", "scripted-session", "--dir", "/tmp"}, append(fixture.sessionArgs(), testJSONFlag)...)...)
	if exec.err != nil || !exec.envelope.OK {
		t.Fatalf("create-only new --json outside tmux failed: %v / %+v", exec.err, exec.envelope)
	}

	result := decodeJSONPayload[sessionNewJSONResult](t, exec.envelope.Result)
	if !result.Created {
		t.Fatal("session was not reported as created")
	}

	if _, err := fixture.server.FindSession(t.Context(), "scripted-session"); err != nil {
		t.Fatalf("created session missing: %v", err)
	}

	fixture.assertNoDefaultLeaseRoot(t)
}

func TestCLIIntegrationSessionNewInteractiveOutsideTmuxFailsBeforeCreate(t *testing.T) {
	fixture := newCLISessionFixture(t)

	clearAmbientTmux(t)

	root := newRootCommand(t.Context())
	root.SetArgs(append([]string{actionCmd, "session-new", "interactive-session", "--dir", "/tmp"}, fixture.sessionArgs()...))

	var stdout, stderr strings.Builder
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	err := root.Execute()
	if err == nil {
		t.Fatal("interactive new outside tmux succeeded without an interactive terminal")
	}

	if !errors.Is(err, session.ErrTerminalRequired) {
		t.Fatalf("error = %v, want ErrTerminalRequired", err)
	}

	if _, findErr := fixture.server.FindSession(t.Context(), "interactive-session"); !errors.Is(findErr, gotmux.ErrNotFound) {
		t.Fatalf("session was created on server despite terminal stream failure: %v", findErr)
	}
}

func TestCLIIntegrationSessionDeleteRemovesLiveAndSavedState(t *testing.T) {
	fixture := newCLISessionFixture(t)

	session := fixture.fixtureSession(t)

	fixture.newLiveSession(t, "keep-alive")
	fixture.saveLiveSession(t, session.ID)

	if names := fixture.savedGenerationNames(t); !containsString(names, "fixture") {
		t.Fatalf("fixture session was not saved: %v", names)
	}

	exec := executeJSON(t, append([]string{actionCmd, "session-delete", "fixture", testRepositoryFlag, fixture.repository}, append(fixture.deleteArgs(), testJSONFlag)...)...)
	if exec.err != nil || !exec.envelope.OK {
		t.Fatalf("session delete failed: %v / %+v", exec.err, exec.envelope)
	}

	result := decodeJSONPayload[sessionDeleteJSONResult](t, exec.envelope.Result)
	if !result.LiveKilled || !result.SavedDeleted {
		t.Fatalf("delete result: %+v", result)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("live session survived deletion: %v", err)
	}

	if names := fixture.savedGenerationNames(t); containsString(names, "fixture") {
		t.Fatalf("saved session survived deletion: %v", names)
	}
}

func TestCLIIntegrationSessionDeleteRetainsRecoveryBarrier(t *testing.T) {
	fixture := newCLISessionFixture(t)

	session := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "keep-alive")
	fixture.saveLiveSession(t, session.ID)
	fixture.beginPendingSave(t)

	exec := executeJSON(t, append([]string{actionCmd, "session-delete", "fixture", testRepositoryFlag, fixture.repository}, append(fixture.deleteArgs(), testJSONFlag)...)...)
	if exec.err == nil {
		t.Fatal("delete succeeded while a save attempt was pending")
	}

	if exec.envelope.OK {
		t.Fatalf("envelope reported success: %+v", exec.envelope)
	}

	if exec.envelope.Error.Code != "recoveryRequired" {
		t.Fatalf("error code = %q, want recoveryRequired", exec.envelope.Error.Code)
	}

	result := decodeJSONPayload[sessionDeleteJSONResult](t, exec.envelope.Result)
	if result.LiveKilled || result.SavedDeleted {
		t.Fatalf("refused deletion reported work: %+v", result)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("live session was destroyed by a refused deletion: %v", err)
	}

	if names := fixture.savedGenerationNames(t); !containsString(names, "fixture") {
		t.Fatalf("saved session was removed by a refused deletion: %v", names)
	}
}

func TestCLIIntegrationSessionDeleteRequiresForceForAttachedSession(t *testing.T) {
	fixture := newCLISessionFixture(t)

	session := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "keep-alive")
	fixture.saveLiveSession(t, session.ID)

	fixture.attachCLIClient(t, session.ID)

	refused := executeJSON(t, append([]string{actionCmd, "session-delete", "fixture", testRepositoryFlag, fixture.repository}, append(fixture.deleteArgs(), testJSONFlag)...)...)
	if refused.err == nil {
		t.Fatal("attached session was deleted without --force")
	}

	if refused.envelope.OK {
		t.Fatalf("envelope reported success: %+v", refused.envelope)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); err != nil {
		t.Fatalf("attached session was destroyed: %v", err)
	}

	forced := executeJSON(t, append([]string{actionCmd, "session-delete", "fixture", "--force", testRepositoryFlag, fixture.repository}, append(fixture.deleteArgs(), testJSONFlag)...)...)
	if forced.err != nil || !forced.envelope.OK {
		t.Fatalf("forced delete failed: %v / %+v", forced.err, forced.envelope)
	}

	forcedResult := decodeJSONPayload[sessionDeleteJSONResult](t, forced.envelope.Result)
	if !forcedResult.LiveKilled || !forcedResult.SavedDeleted {
		t.Fatalf("forced delete result: %+v", forcedResult)
	}
}

func TestCLIIntegrationSessionKillReportsMissingSession(t *testing.T) {
	fixture := newCLISessionFixture(t)

	fixture.newLiveSession(t, "keep-alive")

	exec := executeJSON(t, append([]string{actionCmd, "session-kill", "missing-session"}, append(fixture.sessionArgs(), testJSONFlag)...)...)
	if exec.err == nil {
		t.Fatal("kill of a missing session reported success")
	}

	if exec.envelope.OK {
		t.Fatalf("envelope reported success: %+v", exec.envelope)
	}

	result := decodeJSONPayload[sessionKillJSONResult](t, exec.envelope.Result)
	if result.Killed {
		t.Fatal("partial result claimed a kill that never happened")
	}
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}

func TestCLIIntegrationSessionDeleteRemovesFromDefaultXDGRepository(t *testing.T) {
	fixture := newCLISessionFixture(t)

	xdgDir := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_DATA_HOME", xdgDir)

	defaultRepoPath := filepath.Join(xdgDir, "sesh", "snapshots")

	session := fixture.fixtureSession(t)
	fixture.newLiveSession(t, "keep-alive")

	saveExec := executeJSON(
		t,
		actionCmd, "snapshot-save",
		testRepositoryFlag, defaultRepoPath,
		testSocketFlag, fixture.socket,
		testLeaseRootFlag, fixture.leaseRoot,
		testSessionFlag, string(session.ID),
		testNoConfigFlag,
		testJSONFlag,
	)
	if saveExec.err != nil && !errors.Is(saveExec.err, cli.ErrPartialSnapshot) {
		t.Fatalf("saving to default repository failed: %v", saveExec.err)
	}

	exec := executeJSON(t, actionCmd, "session-delete", "fixture", testSocketFlag, fixture.socket, testLeaseRootFlag, fixture.leaseRoot, testNoConfigFlag, "--history", fixture.historyPath, testJSONFlag)
	if exec.err != nil || !exec.envelope.OK {
		t.Fatalf("session delete without --repository failed: %v / %+v", exec.err, exec.envelope)
	}

	result := decodeJSONPayload[sessionDeleteJSONResult](t, exec.envelope.Result)
	if !result.LiveKilled || !result.SavedDeleted {
		t.Fatalf("delete result: %+v, want live and saved deleted", result)
	}

	if _, err := fixture.server.FindSession(t.Context(), "fixture"); !errors.Is(err, gotmux.ErrNotFound) {
		t.Fatalf("live session survived deletion: %v", err)
	}
}

func TestCLIIntegrationSessionDeleteWithoutDefaultRepositorySucceedsForLive(t *testing.T) {
	fixture := newCLISessionFixture(t)

	xdgDir := filepath.Join(t.TempDir(), "xdg-empty")
	t.Setenv("XDG_DATA_HOME", xdgDir)

	fixture.newLiveSession(t, "keep-alive")

	exec := executeJSON(t, actionCmd, "session-delete", "fixture", testSocketFlag, fixture.socket, testLeaseRootFlag, fixture.leaseRoot, testNoConfigFlag, "--history", fixture.historyPath, testJSONFlag)
	if exec.err != nil || !exec.envelope.OK {
		t.Fatalf("session delete failed: %v / %+v", exec.err, exec.envelope)
	}

	result := decodeJSONPayload[sessionDeleteJSONResult](t, exec.envelope.Result)
	if !result.LiveKilled || result.SavedDeleted {
		t.Fatalf("delete result: %+v, want liveKilled: true, savedDeleted: false", result)
	}

	defaultRepoPath := filepath.Join(xdgDir, "sesh", "snapshots")
	if _, err := os.Stat(defaultRepoPath); !os.IsNotExist(err) {
		t.Fatalf("delete created default repository directory: %v", err)
	}
}
