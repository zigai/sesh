package work

import (
	"testing"
	"time"

	"github.com/zigai/sesh/internal/task"
)

var syncedAt = time.Unix(1_800_000_000, 0)

func linkedItem(prs ...PRLink) Item {
	item := EmptyItem()
	item.ID = "w1"
	item.Conversation = "c1"
	item.Resume = []string{"claude", "--resume", "c1"}
	item.Directory = "/code/shop"
	item.Branch = "fix"
	item.PRs = prs

	return item
}

func pr(number int, state task.PRState, source string) PRLink {
	return PRLink{Number: number, State: state, URL: "", Issues: []int{}, Source: source}
}

func synced(item Item, found ...PRLink) Item {
	state := State{Version: Version, LastID: 1, Items: []Item{item}}
	state.apply(map[string]refresh{item.ID: {directory: item.Directory, gone: false, branch: item.Branch, found: found}}, syncedAt)

	return state.Items[0]
}

func TestAnOpenPullRequestThatMergesFinishesTheItem(t *testing.T) {
	for _, ended := range []task.PRState{task.PRMerged, task.PRClosed} {
		item := synced(linkedItem(pr(4, task.PROpen, LinkBranch)), pr(4, ended, ""))

		if !item.Finished() || item.FinishedBy != FinishedByPR || item.PRs[0].Source != LinkBranch {
			t.Errorf("after the pull request was %s, item = %+v, want it finished by its pull request", ended, item)
		}
	}
}

func TestAPullRequestFirstSeenEndedDoesNotFinishTheItem(t *testing.T) {
	item := synced(linkedItem(), pr(4, task.PRMerged, ""))

	if item.Finished() || len(item.Linked()) != 1 || item.PRs[0].Source != LinkBranch {
		t.Errorf("item = %+v, want the merged pull request linked and the item open", item)
	}
}

func TestAPinnedItemStaysOpenWhenItsPullRequestEnds(t *testing.T) {
	pinned := linkedItem(pr(4, task.PROpen, LinkBranch))
	pinned.PinnedAt = syncedAt.Unix()

	if item := synced(pinned, pr(4, task.PRMerged, "")); item.Finished() {
		t.Errorf("item = %+v, want a pinned item open", item)
	}
}

func TestAnItemWithAnOpenLinkedPullRequestStaysOpen(t *testing.T) {
	item := synced(linkedItem(pr(4, task.PROpen, LinkBranch), pr(9, task.PROpen, LinkUser)), pr(4, task.PRMerged, ""), pr(9, task.PROpen, ""))

	if item.Finished() {
		t.Errorf("item = %+v, want it open while #9 is open", item)
	}
}

func TestAnUnlinkedPullRequestIsNotLinkedAgainOrCounted(t *testing.T) {
	item := synced(linkedItem(pr(4, task.PROpen, LinkDismissed), pr(9, task.PROpen, LinkUser)), pr(4, task.PROpen, ""), pr(9, task.PRMerged, ""))

	if item.PRs[0].Source != LinkDismissed || !item.Finished() {
		t.Errorf("item = %+v, want #4 still unlinked and the item finished by #9", item)
	}
}

func TestABranchChangeKeepsTheEarlierPullRequestLinked(t *testing.T) {
	state := State{Version: Version, LastID: 1, Items: []Item{linkedItem(pr(4, task.PROpen, LinkBranch))}}
	state.apply(map[string]refresh{"w1": {directory: "/code/shop", gone: false, branch: "next", found: []PRLink{pr(7, task.PROpen, "")}}}, syncedAt)

	if linked := state.Items[0].Linked(); len(linked) != 2 || state.Items[0].Branch != "next" {
		t.Errorf("item = %+v, want #4 and #7 linked on branch next", state.Items[0])
	}
}
