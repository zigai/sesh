//go:build linux && integration

package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zigai/gotmux/tmux"
	"github.com/zigai/gotmux/tmuxtest"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var (
	errToolNotFound = errors.New("tool not found in PATH")
	errNoNvimSocket = errors.New("no nvim socket endpoint found for process")
)

const (
	fixturePollTimeout = 10 * time.Second
	fixturePollTick    = 20 * time.Millisecond
)

func resolveApplicationTool(required bool, name string, lookFn func(string) (string, error)) (string, bool, error) {
	path, err := lookFn(name)
	if err == nil {
		return path, false, nil
	}

	if required {
		return "", false, fmt.Errorf("required application integration tool %q is unavailable: %w", name, err)
	}

	return "", true, nil
}

func requireApplicationTool(t *testing.T, name string) string {
	t.Helper()

	required := os.Getenv("SESH_APPLICATION_INTEGRATION_REQUIRED") == "1"

	path, skipped, err := resolveApplicationTool(required, name, exec.LookPath)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if skipped {
		t.Skipf("optional application integration tool %q is unavailable", name)
	}

	return path
}

type applicationTestFixture struct {
	binary   string
	workDir  string
	repoDir  string
	leaseDir string
	env      []string
}

func newApplicationTestFixture(t *testing.T) applicationTestFixture {
	t.Helper()

	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}

	root := t.TempDir()
	binary := filepath.Join(root, "bin", "sesh")
	workDir := filepath.Join(root, "outside-checkout")
	homeDir := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	stateDir := filepath.Join(root, "state")
	cacheDir := filepath.Join(root, "cache")
	runtimeDir := filepath.Join(root, "runtime")
	tmpDir := filepath.Join(root, "tmp")

	paths := []string{
		filepath.Dir(binary),
		workDir,
		homeDir,
		configDir,
		dataDir,
		stateDir,
		cacheDir,
		runtimeDir,
		tmpDir,
	}

	for _, p := range paths {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatalf("creating isolated directory %s: %v", p, err)
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	buildCmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, "./cmd/sesh")
	buildCmd.Dir = repoRoot

	buildCmd.Env = append(os.Environ(), "GOWORK=off")

	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("building sesh standalone artifact: %v\noutput: %s", err, out)
	}

	isolatedEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + homeDir,
		"XDG_CONFIG_HOME=" + configDir,
		"XDG_DATA_HOME=" + dataDir,
		"XDG_STATE_HOME=" + stateDir,
		"XDG_CACHE_HOME=" + cacheDir,
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"TMPDIR=" + tmpDir,
		"GOWORK=off",
		"TERM=dumb",
		"NO_COLOR=1",
		"LANG=C.UTF-8",
	}

	verCmd := exec.CommandContext(ctx, binary, "--version")
	verCmd.Dir = workDir
	verCmd.Env = isolatedEnv

	out, err := verCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("executing standalone sesh --version: %v\noutput: %s", err, out)
	}

	if !strings.HasPrefix(string(out), "sesh") {
		t.Fatalf("unexpected version output from built artifact: %s", out)
	}

	return applicationTestFixture{
		binary:   binary,
		workDir:  workDir,
		repoDir:  filepath.Join(root, "repo"),
		leaseDir: filepath.Join(root, "leases"),
		env:      isolatedEnv,
	}
}

func (f applicationTestFixture) runSuccess(t *testing.T, ctx context.Context, args ...string) []byte {
	t.Helper()

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, f.binary, args...)
	cmd.Dir = f.workDir
	cmd.Env = f.env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("sesh command %v failed: %v\nstdout: %s\nstderr: %s", args, err, stdout.String(), stderr.String())
	}

	return stdout.Bytes()
}

