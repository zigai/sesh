package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/session"
)

func gitTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.AgentSessions.Enabled = true
	cfg.AgentSessions.PopupCounts = true
	cfg.Display.AgentsAgentFormat = "{tree}{agent_git_branch}{right}{agent_git_changes}{status}"

	return cfg
}

func gitTestSessions() []session.Session {
	return []session.Session{
		{
			ID:               "$1",
			Name:             testSupportSessionAlphaName,
			Kind:             session.KindLive,
			Origin:           session.OriginLocal,
			TmuxServerSocket: "/tmp/tmux-1000/default",
			Agents: []session.AgentSession{
				{PaneID: "%1", CWD: "/work/beta"},
				{PaneID: "%2", CWD: "/work/alpha"},
				{PaneID: "%3", CWD: "/work/alpha"},
			},
		},
		{
			ID:               "$2",
			Name:             "remote",
			Kind:             session.KindLive,
			Origin:           session.OriginSSH,
			TmuxServerSocket: "/tmp/tmux-1000/default",
			Agents: []session.AgentSession{
				{PaneID: "%9", CWD: "/remote/work"},
			},
		},
	}
}

func gitTestModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	deps := Dependencies{
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) { return nil, nil },
	}

	model, err := NewPopupModel(context.Background(), gitTestConfig(), testState(), testServices(deps))
	if err != nil {
		t.Fatalf("NewPopupModel() error = %v", err)
	}

	model.applySessionRows(sessionLoadResultMsg{sessions: gitTestSessions(), status: session.StatusComplete})

	return model
}

func TestAgentGitRefreshAppliesMetadataToLocalAgentRows(t *testing.T) {
	var directories []string

	model := gitTestModel(t)
	model.gitScan = func(_ context.Context, dirs []string) map[string]agentGitCacheEntry {
		directories = append([]string(nil), dirs...)

		return map[string]agentGitCacheEntry{
			"/work/alpha": {GitBranch: "main", GitChanges: "+2 -1", GitAhead: 1, GitTrackingKnown: true},
			"/work/beta":  {GitBranch: "feature"},
		}
	}

	command := model.loadAgentGitCommand()
	if command == nil {
		t.Fatal("loadAgentGitCommand() = nil, want refresh command")
	}

	msg, ok := command().(agentGitResultMsg)
	if !ok {
		t.Fatalf("refresh command returned %T, want agentGitResultMsg", msg)
	}

	model = updateTestModel(t, model, msg)

	if want := []string{"/work/alpha", "/work/beta"}; !slicesEqual(directories, want) {
		t.Fatalf("scanned directories = %v, want %v", directories, want)
	}

	agents := model.displaySessions[0].Agents
	if agents[0].GitBranch != "feature" {
		t.Fatalf("beta agent branch = %q, want feature", agents[0].GitBranch)
	}

	if agents[1].GitBranch != "main" || agents[1].GitChanges != "+2 -1" || agents[1].GitAhead != 1 {
		t.Fatalf("alpha agent git = %+v, want main/+2 -1/1", agents[1])
	}

	if agents[1].GitTrackingKnown != true {
		t.Fatalf("alpha agent tracking known = false, want true")
	}

	for _, agent := range model.displaySessions[1].Agents {
		if agent.GitBranch != "" || agent.GitChanges != "" {
			t.Fatalf("remote agent git metadata = %+v, want none", agent)
		}
	}
}

func TestAgentGitRefreshRejectsSupersededResult(t *testing.T) {
	model := gitTestModel(t)
	model.displayMode = displayModeAgents
	model.gitScan = func(context.Context, []string) map[string]agentGitCacheEntry {
		return map[string]agentGitCacheEntry{"/work/alpha": {GitBranch: "stale-branch"}}
	}

	command := model.loadAgentGitCommand()
	if command == nil {
		t.Fatal("loadAgentGitCommand() = nil, want refresh command")
	}

	msg, ok := command().(agentGitResultMsg)
	if !ok {
		t.Fatalf("refresh command returned %T, want agentGitResultMsg", msg)
	}

	model.invalidateAgentGit()

	model = updateTestModel(t, model, msg)

	if branch := model.displaySessions[0].Agents[1].GitBranch; branch != "" {
		t.Fatalf("superseded result applied branch %q, want none", branch)
	}
}

