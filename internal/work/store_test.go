package work_test

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/work"
)

func retainedItem(id string, dir string) work.Item {
	item := work.EmptyItem()
	item.ID, item.Conversation, item.Directory = id, id, dir
	item.Resume = []string{"claude", "--resume", id}

	return item
}

func finishedRetainedItem(id string, dir string, at time.Time) work.Item {
	item := retainedItem(id, dir)
	item.FinishedAtUnix, item.FinishedBy = at.Unix(), work.FinishedByUser

	return item
}

func pinnedItem(item work.Item, at time.Time) work.Item {
	item.PinnedAtUnix = at.Unix()

	return item
}

func itemIDs(items []work.Item) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}

	return ids
}

func TestPruneKeepsPinnedFinishedItemsPastTheCutoff(t *testing.T) {
	cutoff := time.Unix(1_800_000_000, 0)
	state := work.State{
		Version: work.Version,
		LastID:  4,
		Items: []work.Item{
			finishedRetainedItem("w1", "/code/shop", cutoff.Add(-time.Hour)),
			pinnedItem(
				finishedRetainedItem("w2", "/code/shop", cutoff.Add(-time.Hour)),
				cutoff.Add(-2*time.Hour),
			),
			retainedItem("w3", "/code/shop"),
			finishedRetainedItem("w4", "/code/shop", cutoff.Add(time.Hour)),
		},
	}

	changed := state.Prune(cutoff)

	if kept := itemIDs(state.Items); !changed || !slices.Equal(kept, []string{"w2", "w3", "w4"}) {
		t.Errorf("Prune() changed = %t, kept %v, want true and [w2 w3 w4]", changed, kept)
	}
}

func TestSyncKeepsPinnedItemsFinishedLongerThanTheRetentionAgo(t *testing.T) {
	dir := t.TempDir()
	store := work.NewStore(filepath.Join(dir, "state", "work.json"))
	longAgo := time.Now().Add(-91 * 24 * time.Hour)

	if _, err := store.Update(t.Context(), func(state *work.State) error {
		state.LastID = 2
		state.Items = []work.Item{
			finishedRetainedItem("w1", dir, longAgo),
			pinnedItem(finishedRetainedItem("w2", dir, longAgo), longAgo),
		}

		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	changed, err := work.Sync(t.Context(), store)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if kept := itemIDs(state.Items); !changed || !slices.Equal(kept, []string{"w2"}) {
		t.Errorf("Sync() changed = %t, kept %v, want true and [w2]", changed, kept)
	}
}
