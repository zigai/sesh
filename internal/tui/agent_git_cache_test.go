package tui

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/session"
)

func TestAgentGitCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "agent-git.json")

	want := map[string]agentGitCacheEntry{"/project": {
		GitBranch: "main", Worktree: "/project", GitChanges: "+12 -3",
		LinkedWorktree: true, GitAhead: 2, GitBehind: 1, GitTrackingKnown: true,
	}}
	if err := writeAgentGitCache(path, want); err != nil {
		t.Fatal(err)
	}

	if got := readAgentGitCache(path); !maps.Equal(got, want) {
		t.Fatalf("cache round trip = %#v, want %#v", got, want)
	}

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions = %v, %v", info, err)
	}

	info, err = os.Stat(filepath.Dir(path))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("cache directory permissions = %v, %v", info, err)
	}
}

func TestAgentGitCacheInvalidDataIsColdStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-git.json")
	if readAgentGitCache(path) != nil {
		t.Fatal("missing cache produced entries")
	}

	for _, data := range []string{"broken", `{"version":2,"entries":{}}`, strings.Repeat("x", maxAgentGitCacheBytes+1)} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}

		if readAgentGitCache(path) != nil {
			t.Fatal("invalid cache produced entries")
		}
	}
}

func TestAgentGitCacheAppearsOnFirstRender(t *testing.T) {
	model := gitTestModel(t)
	t.Cleanup(model.Close)
	model.loadingSessions = true
	model.gitScan = func(context.Context, []string) (map[string]agentGitCacheEntry, []string, error) {
		t.Error("initial load or render ran a Git scan")

		return map[string]agentGitCacheEntry{}, nil, nil
	}

	entries := map[string]agentGitCacheEntry{"/work/alpha": {GitBranch: "cached-branch"}}
	if err := writeAgentGitCache(agentGitCachePath(), entries); err != nil {
		t.Fatal(err)
	}

	message := model.loadInitialSessionsCommand()()

	result, ok := message.(sessionLoadResultMsg)
	if !ok {
		t.Fatalf("initial load returned %T", message)
	}

	if !maps.Equal(result.agentGit, entries) {
		t.Fatalf("initial load omitted persisted Git state: %#v", result.agentGit)
	}

	result.sessions = gitTestSessions()
	model.handleSessionLoadResult(result)
	model = updateTestModel(t, model, tea.WindowSizeMsg{Width: 100, Height: 30})
	model.displayMode = displayModeAgents
	model.refreshDisplaySessions()
	model.filterSessions()

	if got := stripANSI(viewString(model.View())); !strings.Contains(got, "cached-branch") {
		t.Fatalf("first render omitted cached branch: %q", got)
	}
}

func TestAgentGitRefreshPersistsChangesAndReplacesStaleEntries(t *testing.T) {
	model := gitTestModel(t)
	t.Cleanup(model.Close)

	previous := map[string]agentGitCacheEntry{
		"/work/alpha": {GitBranch: "old-branch"},
		"/gone":       {GitBranch: "stale-project"},
	}
	if err := writeAgentGitCache(agentGitCachePath(), previous); err != nil {
		t.Fatal(err)
	}

	want := map[string]agentGitCacheEntry{"/work/alpha": {GitBranch: "new-branch"}}
	model.agentGit = previous
	model.gitScan = func(context.Context, []string) (map[string]agentGitCacheEntry, []string, error) {
		return want, nil, nil
	}

	command := model.loadAgentGitCommand()
	if command == nil {
		t.Fatal("no Git refresh command")
	}

	model = updateTestModel(t, model, command())
	if got := readAgentGitCache(agentGitCachePath()); !maps.Equal(got, want) {
		t.Fatalf("persisted refresh = %#v, want %#v", got, want)
	}

	if got := model.displaySessions[0].Agents[1].GitBranch; got != "new-branch" {
		t.Fatalf("displayed branch = %q", got)
	}
}

