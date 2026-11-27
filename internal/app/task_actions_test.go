//go:build linux || darwin

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/task"
)

func localWithProjectTasks(t *testing.T, projects ...string) (*Local, task.Store) {
	t.Helper()

	server, err := gotmux.New(gotmux.Config{SocketPath: filepath.Join(t.TempDir(), "tmux.sock")})
	if err != nil {
		t.Fatal(err)
	}

	ledger := filepath.Join(t.TempDir(), "state", "tasks.json")
	store := task.NewStore(ledger)

	if _, err := store.Update(t.Context(), func(ledger *task.Ledger) error {
		for _, project := range projects {
			record := task.EmptyRecord()
			record.ID, record.Kind, record.State = ledger.NextID(), task.KindProject, task.StateActive
			record.Project, record.Directory, record.Branch, record.Base = project, project, "fix", "main"
			ledger.Put(record)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return NewLocal(LocalOptions{Server: server, TaskLedger: ledger}), store
}

func projectDir(t *testing.T, name string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestSyncTasksRefreshesLaterTasksAfterOneRepositoryFailsOnGitHub(t *testing.T) {
	local, store := localWithProjectTasks(
		t,
		projectDir(t, "local-only"),
		projectDir(t, "hosted"),
	)

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(`#!/bin/sh
case "$PWD" in */local-only) echo 'no git remotes found' >&2; exit 1 ;; esac
printf '%s' '[{"number":9,"state":"OPEN","url":"https://example.test/pull/9","isCrossRepository":false}]'
`), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	changed, err := local.SyncTasks(t.Context(), nil)
	if err != nil {
		t.Fatalf("SyncTasks() error = %v", err)
	}

	ledger, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	got := []task.PullRequest{ledger.Tasks[0].PR, ledger.Tasks[1].PR}
	want := []task.PullRequest{
		{Number: 0, State: "", URL: ""},
		{Number: 9, State: task.PROpen, URL: "https://example.test/pull/9"},
	}

	if !changed || got[0] != want[0] || got[1] != want[1] {
		t.Errorf(
			"SyncTasks() changed = %t, pull requests %+v, want true and %+v",
			changed,
			got,
			want,
		)
	}
}

func TestAMissingGHStopsTaskPullRequestRefreshes(t *testing.T) {
	local, store := localWithProjectTasks(t, projectDir(t, "first"), projectDir(t, "second"))

	t.Setenv("PATH", t.TempDir())

	service, err := local.taskService()
	if err != nil {
		t.Fatal(err)
	}

	ledger, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	refreshes := 0

	refreshTaskPRs(
		t.Context(),
		ledger.Tasks,
		func(ctx context.Context, id string) (task.Record, bool, error) {
			refreshes++

			return service.RefreshPR(ctx, id)
		},
	)

	if refreshes != 1 {
		t.Errorf("pull request refreshes = %d, want 1 when gh is not installed", refreshes)
	}
}