type cliResultEnvelope struct {
	OK      bool            `json:"ok"`
	Command string          `json:"command"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type snapshotSaveResultJSON struct {
	Attempt    string `json:"attempt"`
	Generation string `json:"generation"`
	Status     string `json:"status"`
}

type snapshotRestoreResultJSON struct {
	OperationID         string                 `json:"operationId"`
	Generation          string                 `json:"generation"`
	Status              string                 `json:"status"`
	Reason              string                 `json:"reason"`
	SessionsRestored    []string               `json:"sessionsRestored"`
	Verified            bool                   `json:"verified"`
	Uncertain           bool                   `json:"uncertain"`
	MissingRequirements []snapshotEvidenceJSON `json:"missingRequirements"`
}

type snapshotEvidenceJSON struct {
	Domain string `json:"domain"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func decodeEnvelope(t *testing.T, data []byte) cliResultEnvelope {
	t.Helper()

	var env cliResultEnvelope

	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decoding CLI output envelope: %v\nraw: %s", err, data)
	}

	if !env.OK || (len(env.Error) > 0 && string(env.Error) != "null") {
		t.Fatalf("CLI returned failure envelope: %s", data)
	}

	return env
}

const nvimBufferQueryExpr = `json_encode({"pid":luaeval("(function() for _, u in ipairs(vim.api.nvim_list_uis()) do local info = vim.api.nvim_get_chan_info(u.chan) if info and info.client and info.client.attributes and info.client.attributes.pid then return info.client.attributes.pid end; end if #vim.api.nvim_list_uis() > 0 and vim.uv.os_getppid() > 1 then return vim.uv.os_getppid() end return vim.fn.getpid() end)()"),"files":map(filter(getbufinfo(), 'v:val.listed && !empty(v:val.name)'), 'fnamemodify(v:val.name, ":p")')})`

type nvimBufferQueryResponse struct {
	PID   int      `json:"pid"`
	Files []string `json:"files"`
}

func decodePayload[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()

	var payload T
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decoding payload %T: %v\nraw: %s", payload, err, string(raw))
	}

	return payload
}

func TestApplicationIntegrationRequiredGate(t *testing.T) {
	t.Run("required mode fails on missing tool", checkRequiredModeFails)
	t.Run("optional mode skips on missing tool", checkOptionalModeSkips)
	t.Run("available tool resolves in both modes", checkAvailableToolResolves)
	t.Run("required suite tools resolve when gate enabled", checkSuiteToolsResolve)
}

func checkRequiredModeFails(t *testing.T) {
	t.Helper()

	_, skipped, err := resolveApplicationTool(true, "missing-tool", func(string) (string, error) {
		return "", errToolNotFound
	})
	if err == nil || skipped {
		t.Fatalf("expected error in required mode for missing tool, got skipped=%v err=%v", skipped, err)
	}
}

func checkOptionalModeSkips(t *testing.T) {
	t.Helper()

	_, skipped, err := resolveApplicationTool(false, "missing-tool", func(string) (string, error) {
		return "", errToolNotFound
	})
	if err != nil || !skipped {
		t.Fatalf("expected skip without error in optional mode, got skipped=%v err=%v", skipped, err)
	}
}

func checkAvailableToolResolves(t *testing.T) {
	t.Helper()

	for _, req := range []bool{true, false} {
		path, skipped, err := resolveApplicationTool(req, "test-tool", func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		})
		if err != nil || skipped || path != "/usr/bin/test-tool" {
			t.Fatalf("expected clean resolution, got path=%q skipped=%v err=%v", path, skipped, err)
		}
	}
}

func checkSuiteToolsResolve(t *testing.T) {
	t.Helper()

	if os.Getenv("SESH_APPLICATION_INTEGRATION_REQUIRED") != "1" {
		t.Skip("SESH_APPLICATION_INTEGRATION_REQUIRED is not set to 1")
	}

	for _, tool := range []string{"bash", "top", "nvim", "node"} {
		path, skipped, err := resolveApplicationTool(true, tool, exec.LookPath)
		if err != nil || skipped || path == "" {
			t.Fatalf("required tool %q resolution failed: path=%q skipped=%v err=%v", tool, path, skipped, err)
		}
	}
}

