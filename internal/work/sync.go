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
	"strings"

	"github.com/zigai/sesh/internal/git"
	"github.com/zigai/sesh/internal/task"
)

const ghOutputLimit = 1 << 20

var errGitHub = errors.New("gh could not list pull requests")

type refresh struct {
	directory string
	gone      bool
	branch    string
	pr        task.PullRequest
	issues    []int
	prKnown   bool
}

// Sync brings the work items up to date: it drops the items whose directory is gone, and records
// each other item's branch and the pull request gh finds for it, with the issues that pull request
// closes. Items whose pull request ended are left as they are, and without gh so are pull
// requests. It reports whether anything changed.
func Sync(ctx context.Context, store Store) (bool, error) {
	state, err := store.Load(ctx)
	if err != nil {
		return false, err
	}

	refreshed := make(map[string]refresh, len(state.Items))
	findPRs := true

	for _, item := range state.Items {
		if item.PREnded() {
			continue
		}

		update := refresh{directory: item.Directory, gone: false, branch: "", pr: item.PR, issues: item.Issues, prKnown: false}
		if _, err := os.Stat(item.Directory); errors.Is(err, os.ErrNotExist) {
			update.gone = true
			refreshed[item.ID] = update

			continue
		}

		update.branch = branchOf(ctx, item.Directory)
		if findPRs && update.branch != "" && update.branch != defaultBranch(ctx, item.Directory) {
			update.pr, update.issues, err = findPR(ctx, item.Directory, update.branch)
			update.prKnown = err == nil
			findPRs = err == nil
		}

		refreshed[item.ID] = update
	}

	changed := false

	_, err = store.Update(ctx, func(state *State) error {
		changed = state.apply(refreshed)

		return nil
	})

	return changed, err
}

func (s *State) apply(refreshed map[string]refresh) bool {
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
		if item.Branch != update.branch {
			item.Branch = update.branch
			item.PR, item.Issues = task.PullRequest{Number: 0, State: "", URL: ""}, []int{}
		}

		if update.prKnown {
			item.PR, item.Issues = update.pr, update.issues
		}

		changed = changed || !same(item, before)
		kept = append(kept, item)
	}

	s.Items = kept

	return changed
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

// findPR returns the pull request gh lists for branch, from this repository rather than a fork,
// and the issues it closes. No pull request is a zero one.
func findPR(ctx context.Context, dir, branch string) (task.PullRequest, []int, error) {
	none := task.PullRequest{Number: 0, State: "", URL: ""}

	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return none, nil, errors.Join(errGitHub, err)
	}

	command := exec.CommandContext(ctx, ghPath, "pr", "list", "--head", branch, "--state", "all",
		"--json", "number,state,url,isCrossRepository,closingIssuesReferences")
	command.Dir = dir

	command.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1")

	output := git.NewLimitedBuffer(ghOutputLimit)

	command.Stdout = output
	if err := command.Run(); err != nil {
		return none, nil, errors.Join(errGitHub, err)
	}

	var listed []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
		URL    string `json:"url"`
		Fork   bool   `json:"isCrossRepository"`
		Closes []struct {
			Number int `json:"number"`
		} `json:"closingIssuesReferences"`
	}

	if err := json.Unmarshal(output.Bytes(), &listed); err != nil {
		return none, nil, errors.Join(errGitHub, fmt.Errorf("decode gh output: %w", err))
	}

	for _, pr := range listed {
		if pr.Fork || pr.Number <= 0 {
			continue
		}

		issues := make([]int, 0, len(pr.Closes))
		for _, closes := range pr.Closes {
			issues = append(issues, closes.Number)
		}

		slices.Sort(issues)

		return task.PullRequest{Number: pr.Number, State: task.PRState(strings.ToLower(pr.State)), URL: pr.URL}, issues, nil
	}

	return none, []int{}, nil
}
