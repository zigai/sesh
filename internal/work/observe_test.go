package work_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/work"
)

var observedAt = time.Unix(1_800_000_000, 0)

func sighting(conversation, pane string, pid int) work.Sighting {
	return work.Sighting{
		Harness: "claude", Conversation: conversation, SessionPath: "/history/" + conversation + ".jsonl",
		Resume: []string{"claude", "--resume", conversation}, Title: "", Directory: "/code/shop", Project: "/code/shop",
		Server: "/run/tmux/default", Pane: pane, PanePID: pid, TurnAt: observedAt,
	}
}

func branchMain(string) (string, string) { return "main", "/code/shop" }

func emptyState() work.State {
	return work.State{Version: work.Version, LastID: 0, Items: []work.Item{}}
}

func TestObserveStartsOneItemForANewConversation(t *testing.T) {
	state := emptyState()

	if !state.Observe([]work.Sighting{sighting("c1", "%1", 100)}, observedAt, branchMain) {
		t.Fatal("Observe() = false, want a change")
	}

	if len(state.Items) != 1 {
		t.Fatalf("Items = %+v, want one", state.Items)
	}

	item := state.Items[0]
	if item.ID != "w1" || item.Conversation != "c1" || item.Branch != "main" || item.CreatedAt != observedAt.Unix() {
		t.Errorf("item = %+v", item)
	}
}

func TestObserveOfAnUnchangedConversationChangesNothing(t *testing.T) {
	state := emptyState()
	state.Observe([]work.Sighting{sighting("c1", "%1", 100)}, observedAt, branchMain)

	if state.Observe([]work.Sighting{sighting("c1", "%1", 100)}, observedAt.Add(time.Minute), branchMain) {
		t.Errorf("Observe() = true for the same sighting, Items = %+v", state.Items)
	}
}

func TestObserveRecordsALaterTurn(t *testing.T) {
	state := emptyState()
	state.Observe([]work.Sighting{sighting("c1", "%1", 100)}, observedAt, branchMain)

	later := sighting("c1", "%1", 100)
	later.TurnAt = observedAt.Add(time.Hour)

	if !state.Observe([]work.Sighting{later}, observedAt, branchMain) || state.Items[0].TurnAt != later.TurnAt.Unix() {
		t.Errorf("Items = %+v, want the later turn", state.Items)
	}
}

func TestANewConversationInTheSamePaneContinuesTheItem(t *testing.T) {
	state := emptyState()
	state.Observe([]work.Sighting{sighting("c1", "%1", 100)}, observedAt, branchMain)
	state.Observe([]work.Sighting{sighting("c2", "%1", 100)}, observedAt, branchMain)

	if len(state.Items) != 1 || state.Items[0].ID != "w1" || state.Items[0].Conversation != "c2" {
		t.Errorf("Items = %+v, want w1 continued by c2", state.Items)
	}
}

func TestAReusedPaneIDWithAnotherProcessStartsANewItem(t *testing.T) {
	state := emptyState()
	state.Observe([]work.Sighting{sighting("c1", "%1", 100)}, observedAt, branchMain)
	state.Observe([]work.Sighting{sighting("c2", "%1", 200)}, observedAt, branchMain)

	if len(state.Items) != 2 || state.Items[0].Conversation != "c1" || state.Items[1].Conversation != "c2" {
		t.Errorf("Items = %+v, want separate items", state.Items)
	}
}

func TestTwoPanesInOneDirectoryAreTwoItems(t *testing.T) {
	state := emptyState()
	state.Observe([]work.Sighting{sighting("c1", "%1", 100), sighting("c2", "%2", 101)}, observedAt, branchMain)

	if len(state.Items) != 2 {
		t.Errorf("Items = %+v, want two", state.Items)
	}
}

func TestObserveIgnoresAConversationItCannotResume(t *testing.T) {
	state := emptyState()

	noID := sighting("", "%1", 100)
	noResume := sighting("c2", "%2", 101)
	noResume.Resume = nil

	if state.Observe([]work.Sighting{noID, noResume}, observedAt, branchMain) || len(state.Items) != 0 {
		t.Errorf("Items = %+v, want none", state.Items)
	}
}

func TestABlankTitleKeepsTheKnownTitle(t *testing.T) {
	state := emptyState()

	titled := sighting("c1", "%1", 100)
	titled.Title = "Fix the checkout"
	state.Observe([]work.Sighting{titled}, observedAt, branchMain)
	state.Observe([]work.Sighting{sighting("c1", "%1", 100)}, observedAt, branchMain)

	if state.Items[0].Title != "Fix the checkout" {
		t.Errorf("Title = %q", state.Items[0].Title)
	}
}

func TestObservedItemsSurviveTheStore(t *testing.T) {
	store := work.NewStore(filepath.Join(t.TempDir(), "state", "work.json"))

	if _, err := store.Update(t.Context(), func(state *work.State) error {
		state.Observe([]work.Sighting{sighting("c1", "%1", 100)}, observedAt, branchMain)

		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if item, found := state.Find("w1"); !found || item.Conversation != "c1" || item.Resume[2] != "c1" {
		t.Errorf("Find(w1) = %+v, %t", item, found)
	}
}
