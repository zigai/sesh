package work

import (
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
	"time"

	"github.com/rs/zerolog"

	"github.com/zigai/sesh/internal/git"
	"github.com/zigai/sesh/internal/task"
)

const (
	ghOutputLimit     = 1 << 20
	ghPRFields        = "number,state,url,isCrossRepository,closingIssuesReferences,createdAt,mergedAt,closedAt"
	finishedRetention = 90 * 24 * time.Hour
)

var (
	errGitHub     = errors.New("gh could not read pull requests")
	errPRMismatch = errors.New("gh reported another pull request")
)

type refresh struct {
	directory string
	gone      bool
	branch    string
	// found are the pull requests gh listed for the branch or viewed again because they are
	// linked and open.
	found []PRLink
}

type ghPR struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Fork   bool   `json:"isCrossRepository"`
	Closes []struct {
		Number int `json:"number"`
	} `json:"closingIssuesReferences"`
	CreatedAt time.Time `json:"createdAt"`
	MergedAt  time.Time `json:"mergedAt"`
	ClosedAt  time.Time `json:"closedAt"`
}

// Sync brings the work items up to date: it drops the items whose directory is gone and those
// finished longer than finishedRetention ago, and for each open item records its branch. Outside
// task windows it also links the pull request gh finds for that branch when it was open during the
// conversation, refreshes the linked pull requests that were open, and finishes an unpinned item
// whose linked pull requests all ended after it started and after its latest prompt. Without gh
// pull requests stay as they are. It reports whether anything changed.
func Sync(ctx context.Context, store Store) (bool, error) {
	state, err := store.Load(ctx)
	if err != nil {
		return false, err
	}

	refreshed := make(map[string]refresh, len(state.Items))
	gh := true

	for _, item := range state.Items {
		update := refresh{directory: item.Directory, gone: false, branch: item.Branch, found: nil}
		if _, err := os.Stat(item.Directory); errors.Is(err, os.ErrNotExist) {
			update.gone = true
			refreshed[item.ID] = update

			continue
		}

		if item.Finished() {
			continue
		}

		update.branch = branchOf(ctx, item.Directory)
		if gh && item.Task == "" {
			update.found, err = findPRs(ctx, item, update.branch)
			gh = err == nil
		}

		refreshed[item.ID] = update
	}

	changed := false

	_, err = store.Update(ctx, func(state *State) error {
		now := time.Now()
		changed = state.apply(refreshed, now)
		changed = state.Prune(now.Add(-finishedRetention)) || changed

		return nil
	})

	return changed, err
}

// SyncEvery runs Sync every interval until ctx ends. It logs a failing sync once, until a sync
// succeeds again.
func SyncEvery(ctx context.Context, store Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	syncOnTicks(ctx, store, ticker.C)
}

// ViewPR returns pull request number of the repository dir is in, as gh reports it.
func ViewPR(ctx context.Context, dir string, number int) (PRLink, error) {
	var viewed ghPR

	if err := runGH(ctx, dir, &viewed, "pr", "view", strconv.Itoa(number), "--json", ghPRFields); err != nil {
		return noPR(), err
	}

	if viewed.Number != number {
		return noPR(), errors.Join(errGitHub, errPRMismatch)
	}

	return viewed.link(), nil
}

func syncOnTicks(ctx context.Context, store Store, ticks <-chan time.Time) {
	failing := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}

		_, err := Sync(ctx, store)
		if ctx.Err() != nil {
			return
		}

		if err != nil && !failing {
			zerolog.Ctx(ctx).Warn().Str("component", "work").Str("operation", "sync_work").
				Str("status", "degraded").Str("reason", "work_sync_failed").Msg("periodic work sync failed")
		}

		failing = err != nil
	}
}

// findPRs lists the pull request of branch, unless it is the default branch, and views the item's
// linked pull requests that were open again.
func findPRs(ctx context.Context, item Item, branch string) ([]PRLink, error) {
	var found []PRLink

	if branch != "" && branch != defaultBranch(ctx, item.Directory) {
		pr, err := branchPR(ctx, item.Directory, branch)
		if err != nil {
			return nil, err
		}

		if pr.Number != 0 {
			found = append(found, pr)
		}
	}

	for _, linked := range item.Linked() {
		if linked.Ended() || slices.ContainsFunc(found, func(pr PRLink) bool { return pr.Number == linked.Number }) {
			continue
		}

		pr, err := ViewPR(ctx, item.Directory, linked.Number)
		if err != nil {
			return nil, err
		}

		found = append(found, pr)
	}

	return found, nil
}

func (s *State) apply(refreshed map[string]refresh, now time.Time) bool {
	changed := false
	kept := s.Items[:0]

	for _, item := range s.Items {
		update, found := refreshed[item.ID]
		if !found || update.directory != item.Directory {
			kept = append(kept, item)

			continue
		}

		if update.gone {
			changed = true

			continue
		}

		before := item
		item.Branch = update.branch
		item = item.withFound(update.found, now)

		changed = changed || !same(item, before)
		kept = append(kept, item)
	}

	s.Items = kept

	return changed
}