func TestBuiltArtifactApplicationWorkflowRoundTrip(t *testing.T) {
	bashPath := requireApplicationTool(t, "bash")
	topPath := requireApplicationTool(t, "top")
	nvimPath := requireApplicationTool(t, "nvim")
	nodePath := requireApplicationTool(t, "node")

	fixture := newApplicationTestFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)

	defer cancel()

	noteFile, nodeScript := createTestApplicationFiles(t, fixture.workDir)
	sourceServer := tmuxtest.NewServer(t)

	const sessionName = "app-workflow-sess"

	sourceSession := setupSourceSession(
		t, ctx, sourceServer, sessionName, fixture.workDir,
		bashPath, topPath, nvimPath, nodePath, noteFile, nodeScript,
	)

	waitForSourceApplicationsReady(t, ctx, sourceSession)

	saveResult := executeSave(t, ctx, fixture, sourceServer, sourceSession)
	validateSavedRepository(t, ctx, fixture.repoDir, saveResult.Generation, sessionName, noteFile)

	destroySourceServer(t, ctx, sourceServer)

	targetServer := tmuxtest.NewServer(t)

	restoreResult := executeRestore(t, ctx, fixture, targetServer, saveResult.Generation)

	assertRestoredTargetSession(t, ctx, targetServer, sessionName, noteFile, restoreResult, fixture.env)
}

func createTestApplicationFiles(t *testing.T, workDir string) (string, string) {
	t.Helper()

	noteFile := filepath.Join(workDir, "work-notes.txt")
	if err := os.WriteFile(noteFile, []byte("sesh integration workflow notes\n"), 0o600); err != nil {
		t.Fatalf("writing note file: %v", err)
	}

	nodeScript := filepath.Join(workDir, "worker.js")
	if err := os.WriteFile(nodeScript, []byte("setInterval(() => {}, 1000);\n"), 0o600); err != nil {
		t.Fatalf("writing node script: %v", err)
	}

	return noteFile, nodeScript
}

func setupSourceSession(
	t *testing.T,
	ctx context.Context,
	server *tmux.Server,
	sessionName, workDir, bashPath, topPath, nvimPath, nodePath, noteFile, nodeScript string,
) tmux.Session {
	t.Helper()

	session, err := server.NewSession(ctx, tmux.NewSessionOptions{
		Name:    sessionName,
		Dir:     workDir,
		Window:  "shell-win",
		Program: tmux.Exec(bashPath, "--norc", "--noprofile"),
		Size:    tmux.Size{Width: 120, Height: 40},
		Start:   tmux.StartPolicyExistingOnly,
	})
	if err != nil {
		t.Fatalf("creating source session: %v", err)
	}

	if _, err = session.NewWindow(ctx, tmux.NewWindowOptions{
		Name:    "monitor-win",
		Dir:     workDir,
		Program: tmux.Exec(topPath, "-d", "100"),
	}); err != nil {
		t.Fatalf("creating monitor window: %v", err)
	}

	if _, err = session.NewWindow(ctx, tmux.NewWindowOptions{
		Name:    "editor-win",
		Dir:     workDir,
		Program: tmux.Exec(nvimPath, noteFile),
	}); err != nil {
		t.Fatalf("creating editor window: %v", err)
	}

	if _, err = session.NewWindow(ctx, tmux.NewWindowOptions{
		Name:    "script-win",
		Dir:     workDir,
		Program: tmux.Exec(nodePath, nodeScript),
	}); err != nil {
		t.Fatalf("creating script window: %v", err)
	}

	windows, err := server.Windows(ctx)
	if err != nil {
		t.Fatalf("listing source windows: %v", err)
	}

	for _, w := range windows {
		opts := w.Handle().Options()

		if err := opts.SetAutomaticRename(ctx, false); err != nil {
			t.Fatalf("setting automatic rename: %v", err)
		}

		if err := opts.SetAllowRename(ctx, false); err != nil {
			t.Fatalf("setting allow rename: %v", err)
		}
	}

	return session
}