func TestAgentGitRefreshCancelsInFlightScanOnClose(t *testing.T) {
	scanStarted := make(chan struct{})
	scanCanceled := make(chan struct{})

	model := gitTestModel(t)
	model.gitScan = func(ctx context.Context, _ []string) map[string]agentGitCacheEntry {
		close(scanStarted)
		<-ctx.Done()
		close(scanCanceled)

		return nil
	}

	command := model.loadAgentGitCommand()
	if command == nil {
		t.Fatal("loadAgentGitCommand() = nil, want refresh command")
	}

	go func() { _ = command() }()

	select {
	case <-scanStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("git scan did not start")
	}

	model.Close()

	select {
	case <-scanCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("git scan was not canceled by Close()")
	}
}

func TestAgentGitRefreshDoesNotOverlapScans(t *testing.T) {
	var calls atomic.Int32

	model := gitTestModel(t)
	model.gitScan = func(ctx context.Context, _ []string) map[string]agentGitCacheEntry {
		calls.Add(1)
		<-ctx.Done()

		return nil
	}

	first := model.loadAgentGitCommand()
	if first == nil {
		t.Fatal("loadAgentGitCommand() = nil, want refresh command")
	}

	model.loadAgentGitCommand()

	if got := calls.Load(); got != 0 {
		t.Fatalf("scan invocations before running the command = %d, want 0", got)
	}

	done := make(chan struct{})

	go func() {
		_ = first()

		close(done)
	}()

	deadline := time.After(2 * time.Second)

	for calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("git scan did not start")
		case <-time.After(time.Millisecond):
		}
	}

	model.loadAgentGitCommand()

	model.Close()
	<-done

	if got := calls.Load(); got != 1 {
		t.Fatalf("scan invocations = %d, want 1", got)
	}
}

func TestAgentGitScanIsNotRunWhileRendering(t *testing.T) {
	var calls atomic.Int32

	model := gitTestModel(t)
	model.gitScan = func(context.Context, []string) map[string]agentGitCacheEntry {
		calls.Add(1)

		return nil
	}

	model = updateTestModel(t, model, tea.WindowSizeMsg{Width: 100, Height: 30})

	content := viewString(model.View())
	if !strings.Contains(stripANSI(content), testSupportSessionAlphaName) {
		t.Fatalf("rendered frame = %q, want it to contain the session name", stripANSI(content))
	}

	if got := calls.Load(); got != 0 {
		t.Fatalf("git scan invocations during rendering = %d, want 0", got)
	}
}

func TestAgentGitDisabledWithoutGitFormatFields(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentSessions.Enabled = true
	cfg.AgentSessions.PopupCounts = true
	cfg.Display.AgentsAgentFormat = "{tree}{harness}{right}{status}"
	cfg.Display.WorkTaskFormat = "{tree}{harness}{right}{status}"

	model, err := NewPopupModel(context.Background(), cfg, testState(), testServices(Dependencies{
		LoadAgentSummaries: func(context.Context) ([]session.AgentSummary, error) { return nil, nil },
	}))
	if err != nil {
		t.Fatalf("NewPopupModel() error = %v", err)
	}

	if model.agentGitEnabled() {
		t.Fatal("agentGitEnabled() = true, want false without git fields in the agent and task formats")
	}

	if cmd := model.loadAgentGitCommand(); cmd != nil {
		go func() { _ = cmd() }()
	}
}

