package work

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/task"
)

var (
	syncedAt  = time.Unix(1_800_000_000, 0)
	startedAt = syncedAt.Add(-time.Hour).Unix()
)

func linkedItem(prs ...PRLink) Item {
	item := EmptyItem()
	item.ID = "w1"
	item.Conversation = "c1"
	item.Resume = []string{"claude", "--resume", "c1"}
	item.Directory = "/code/shop"
	item.Branch = "fix"
	item.CreatedAt = startedAt
	item.PRs = append(item.PRs, prs...)

	return item
}

func prOpenedBeforeStart(number int, state task.PRState, source LinkSource) PRLink {
	link := PRLink{Number: number, State: state, URL: "", Issues: []int{}, Source: source, CreatedAt: startedAt - 60, EndedAt: 0}
	if link.Ended() {
		link.EndedAt = startedAt + 60
	}

	return link
}

func synced(item Item, found ...PRLink) Item {
	state := State{Version: Version, LastID: 1, Items: []Item{item}}
	state.apply(map[string]refresh{item.ID: {directory: item.Directory, gone: false, branch: item.Branch, found: found}}, syncedAt)

	return state.Items[0]
}

func TestAnOpenPullRequestThatMergesFinishesTheItem(t *testing.T) {
	for _, ended := range []task.PRState{task.PRMerged, task.PRClosed} {
		item := synced(linkedItem(prOpenedBeforeStart(4, task.PROpen, LinkBranch)), prOpenedBeforeStart(4, ended, ""))

		if !item.Finished() || item.FinishedBy != FinishedByPR || item.PRs[0].Source != LinkBranch {
			t.Errorf("after the pull request was %s, item = %+v, want it finished by its pull request", ended, item)
		}
	}
}

func TestABranchPullRequestOpenedAndMergedBetweenSyncsFinishesTheItem(t *testing.T) {
	item := synced(linkedItem(), prOpenedBeforeStart(4, task.PRMerged, ""))

	if !item.Finished() || item.FinishedBy != FinishedByPR || len(item.Linked()) != 1 || item.PRs[0].Source != LinkBranch {
		t.Errorf("item = %+v, want the merged pull request linked and the item finished by it", item)
	}
}

func TestABranchPullRequestThatEndedBeforeTheConversationIsNotLinked(t *testing.T) {
	old := prOpenedBeforeStart(4, task.PRMerged, "")
	old.CreatedAt, old.EndedAt = startedAt-7200, startedAt-3600

	if item := synced(linkedItem(), old); item.Finished() || len(item.PRs) != 0 {
		t.Errorf("item = %+v, want the earlier pull request unlinked and the item open", item)
	}
}

func TestABranchPullRequestOpenedAfterTheLastTurnIsNotLinked(t *testing.T) {
	quiet := linkedItem()
	quiet.TurnAt = startedAt + 60

	later := prOpenedBeforeStart(4, task.PROpen, "")
	later.CreatedAt = startedAt + 600

	if item := synced(quiet, later); len(item.PRs) != 0 {
		t.Errorf("item = %+v, want a pull request opened after the conversation went quiet unlinked", item)
	}
}

func TestALinkedPullRequestOutsideTheConversationIsStillRefreshed(t *testing.T) {
	linked := prOpenedBeforeStart(4, task.PROpen, LinkUser)
	linked.CreatedAt = startedAt - 7200

	quiet := linkedItem(linked)
	quiet.TurnAt = startedAt - 3600

	refreshed := prOpenedBeforeStart(4, task.PROpen, "")
	refreshed.CreatedAt, refreshed.URL = startedAt-7200, "https://example.test/pull/4"

	if item := synced(quiet, refreshed); item.PRs[0].URL != refreshed.URL || item.PRs[0].Source != LinkUser {
		t.Errorf("item = %+v, want #4 refreshed and still linked by the user", item)
	}
}