func waitForSourceApplicationsReady(t *testing.T, ctx context.Context, session tmux.Session) {
	t.Helper()

	deadline := time.Now().Add(fixturePollTimeout)

	for time.Now().Before(deadline) {
		if checkSourceApplicationsReady(ctx, session) {
			return
		}

		time.Sleep(fixturePollTick)
	}

	t.Fatal("source session applications did not reach observable readiness within deadline")
}

func checkSourceApplicationsReady(ctx context.Context, session tmux.Session) bool {
	windows, _, err := session.WindowInfos(ctx)
	if err != nil || len(windows) < 4 {
		return false
	}

	observed := make(map[string]bool)

	for _, win := range windows {
		pane, err := win.Handle().ActivePane(ctx)
		if err != nil {
			continue
		}

		info, err := pane.Info(ctx)
		if err != nil || info.PID <= 0 {
			continue
		}

		birth, err := process.ReadIdentity(ctx, info.PID)
		if err != nil {
			continue
		}

		obs, err := process.ObserveForeground(ctx, process.ForegroundRequest{
			PanePID:           info.PID,
			ExpectedPaneBirth: birth,
		})
		if err != nil || obs == nil {
			continue
		}

		execBase := filepath.Base(string(obs.Executable))

		if isExpectedProcessForWindow(win.Name, execBase) {
			observed[win.Name] = true
		}
	}

	return len(observed) == 4
}

func isExpectedProcessForWindow(windowName, execBase string) bool {
	switch windowName {
	case "shell-win":
		return execBase == "bash"
	case "monitor-win":
		return execBase == "top"
	case "editor-win":
		return execBase == "nvim"
	case "script-win":
		return execBase == "node"
	default:
		return false
	}
}

func executeSave(
	t *testing.T,
	ctx context.Context,
	fixture applicationTestFixture,
	server *tmux.Server,
	session tmux.Session,
) snapshotSaveResultJSON {
	t.Helper()

	stdout := fixture.runSuccess(t, ctx, "action", "snapshot-save",
		"--repository", fixture.repoDir,
		"--socket", server.Endpoint().SocketPath,
		"--lease-root", fixture.leaseDir,
		"--session", string(session.ID()),
		"--no-config",
		"--json",
	)

	envelope := decodeEnvelope(t, stdout)
	result := decodePayload[snapshotSaveResultJSON](t, envelope.Result)

	if result.Generation == "" || result.Attempt == "" || result.Status != "best_effort" {
		t.Fatalf("unexpected save result payload: %+v", result)
	}

	return result
}

func validateSavedRepository(
	t *testing.T,
	ctx context.Context,
	repoDir, generation, sessionName, noteFile string,
) {
	t.Helper()

	repo, err := store.Open(ctx, repoDir)
	if err != nil {
		t.Fatalf("opening repository at %s: %v", repoDir, err)
	}

	t.Cleanup(func() {
		if err := repo.Close(); err != nil {
			t.Errorf("closing repository: %v", err)
		}
	})

	manifest, err := repo.Read(ctx, generation)
	if err != nil {
		t.Fatalf("reading manifest %s: %v", generation, err)
	}

	assertPersistedManifest(t, manifest, sessionName, noteFile)
}