func TestFormatGitChangesReportsCountsAndUnknownOutput(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{"insertions and deletions", " 3 files changed, 12 insertions(+), 4 deletions(-)", "+12 -4"},
		{"singular forms", " 1 file changed, 1 insertion(+), 1 deletion(-)", "+1 -1"},
		{"no changes", "", "+0 -0"},
		{"malformed count", " 2 files changed, many insertions(+)", ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := formatGitChanges(testCase.output); got != testCase.want {
				t.Fatalf("formatGitChanges(%q) = %q, want %q", testCase.output, got, testCase.want)
			}
		})
	}
}

func TestBoundedBufferTruncatesOutput(t *testing.T) {
	buffer := &boundedBuffer{limit: 8}

	written, err := buffer.Write([]byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if written != 16 {
		t.Fatalf("Write() = %d, want the full length reported to the writer", written)
	}

	if got := buffer.String(); got != "01234567" {
		t.Fatalf("buffer = %q, want truncated content", got)
	}
}

func TestAgentGitScanRespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if entries := scanGitMetadata(ctx, []string{t.TempDir()}); len(entries) != 0 {
		t.Fatalf("scanGitMetadata() = %v, want no entries for a canceled context", entries)
	}
}

func TestSessionIdentityForNamePrefersLiveRows(t *testing.T) {
	model := gitTestModel(t)

	model.sessions = append(model.sessions, session.Session{
		Name:             testSupportSessionAlphaName,
		Kind:             session.KindSaved,
		Origin:           session.OriginLocal,
		RepositorySource: "/repo",
	})

	identity := model.sessionIdentityForName(testSupportSessionAlphaName)
	if identity.Name != testSupportSessionAlphaName || identity.RepositorySource != "" {
		t.Fatalf("identity = %+v, want the live identity without a repository source", identity)
	}

	fallback := model.sessionIdentityForName("missing")
	if fallback.Name != "missing" {
		t.Fatalf("identity for missing session = %+v, want name-only identity", fallback)
	}
}

func TestScanGitMetadataReadsRealRepository(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}

	directory := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()

		command := exec.CommandContext(t.Context(), gitPath, args...)
		command.Dir = directory

		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}

	runGit("-c", "init.defaultBranch=main", "init")
	runGit("config", "user.email", "picker@example.invalid")
	runGit("config", "user.name", "Picker Test")

	if err := os.WriteFile(filepath.Join(directory, "file.txt"), []byte("first\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	runGit("add", "file.txt")
	runGit("commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(directory, "file.txt"), []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	entries := scanGitMetadata(t.Context(), []string{directory, directory})

	entry, ok := entries[directory]
	if !ok {
		t.Fatalf("scanGitMetadata() = %v, want an entry for the repository", entries)
	}

	if entry.GitBranch != "main" {
		t.Fatalf("GitBranch = %q, want main", entry.GitBranch)
	}

	if entry.Worktree != directory {
		t.Fatalf("Worktree = %q, want %q", entry.Worktree, directory)
	}

	if entry.GitChanges != "+1 -0" {
		t.Fatalf("GitChanges = %q, want +1 -0", entry.GitChanges)
	}

	assertCleanGitExtrasExceptDirty(t, entry)

	if entry.GitTrackingKnown {
		t.Fatal("GitTrackingKnown = true, want false without an upstream")
	}
}

func assertCleanGitExtrasExceptDirty(t *testing.T, entry agentGitCacheEntry) {
	t.Helper()

	if !entry.GitDirty || entry.GitConflicts != 0 || entry.GitStash != 0 {
		t.Fatalf("git extras = %+v", entry)
	}
}

func TestParseGitStatusCountsConflictsAndDirtyWorktree(t *testing.T) {
	status := parseGitStatus("UU conflicted.txt\nAA added.txt\n M edited.txt\n?? untracked.txt")
	if !status.dirty || status.conflicts != 2 {
		t.Fatalf("parsed status = %+v", status)
	}

	if clean := parseGitStatus(""); clean.dirty || clean.conflicts != 0 {
		t.Fatalf("clean status = %+v", clean)
	}
}