// withFound links and refreshes the pull requests sync found for the item, and finishes the item
// when they ended it. The task ledger owns the pull requests of task items.
func (i Item) withFound(found []PRLink, now time.Time) Item {
	if i.Task != "" {
		return i
	}

	for _, pr := range found {
		if i.knows(pr.Number) || i.overlaps(pr) {
			i = i.withPR(pr)
		}
	}

	if !i.Pinned() && !i.Finished() && i.endedByPRs() {
		i.FinishedAt, i.FinishedBy = now.Unix(), FinishedByPR
	}

	return i
}

func (i Item) knows(number int) bool {
	return slices.ContainsFunc(i.PRs, func(linked PRLink) bool { return linked.Number == number })
}

// overlaps reports whether the pull request was open at some point during the conversation: opened
// no later than its last turn, and still open or ended no earlier than its start.
func (i Item) overlaps(pr PRLink) bool {
	if i.TurnAt != 0 && pr.CreatedAt > i.TurnAt {
		return false
	}

	return !pr.Ended() || pr.EndedAt >= i.CreatedAt
}

// endedByPRs reports whether every linked pull request ended and the last one ended after the
// item started, after the user's latest prompt, and after it last reopened.
func (i Item) endedByPRs() bool {
	if !i.PRsEnded() {
		return false
	}

	var latest int64
	for _, pr := range i.Linked() {
		latest = max(latest, pr.EndedAt)
	}

	return latest >= i.CreatedAt && latest >= i.PromptAt && latest >= i.ReopenedAt
}

// withPR updates what is known about a pull request found for the item, keeping how it was
// linked; a pull request found for the first time is the branch's.
func (i Item) withPR(pr PRLink) Item {
	i.PRs = slices.Clone(i.PRs)

	index := slices.IndexFunc(i.PRs, func(linked PRLink) bool { return linked.Number == pr.Number })
	if index < 0 {
		pr.Source = LinkBranch
		i.PRs = append(i.PRs, pr)

		return i
	}

	pr.Source = i.PRs[index].Source
	i.PRs[index] = pr

	return i
}

func branchOf(ctx context.Context, dir string) string {
	branch, err := git.Output(ctx, dir, "branch", "--show-current")
	if err != nil {
		return ""
	}

	return strings.TrimSpace(branch)
}

// projectOf is the root of the main checkout of the repository dir is in, which a linked worktree
// shares, or empty outside a repository.
func projectOf(ctx context.Context, dir string) string {
	common, err := git.Output(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return ""
	}

	if common = strings.TrimSpace(common); filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}

	top, err := git.Output(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}

	return strings.TrimSpace(top)
}

func defaultBranch(ctx context.Context, dir string) string {
	head, err := git.Output(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return ""
	}

	_, branch, _ := strings.Cut(strings.TrimSpace(head), "/")

	return branch
}

// branchPR returns the pull request gh lists for branch, from this repository rather than a fork.
// No pull request is a zero one.
func branchPR(ctx context.Context, dir, branch string) (PRLink, error) {
	var listed []ghPR

	if err := runGH(ctx, dir, &listed, "pr", "list", "--head", branch, "--state", "all", "--json", ghPRFields); err != nil {
		return noPR(), err
	}

	for _, pr := range listed {
		if !pr.Fork && pr.Number > 0 {
			return pr.link(), nil
		}
	}

	return noPR(), nil
}

func (pr ghPR) link() PRLink {
	issues := make([]int, 0, len(pr.Closes))
	for _, closes := range pr.Closes {
		issues = append(issues, closes.Number)
	}

	slices.Sort(issues)

	link := PRLink{
		Number: pr.Number, State: task.PRState(strings.ToLower(pr.State)), URL: pr.URL, Issues: issues, Source: "",
		CreatedAt: unixOrZero(pr.CreatedAt), EndedAt: 0,
	}

	switch link.State {
	case task.PRMerged:
		link.EndedAt = unixOrZero(pr.MergedAt)
	case task.PRClosed:
		link.EndedAt = unixOrZero(pr.ClosedAt)
	case task.PROpen:
	}

	return link
}

func unixOrZero(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}

	return at.Unix()
}

func noPR() PRLink {
	return PRLink{Number: 0, State: "", URL: "", Issues: []int{}, Source: "", CreatedAt: 0, EndedAt: 0}
}

func runGH(ctx context.Context, dir string, decoded any, args ...string) error {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return errors.Join(errGitHub, err)
	}

	command := exec.CommandContext(ctx, ghPath, args...)
	command.Dir = dir

	command.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1")

	output := git.NewLimitedBuffer(ghOutputLimit)

	command.Stdout = output
	if err := command.Run(); err != nil {
		return errors.Join(errGitHub, err)
	}

	if err := json.Unmarshal(output.Bytes(), decoded); err != nil {
		return errors.Join(errGitHub, fmt.Errorf("decode gh output: %w", err))
	}

	return nil
}
