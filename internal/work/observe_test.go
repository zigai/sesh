package work_test

import (
	"errors"
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

func finishedState(t *testing.T) work.State {
	t.Helper()

	state := emptyState()
	state.Observe([]work.Sighting{sighting("c1", "%1", 100)}, observedAt, branchMain)

	if err := state.Finish("w1", work.FinishedByUser, observedAt); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	return state
}

func TestAFinishedConversationSeenInAnotherPaneReopens(t *testing.T) {
	state := finishedState(t)
	state.Observe([]work.Sighting{sighting("c1", "%7", 300)}, observedAt, branchMain)

	if state.Items[0].Finished() {
		t.Errorf("item = %+v, want it reopened", state.Items[0])
	}
}

func TestAFinishedConversationStillInItsPaneStaysFinished(t *testing.T) {
	state := finishedState(t)

	later := sighting("c1", "%1", 100)
	later.TurnAt = observedAt.Add(time.Minute)
	state.Observe([]work.Sighting{later}, observedAt, branchMain)

	if !state.Items[0].Finished() {
		t.Errorf("item = %+v, want it still finished", state.Items[0])
	}
}

func TestANewConversationInAFinishedItemsPaneStartsANewItem(t *testing.T) {
	state := finishedState(t)
	state.Observe([]work.Sighting{sighting("c2", "%1", 100)}, observedAt, branchMain)

	if len(state.Items) != 2 || state.Items[0].Conversation != "c1" || state.Items[1].Finished() {
		t.Errorf("Items = %+v, want c1 finished and c2 a new open item", state.Items)
	}
}

func TestObserveRecordsTheTaskAConversationRunsIn(t *testing.T) {
	state := emptyState()

	inTask := sighting("c1", "%1", 100)
	inTask.Task = "t3"
	state.Observe([]work.Sighting{inTask}, observedAt, branchMain)

	if state.Items[0].Task != "t3" {
		t.Errorf("Task = %q, want t3", state.Items[0].Task)
	}
}

func TestUnlinkingAPullRequestKeepsItOutOfTheLinkedOnes(t *testing.T) {
	state := emptyState()
	state.Observe([]work.Sighting{sighting("c1", "%1", 100)}, observedAt, branchMain)

	if err := state.Link("w1", work.PRLink{Number: 4, State: "open", URL: "", Issues: []int{2}, Source: ""}); err != nil {
		t.Fatalf("Link() error = %v", err)
	}

	if err := state.Unlink("w1", 4); err != nil {
		t.Fatalf("Unlink() error = %v", err)
	}

	if item := state.Items[0]; len(item.Linked()) != 0 || len(item.PRs) != 1 {
		t.Errorf("item = %+v, want the pull request remembered but not linked", item)
	}

	if err := state.Unlink("w1", 4); !errors.Is(err, work.ErrNotLinked) {
		t.Errorf("Unlink() again error = %v, want ErrNotLinked", err)
	}
}

func TestObserveKeepsTheLatestPrompt(t *testing.T) {
	state := emptyState()

	prompted := sighting("c1", "%1", 100)
	prompted.PromptAt = observedAt.Add(time.Hour)
	state.Observe([]work.Sighting{prompted}, observedAt, branchMain)

	earlier := sighting("c1", "%1", 100)
	earlier.PromptAt = observedAt

	if state.Observe([]work.Sighting{sighting("c1", "%1", 100), earlier}, observedAt, branchMain) ||
		state.Items[0].PromptAt != prompted.PromptAt.Unix() {
		t.Errorf("Items = %+v, want the later prompt kept unchanged", state.Items)
	}
}

func TestAFinishedConversationPromptedAgainInItsPaneReopens(t *testing.T) {
	state := finishedState(t)

	prompted := sighting("c1", "%1", 100)
	prompted.PromptAt = observedAt.Add(time.Minute)
	state.Observe([]work.Sighting{prompted}, observedAt, branchMain)

	if state.Items[0].Finished() || state.Items[0].PromptAt != prompted.PromptAt.Unix() {
		t.Errorf("item = %+v, want it reopened with the new prompt", state.Items[0])
	}
}

func TestAFinishedConversationWithAPromptFromBeforeItFinishedStaysFinished(t *testing.T) {
	state := finishedState(t)

	prompted := sighting("c1", "%1", 100)
	prompted.PromptAt = observedAt.Add(-time.Minute)
	state.Observe([]work.Sighting{prompted}, observedAt, branchMain)

	if !state.Items[0].Finished() {
		t.Errorf("item = %+v, want it still finished", state.Items[0])
	}
}

func taskState(t *testing.T) work.State {
	t.Helper()

	state := emptyState()

	for index, conversation := range []string{"c1", "c2", "c3"} {
		seen := sighting(conversation, "%"+conversation, 100+index)
		seen.Task, seen.Directory = "t1", "/trees/shop-t1"

		if conversation == "c3" {
			seen.Task, seen.Directory = "t2", "/trees/shop-t2"
		}

		state.Observe([]work.Sighting{seen}, observedAt, branchMain)
	}

	if err := state.Finish("w2", work.FinishedByUser, observedAt); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	return state
}

func TestFinishingATaskFinishesItsConversationsInTheMainCheckout(t *testing.T) {
	state := taskState(t)
	state.FinishTask("t1", work.FinishedByPR, "/code/shop", observedAt.Add(time.Hour))

	open, earlier, other := state.Items[0], state.Items[1], state.Items[2]

	if open.FinishedBy != work.FinishedByPR || open.FinishedAt != observedAt.Add(time.Hour).Unix() || open.Task != "" ||
		open.Directory != "/code/shop" {
		t.Errorf("open item = %+v, want it finished by pr, detached and in /code/shop", open)
	}

	if earlier.FinishedBy != work.FinishedByUser || earlier.FinishedAt != observedAt.Unix() || earlier.Task != "" ||
		earlier.Directory != "/code/shop" {
		t.Errorf("earlier finished item = %+v, want its finish kept, detached and in /code/shop", earlier)
	}

	if other.Finished() || other.Task != "t2" || other.Directory != "/trees/shop-t2" {
		t.Errorf("item of another task = %+v, want it unchanged", other)
	}
}

func TestFinishingATaskWithoutADirectoryKeepsWhereItsConversationsRan(t *testing.T) {
	state := taskState(t)
	state.FinishTask("t1", work.FinishedByUser, "", observedAt)

	if item := state.Items[0]; item.Directory != "/trees/shop-t1" || item.Task != "" || !item.Finished() {
		t.Errorf("item = %+v, want it finished and detached in /trees/shop-t1", item)
	}
}