func TestAgentGitCanceledRefreshPreservesCache(t *testing.T) {
	model := gitTestModel(t)
	t.Cleanup(model.Close)

	want := map[string]agentGitCacheEntry{"/work/alpha": {GitBranch: "previous-branch"}}
	if err := writeAgentGitCache(agentGitCachePath(), want); err != nil {
		t.Fatal(err)
	}

	model.agentGit = want
	model.gitScan = func(ctx context.Context, _ []string) (map[string]agentGitCacheEntry, []string, error) {
		if ctx.Err() == nil {
			t.Error("invalidated scan context was not canceled")
		}

		return map[string]agentGitCacheEntry{"/work/alpha": {GitBranch: "canceled-branch"}}, nil, ctx.Err()
	}

	command := model.loadAgentGitCommand()
	if command == nil {
		t.Fatal("no Git refresh command")
	}

	model.invalidateAgentGit()

	model = updateTestModel(t, model, command())
	if got := readAgentGitCache(agentGitCachePath()); !maps.Equal(got, want) {
		t.Fatalf("canceled refresh changed persisted cache: %#v", got)
	}

	if !maps.Equal(model.agentGit, want) {
		t.Fatalf("canceled refresh changed displayed cache: %#v", model.agentGit)
	}
}

func TestAgentGitCacheFailureDoesNotHideLiveResults(t *testing.T) {
	model := gitTestModel(t)
	t.Cleanup(model.Close)

	if err := os.WriteFile(filepath.Dir(agentGitCachePath()), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}

	model.gitScan = func(context.Context, []string) (map[string]agentGitCacheEntry, []string, error) {
		return map[string]agentGitCacheEntry{"/work/alpha": {GitBranch: "live-branch"}}, nil, nil
	}

	command := model.loadAgentGitCommand()
	if command == nil {
		t.Fatal("no Git refresh command")
	}

	model = updateTestModel(t, model, command())
	if got := model.displaySessions[0].Agents[1].GitBranch; got != "live-branch" {
		t.Fatalf("live branch hidden after cache failure: %q", got)
	}
}

func TestAgentGitCacheRejectsSymlinksAndPublicFiles(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(directory, "agent-git.json")
	target := filepath.Join(directory, "target")

	data := []byte(`{"version":1,"entries":{"/project":{"gitBranch":"private"}}}`)
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	if readAgentGitCache(path) != nil {
		t.Fatal("symlink cache produced entries")
	}

	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}

	if readAgentGitCache(target) != nil {
		t.Fatal("public cache produced entries")
	}

	if err := writeAgentGitCache(path, map[string]agentGitCacheEntry{}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(data) {
		t.Fatalf("cache write followed symlink: %q, %v", got, err)
	}
}

func TestAgentGitCacheOversizedWritePreservesPreviousFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "agent-git.json")

	want := map[string]agentGitCacheEntry{"/project": {GitBranch: "previous"}}
	if err := writeAgentGitCache(path, want); err != nil {
		t.Fatal(err)
	}

	oversized := map[string]agentGitCacheEntry{"/project": {GitBranch: strings.Repeat("x", maxAgentGitCacheBytes)}}
	if err := writeAgentGitCache(path, oversized); err != nil {
		t.Fatal(err)
	}

	if got := readAgentGitCache(path); !maps.Equal(got, want) {
		t.Fatalf("oversized write replaced prior cache: %#v", got)
	}
}

func TestAgentGitCacheUsesOnlyNativePath(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", directory)

	legacyPath := filepath.Join(directory, "legacy-tool", "agent-git.json")
	if err := writeAgentGitCache(legacyPath, map[string]agentGitCacheEntry{"/project": {GitBranch: "old-tool"}}); err != nil {
		t.Fatal(err)
	}

	if got := agentGitCachePath(); got != filepath.Join(directory, "sesh", "agent-git.json") {
		t.Fatalf("native cache path = %q", got)
	}

	if got := readAgentGitCache(agentGitCachePath()); len(got) != 0 {
		t.Fatalf("native cache read old tool's data: %#v", got)
	}
}

func TestAgentGitCacheDoesNotScanOrDecorateRemoteOrigins(t *testing.T) {
	model := gitTestModel(t)
	t.Cleanup(model.Close)
	model.sessions = []session.Session{
		{Origin: session.OriginSSH, Agents: []session.AgentSession{{CWD: "/shared/path", GitBranch: "ssh-branch"}}},
		{Origin: session.OriginBridge, Agents: []session.AgentSession{{CWD: "/shared/path", GitBranch: "origin-branch"}}},
	}

	model.agentGit = map[string]agentGitCacheEntry{"/shared/path": {GitBranch: "local-branch"}}
	if got := model.agentGitDirectories(); len(got) != 0 {
		t.Fatalf("remote origins included in local Git scan: %#v", got)
	}

	for _, row := range model.sessions {
		want := row.Agents[0].GitBranch
		model.applyCachedAgentGit(&row)

		if row.Agents[0].GitBranch != want {
			t.Fatalf("remote Git branch overwritten by local cache: %+v", row.Agents[0])
		}
	}
}