func assertPersistedManifest(t *testing.T, manifest model.Manifest, sessionName, noteFile string) {
	t.Helper()

	if len(manifest.Sessions) != 1 {
		t.Fatalf("manifest session count = %d, want 1", len(manifest.Sessions))
	}

	if string(manifest.Sessions[0].Name) != sessionName {
		t.Fatalf("saved session name = %q, want %q", manifest.Sessions[0].Name, sessionName)
	}

	if len(manifest.Windows) != 4 {
		t.Fatalf("manifest windows count = %d, want 4", len(manifest.Windows))
	}

	if len(manifest.Panes) != 4 {
		t.Fatalf("manifest panes count = %d, want 4", len(manifest.Panes))
	}

	foundPanes := make(map[string]bool)

	for _, pane := range manifest.Panes {
		kind := assertManifestPane(t, pane, noteFile)
		if kind != "" {
			foundPanes[kind] = true
		}
	}

	for _, wantKind := range []string{"bash", "top", "nvim", "node"} {
		if !foundPanes[wantKind] {
			t.Errorf("manifest missing expected pane for application %q", wantKind)
		}
	}
}

func assertManifestPane(t *testing.T, pane model.Pane, noteFile string) string {
	t.Helper()

	execName := filepath.Base(string(pane.Launch.Executable))

	switch execName {
	case "bash":
		if pane.Launch.Recovery != "restart" {
			t.Errorf("bash recovery = %q, want restart", pane.Launch.Recovery)
		}

		return "bash"
	case "top":
		if pane.Launch.Recovery != "restart" {
			t.Errorf("top recovery = %q, want restart", pane.Launch.Recovery)
		}

		return "top"
	case "nvim":
		if pane.Launch.Recovery != "restart" && pane.Launch.Recovery != "resume" {
			t.Errorf("nvim recovery = %q, reason = %q, args = %v, want restart or resume",
				pane.Launch.Recovery, pane.Launch.Reason, pane.Launch.Arguments)
		}

		assertNvimLaunchContainsFile(t, pane.Launch.Arguments, noteFile)

		return "nvim"
	case "node":
		if pane.Launch.Recovery != "unverified" {
			t.Errorf("unsupported node script recovery = %q, want unverified", pane.Launch.Recovery)
		}

		return "node"
	default:
		return ""
	}
}

func assertNvimLaunchContainsFile(t *testing.T, arguments []model.Bytes, noteFile string) {
	t.Helper()

	for _, arg := range arguments {
		if string(arg) == noteFile {
			return
		}
	}

	t.Errorf("nvim arguments %v do not contain noteFile %q", arguments, noteFile)
}

func destroySourceServer(t *testing.T, ctx context.Context, server *tmux.Server) {
	t.Helper()

	if err := server.Kill(ctx); err != nil {
		t.Fatalf("killing source server: %v", err)
	}

	if _, err := server.Sessions(ctx); err == nil {
		t.Fatal("source server remained reachable after kill")
	}
}

func executeRestore(
	t *testing.T,
	ctx context.Context,
	fixture applicationTestFixture,
	target *tmux.Server,
	generation string,
) snapshotRestoreResultJSON {
	t.Helper()

	stdout := fixture.runSuccess(t, ctx, "action", "snapshot-restore",
		"--repository", fixture.repoDir,
		"--socket", target.Endpoint().SocketPath,
		"--lease-root", fixture.leaseDir,
		"--generation", generation,
		"--no-config",
		"--json",
	)

	envelope := decodeEnvelope(t, stdout)
	result := decodePayload[snapshotRestoreResultJSON](t, envelope.Result)

	if result.OperationID == "" || result.Generation != generation || result.Status != "best_effort" || !result.Verified || result.Uncertain {
		t.Fatalf("unexpected restore result payload: %+v", result)
	}

	if len(result.SessionsRestored) != 1 {
		t.Fatalf("restored sessions count = %d, want 1", len(result.SessionsRestored))
	}

	var foundUnsupportedApp bool

	for _, req := range result.MissingRequirements {
		if req.Reason == "application_restart_unsupported" {
			foundUnsupportedApp = true

			break
		}
	}

	if !foundUnsupportedApp {
		t.Errorf("restore missing expected requirement application_restart_unsupported: %+v", result.MissingRequirements)
	}

	return result
}

