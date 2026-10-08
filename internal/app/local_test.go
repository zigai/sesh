//go:build linux || darwin

package app

import (
	"path/filepath"
	"sync"
	"testing"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/action"
	"github.com/zigai/sesh/internal/snapshot/store"
)

func TestConcurrentActionsShareTheRepository(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "snapshots")

	repo, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	repository := NewRepository(path)

	t.Cleanup(func() { _ = repository.Close() })

	local := NewLocal(LocalOptions{
		Config: nil, Server: nil, Repository: repository, LeaseRoot: "", HistoryPath: "",
		AgentSource: nil, Streams: gotmux.Streams{In: nil, Out: nil, Err: nil}, Source: "test", DeferSwitchHistory: false,
	})

	var group sync.WaitGroup

	for range 2 {
		group.Go(func() {
			if _, err := local.ResolveSubject(t.Context(), action.SubjectSavedSession, "", []string{"missing"}); err == nil {
				t.Error("ResolveSubject found a session in an empty repository")
			}
		})
	}

	group.Wait()
}