func TestAPullRequestThatEndedBeforeTheLatestPromptDoesNotFinishTheItem(t *testing.T) {
	prompted := linkedItem(prOpenedBeforeStart(4, task.PROpen, LinkBranch))
	prompted.PromptAt = startedAt + 120

	if item := synced(prompted, prOpenedBeforeStart(4, task.PRMerged, "")); item.Finished() || item.PRs[0].State != task.PRMerged {
		t.Errorf("item = %+v, want the pull request merged and the item open after a later prompt", item)
	}
}

func TestAResumedItemWhosePullRequestEndedBeforeIsNotFinishedAgain(t *testing.T) {
	state := State{Version: Version, LastID: 1, Items: []Item{linkedItem(prOpenedBeforeStart(4, task.PRMerged, LinkBranch))}}
	state.Items[0].FinishedAt, state.Items[0].FinishedBy = startedAt+90, FinishedByPR

	if err := state.Reopen("w1", time.Unix(startedAt+600, 0)); err != nil {
		t.Fatalf("Reopen() error = %v", err)
	}

	if item := synced(state.Items[0], prOpenedBeforeStart(4, task.PRMerged, "")); item.Finished() {
		t.Errorf("item = %+v, want it open after it was resumed", item)
	}
}

func TestATaskItemIsNeitherLinkedNorFinishedBySync(t *testing.T) {
	inTask := linkedItem(prOpenedBeforeStart(4, task.PROpen, LinkUser))
	inTask.Task = "t3"

	if item := synced(inTask, prOpenedBeforeStart(4, task.PRMerged, ""), prOpenedBeforeStart(7, task.PRMerged, "")); item.Finished() || len(item.PRs) != 1 || item.PRs[0].Ended() {
		t.Errorf("item = %+v, want the task's item left to the task ledger", item)
	}
}

func TestAPinnedItemStaysOpenWhenItsPullRequestEnds(t *testing.T) {
	pinned := linkedItem(prOpenedBeforeStart(4, task.PROpen, LinkBranch))
	pinned.PinnedAt = syncedAt.Unix()

	if item := synced(pinned, prOpenedBeforeStart(4, task.PRMerged, "")); item.Finished() {
		t.Errorf("item = %+v, want a pinned item open", item)
	}
}

func TestAnItemWithAnOpenLinkedPullRequestStaysOpen(t *testing.T) {
	item := synced(linkedItem(prOpenedBeforeStart(4, task.PROpen, LinkBranch), prOpenedBeforeStart(9, task.PROpen, LinkUser)), prOpenedBeforeStart(4, task.PRMerged, ""), prOpenedBeforeStart(9, task.PROpen, ""))

	if item.Finished() {
		t.Errorf("item = %+v, want it open while #9 is open", item)
	}
}

func TestAnUnlinkedPullRequestIsNotLinkedAgainOrCounted(t *testing.T) {
	item := synced(linkedItem(prOpenedBeforeStart(4, task.PROpen, LinkDismissed), prOpenedBeforeStart(9, task.PROpen, LinkUser)), prOpenedBeforeStart(4, task.PROpen, ""), prOpenedBeforeStart(9, task.PRMerged, ""))

	if item.PRs[0].Source != LinkDismissed || !item.Finished() {
		t.Errorf("item = %+v, want #4 still unlinked and the item finished by #9", item)
	}
}

func TestABranchChangeKeepsTheEarlierPullRequestLinked(t *testing.T) {
	state := State{Version: Version, LastID: 1, Items: []Item{linkedItem(prOpenedBeforeStart(4, task.PROpen, LinkBranch))}}
	state.apply(map[string]refresh{"w1": {directory: "/code/shop", gone: false, branch: "next", found: []PRLink{prOpenedBeforeStart(7, task.PROpen, "")}}}, syncedAt)

	if linked := state.Items[0].Linked(); len(linked) != 2 || state.Items[0].Branch != "next" {
		t.Errorf("item = %+v, want #4 and #7 linked on branch next", state.Items[0])
	}
}