func assertRestoredTargetSession(
	t *testing.T,
	ctx context.Context,
	targetServer *tmux.Server,
	sessionName, noteFile string,
	restoreResult snapshotRestoreResultJSON,
	env []string,
) {
	t.Helper()

	restoredSession, err := targetServer.FindSession(ctx, sessionName)
	if err != nil {
		t.Fatalf("finding restored session %q: %v", sessionName, err)
	}

	windows, _, err := restoredSession.WindowInfos(ctx)
	if err != nil {
		t.Fatalf("getting restored window infos: %v", err)
	}

	if len(windows) != 4 {
		t.Fatalf("restored window count = %d, want 4", len(windows))
	}

	for _, win := range windows {
		pane, err := win.Handle().ActivePane(ctx)
		if err != nil {
			t.Fatalf("getting active pane for window %q: %v", win.Name, err)
		}

		info, err := pane.Info(ctx)
		if err != nil || info.PID <= 0 {
			t.Fatalf("window %q pane has invalid PID: %d, err: %v", win.Name, info.PID, err)
		}

		assertRestoredWindowPane(t, ctx, win.Name, info.PID, noteFile, restoreResult, env)
	}
}

func assertRestoredWindowPane(
	t *testing.T,
	ctx context.Context,
	windowName string,
	pid int,
	noteFile string,
	restoreResult snapshotRestoreResultJSON,
	env []string,
) {
	t.Helper()

	switch windowName {
	case "shell-win":
		assertRestoredShell(t, ctx, pid)
	case "monitor-win":
		assertRestoredMonitor(t, ctx, pid)
	case "editor-win":
		assertRestoredEditor(t, ctx, pid, noteFile, restoreResult, env)
	case "script-win":
		assertRestoredScriptFallback(t, ctx, pid)
	}
}

func assertRestoredShell(t *testing.T, ctx context.Context, pid int) {
	t.Helper()

	obs := waitForProcessForeground(t, ctx, pid, "bash")

	obsEnv, err := process.Observe(ctx, process.Request{
		PID:             pid,
		ExpectedBirth:   obs.Birth,
		EnvironmentKeys: []string{"HOME"},
	})
	if err != nil {
		t.Fatalf("observing shell environment: %v", err)
	}

	var shellHome string

	for _, envVar := range obsEnv.Environment {
		if string(envVar.Name) == "HOME" {
			shellHome = string(envVar.Value)

			break
		}
	}

	if shellHome == "" {
		t.Error("restored shell has empty HOME environment variable")
	}

	if realHome := os.Getenv("HOME"); realHome != "" && shellHome == realHome {
		t.Errorf("restored shell inherited host user HOME %q instead of sanitized fixture HOME", realHome)
	}
}

func assertRestoredMonitor(t *testing.T, ctx context.Context, pid int) {
	t.Helper()

	_ = waitForProcessForeground(t, ctx, pid, "top")
}

func assertRestoredEditor(
	t *testing.T,
	ctx context.Context,
	pid int,
	noteFile string,
	restoreResult snapshotRestoreResultJSON,
	env []string,
) {
	t.Helper()

	obs := waitForProcessForeground(t, ctx, pid, "nvim")

	if !restoreResult.Verified {
		t.Error("restore result Verified is false, want true for structural restore verification")
	}

	var hasFileArg bool

	for _, arg := range obs.Arguments {
		if string(arg) == noteFile {
			hasFileArg = true

			break
		}
	}

	if !hasFileArg {
		t.Errorf("restored nvim arguments %v do not contain file %q", obs.Arguments, noteFile)
	}

	verifyNvimReopenedFile(t, ctx, pid, obs.PID, noteFile, env)
}