func TestGitHubPullRequestTimesBecomeTheLinkLifetime(t *testing.T) {
	const listed = `[
		{"number": 1, "state": "MERGED", "createdAt": "2027-01-15T08:00:00Z", "mergedAt": "2027-01-15T10:00:00Z", "closedAt": "2027-01-15T10:00:00Z"},
		{"number": 2, "state": "CLOSED", "createdAt": "2027-01-15T08:00:00Z", "mergedAt": null, "closedAt": "2027-01-15T09:00:00Z"},
		{"number": 3, "state": "OPEN", "createdAt": "2027-01-15T08:00:00Z", "mergedAt": null, "closedAt": null}
	]`

	var decoded []ghPR
	if err := json.Unmarshal([]byte(listed), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}

	opened := time.Date(2027, 1, 15, 8, 0, 0, 0, time.UTC).Unix()
	want := [][2]int64{{opened, opened + 7200}, {opened, opened + 3600}, {opened, 0}}

	for index, pr := range decoded {
		if link := pr.link(); link.CreatedAt != want[index][0] || link.EndedAt != want[index][1] {
			t.Errorf("#%d lifetime = %d..%d, want %d..%d", pr.Number, link.CreatedAt, link.EndedAt, want[index][0], want[index][1])
		}
	}
}

func finishedAt(id string, at time.Time, dir string) Item {
	item := linkedItem()
	item.ID, item.Conversation, item.Directory = id, id, dir
	item.FinishedAt, item.FinishedBy = at.Unix(), FinishedByUser

	return item
}

func TestSyncDropsItemsFinishedLongerThanTheRetentionAgo(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "state", "work.json"))
	now := time.Now()

	open := linkedItem()
	open.ID, open.Directory, open.CreatedAt, open.Task = "w3", dir, now.Add(-2*finishedRetention).Unix(), "t1"

	items := []Item{finishedAt("w1", now.Add(-finishedRetention-time.Hour), dir), finishedAt("w2", now.Add(-finishedRetention+time.Hour), dir), open}

	if _, err := store.Update(t.Context(), func(state *State) error {
		state.Items = items

		return nil
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	changed, err := Sync(t.Context(), store)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	state, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	kept := make([]string, 0, len(state.Items))
	for _, item := range state.Items {
		kept = append(kept, item.ID)
	}

	if !changed || !reflect.DeepEqual(kept, []string{"w2", "w3"}) {
		t.Errorf("Sync() changed = %t, kept %v, want true and [w2 w3]", changed, kept)
	}
}

func logRecords(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()

	var records []map[string]any

	for line := range strings.Lines(logs.String()) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}

		records = append(records, record)
	}

	return records
}

func runSyncTicks(t *testing.T, store Store, count int) []map[string]any {
	t.Helper()

	var logs bytes.Buffer

	ctx, cancel := context.WithCancel(zerolog.New(&logs).WithContext(t.Context()))

	ticks := make(chan time.Time)
	done := make(chan struct{})

	go func() {
		syncOnTicks(ctx, store, ticks)
		close(done)
	}()

	for range count {
		ticks <- time.Now()
	}

	// The loop takes the next tick only after the previous sync, so this send waits for the last one.
	ticks <- time.Now()

	cancel()
	<-done

	return logRecords(t, &logs)
}

func TestRepeatedSyncFailuresLogOneDegradedRecord(t *testing.T) {
	records := runSyncTicks(t, NewStore("relative/private/work.json"), 3)

	want := map[string]any{
		"level": "warn", "component": "work", "operation": "sync_work", "status": "degraded", "reason": "work_sync_failed",
		"message": "periodic work sync failed",
	}
	if len(records) != 1 || !reflect.DeepEqual(records[0], want) {
		t.Errorf("records = %#v, want only %#v", records, want)
	}
}

func TestSuccessfulSyncsLogNothing(t *testing.T) {
	if records := runSyncTicks(t, NewStore(filepath.Join(t.TempDir(), "state", "work.json")), 2); len(records) != 0 {
		t.Errorf("records = %#v, want none", records)
	}
}