func verifyNvimReopenedFile(
	t *testing.T,
	ctx context.Context,
	pid int,
	expectedPID int,
	noteFile string,
	env []string,
) {
	t.Helper()

	socketPath, err := findNvimSocket(pid)
	if err != nil {
		t.Fatalf("finding nvim socket for pid %d: %v", pid, err)
	}

	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(queryCtx, "nvim", "--clean", "--server", socketPath, "--remote-expr", nvimBufferQueryExpr)
	cmd.Env = env

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("querying nvim buffers via endpoint %s: %v\noutput: %s", socketPath, err, out)
	}

	var response nvimBufferQueryResponse

	if err := json.Unmarshal(bytes.TrimSpace(out), &response); err != nil {
		t.Fatalf("unmarshaling nvim buffer query response: %v\noutput: %s", err, out)
	}

	if response.PID != expectedPID {
		t.Errorf("nvim buffer query returned PID %d, want observed PID %d", response.PID, expectedPID)
	}

	if !slices.Contains(response.Files, noteFile) {
		t.Errorf("nvim buffer files %v do not contain exact expected file %s", response.Files, noteFile)
	}
}

func findNvimSocket(pid int) (string, error) {
	socketInodes := collectSocketInodes(pid)
	if len(socketInodes) == 0 {
		return "", errNoNvimSocket
	}

	return matchNvimSocketPath(socketInodes)
}

func collectSocketInodes(pid int) map[string]bool {
	pids := []int{pid}

	childrenFile := filepath.Join("/proc", strconv.Itoa(pid), "task", strconv.Itoa(pid), "children")
	if childData, err := os.ReadFile(childrenFile); err == nil {
		for field := range strings.FieldsSeq(string(childData)) {
			if childPID, cErr := strconv.Atoi(field); cErr == nil && childPID > 0 {
				pids = append(pids, childPID)
			}
		}
	}

	socketInodes := make(map[string]bool)

	for _, p := range pids {
		fdDir := filepath.Join("/proc", strconv.Itoa(p), "fd")

		entries, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			target, rErr := os.Readlink(filepath.Join(fdDir, entry.Name()))
			if rErr != nil {
				continue
			}

			if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
				inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
				socketInodes[inode] = true
			}
		}
	}

	return socketInodes
}

func matchNvimSocketPath(socketInodes map[string]bool) (string, error) {
	data, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return "", fmt.Errorf("reading /proc/net/unix: %w", err)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}

		inode := fields[6]
		if !socketInodes[inode] {
			continue
		}

		path := fields[7]
		if strings.Contains(path, "nvim") {
			return path, nil
		}
	}

	return "", errNoNvimSocket
}

func assertRestoredScriptFallback(t *testing.T, ctx context.Context, pid int) {
	t.Helper()

	birth, err := process.ReadIdentity(ctx, pid)
	if err != nil {
		return
	}

	obs, err := process.ObserveForeground(ctx, process.ForegroundRequest{
		PanePID:           pid,
		ExpectedPaneBirth: birth,
	})
	if err == nil && obs != nil {
		execName := filepath.Base(string(obs.Executable))
		if execName == "node" {
			t.Errorf("unsupported node script was replayed, want safe fallback shell: %s", execName)
		}
	}
}

func waitForProcessForeground(t *testing.T, ctx context.Context, pid int, expectedBase string) *process.Observation {
	t.Helper()

	birth, err := process.ReadIdentity(ctx, pid)
	if err != nil {
		t.Fatalf("reading process identity for pid %d: %v", pid, err)
	}

	deadline := time.Now().Add(fixturePollTimeout)

	for time.Now().Before(deadline) {
		obs, err := process.ObserveForeground(ctx, process.ForegroundRequest{
			PanePID:           pid,
			ExpectedPaneBirth: birth,
		})
		if err == nil && obs != nil && filepath.Base(string(obs.Executable)) == expectedBase {
			return obs
		}

		time.Sleep(fixturePollTick)
	}

	t.Fatalf("process %d did not achieve expected foreground %s within deadline", pid, expectedBase)

	return nil
}
