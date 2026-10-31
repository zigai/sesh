package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/git"
)

const (
	gitCommandTimeout = 5 * time.Second
	gitProcessLimit   = 8
	gitOutputLimit    = 64 * 1024
	gitWaitDelay      = 50 * time.Millisecond
)

type agentGitCacheEntry struct {
	GitBranch        string `json:"gitBranch"`
	Worktree         string `json:"worktree"`
	GitRepo          string `json:"gitRepo"`
	GitHasRemote     bool   `json:"gitHasRemote"`
	GitChanges       string `json:"gitChanges"`
	LinkedWorktree   bool   `json:"linkedWorktree"`
	GitAhead         int    `json:"gitAhead"`
	GitBehind        int    `json:"gitBehind"`
	GitTrackingKnown bool   `json:"gitTrackingKnown"`
	GitStash         int    `json:"gitStash"`
	GitConflicts     int    `json:"gitConflicts"`
	GitDirty         bool   `json:"gitDirty"`
	GitUnmerged      int    `json:"gitUnmerged"`
}

type gitTracking struct {
	ahead  int
	behind int
	known  bool
}

type gitStatus struct {
	branch    string
	tracking  gitTracking
	dirty     bool
	conflicts int
}

type gitLocation struct {
	worktree  string
	gitDir    string
	commonDir string
}

type gitWorktreeState struct {
	status    gitStatus
	changes   string
	hasRemote bool
	stash     int
	unmerged  int
}

type gitScanner func(ctx context.Context, directories []string) (map[string]agentGitCacheEntry, []string, error)

type gitRunner struct {
	slots chan struct{}
}

type boundedBuffer struct {
	data  []byte
	limit int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if remaining := b.limit - len(b.data); remaining > 0 {
		b.data = append(b.data, data[:min(len(data), remaining)]...)
	}

	return len(data), nil
}

func (b *boundedBuffer) String() string {
	return string(b.data)
}

func scanGitMetadata(ctx context.Context, directories []string) (map[string]agentGitCacheEntry, []string, error) {
	runner := gitRunner{slots: make(chan struct{}, gitProcessLimit)}

	directories = absoluteDirectories(directories)
	locations := make([]gitLocation, len(directories))
	failures := make([]error, len(directories))

	var group sync.WaitGroup
	for index, directory := range directories {
		group.Go(func() { locations[index], failures[index] = runner.locate(ctx, directory) })
	}

	group.Wait()

	worktrees := make([]string, 0, len(locations))
	for _, location := range locations {
		if location.worktree != "" {
			worktrees = append(worktrees, location.worktree)
		}
	}

	slices.Sort(worktrees)
	worktrees = slices.Compact(worktrees)
	states := make([]gitWorktreeState, len(worktrees))
	stateFailures := make([]error, len(worktrees))

	for index, worktree := range worktrees {
		group.Go(func() { states[index], stateFailures[index] = runner.readWorktree(ctx, worktree) })
	}

	group.Wait()

	entries := make(map[string]agentGitCacheEntry, len(directories))
	incomplete := make([]string, 0)

	var cause error

	for index, directory := range directories {
		failure := failures[index]

		if location := locations[index]; location.worktree != "" {
			worktree, _ := slices.BinarySearch(worktrees, location.worktree)
			failure = firstError(failure, stateFailures[worktree])
			entries[directory] = location.entry(states[worktree])
		} else if failure == nil {
			entries[directory] = emptyAgentGitCacheEntry()
		}

		if failure != nil {
			incomplete = append(incomplete, directory)
			cause = firstError(cause, failure)
		}
	}

	if cause != nil {
		return entries, incomplete, oops.In("tui").Code("git_scan_failed").
			With("operation", "scan_git_metadata", "directories", incomplete).Wrap(cause)
	}

	return entries, nil, nil
}

func absoluteDirectories(directories []string) []string {
	absolute := make([]string, 0, len(directories))
	for _, directory := range directories {
		if filepath.IsAbs(directory) {
			absolute = append(absolute, directory)
		}
	}

	slices.Sort(absolute)

	return slices.Compact(absolute)
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

func (location gitLocation) entry(state gitWorktreeState) agentGitCacheEntry {
	return agentGitCacheEntry{
		GitBranch:        state.status.branch,
		Worktree:         location.worktree,
		GitRepo:          location.commonDir,
		GitHasRemote:     state.hasRemote,
		LinkedWorktree:   location.gitDir != location.commonDir,
		GitChanges:       state.changes,
		GitAhead:         state.status.tracking.ahead,
		GitBehind:        state.status.tracking.behind,
		GitTrackingKnown: state.status.tracking.known,
		GitStash:         state.stash,
		GitConflicts:     state.status.conflicts,
		GitDirty:         state.status.dirty,
		GitUnmerged:      state.unmerged,
	}
}

func (r gitRunner) locate(ctx context.Context, directory string) (gitLocation, error) {
	output, ok, err := r.output(ctx, directory, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")

	paths := strings.Split(output, "\n")
	if !ok || len(paths) != 3 {
		var location gitLocation

		return location, err
	}

	return gitLocation{worktree: paths[0], gitDir: paths[1], commonDir: paths[2]}, nil
}

func (r gitRunner) readWorktree(ctx context.Context, worktree string) (gitWorktreeState, error) {
	var (
		state    gitWorktreeState
		failures [5]error
		group    sync.WaitGroup
	)

	group.Go(func() { state.status, failures[0] = r.status(ctx, worktree) })
	group.Go(func() { state.changes, failures[1] = r.changes(ctx, worktree) })
	group.Go(func() { state.hasRemote, failures[2] = r.hasRemote(ctx, worktree) })
	group.Go(func() { state.stash, failures[3] = r.stashCount(ctx, worktree) })
	group.Go(func() { state.unmerged, failures[4] = r.unmerged(ctx, worktree) })
	group.Wait()

	return state, firstError(failures[:]...)
}

func (r gitRunner) status(ctx context.Context, worktree string) (gitStatus, error) {
	output, ok, err := r.output(ctx, worktree, "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if !ok {
		var status gitStatus

		return status, err
	}

	return parseGitStatus(output), nil
}

func parseGitStatus(output string) gitStatus {
	var status gitStatus

	for line := range strings.SplitSeq(output, "\n") {
		header, isHeader := strings.CutPrefix(line, "# ")
		if !isHeader {
			status.dirty = status.dirty || line != ""
			if strings.HasPrefix(line, "u ") {
				status.conflicts++
			}

			continue
		}

		key, value, _ := strings.Cut(header, " ")
		switch key {
		case "branch.head":
			if value != "(detached)" {
				status.branch = value
			}
		case "branch.ab":
			status.tracking = parseGitAheadBehind(value)
		}
	}

	return status
}

func parseGitAheadBehind(value string) gitTracking {
	aheadText, behindText, found := strings.Cut(value, " ")
	ahead, aheadErr := strconv.Atoi(strings.TrimPrefix(aheadText, "+"))

	behind, behindErr := strconv.Atoi(strings.TrimPrefix(behindText, "-"))
	if !found || aheadErr != nil || behindErr != nil || ahead < 0 || behind < 0 {
		var tracking gitTracking

		return tracking
	}

	return gitTracking{ahead: ahead, behind: behind, known: true}
}

func (r gitRunner) changes(ctx context.Context, worktree string) (string, error) {
	output, ok, err := r.output(ctx, worktree, "diff", "--no-ext-diff", "--no-textconv", "--shortstat", "HEAD", "--")
	if !ok {
		return "", err
	}

	return formatGitChanges(output), nil
}

func (r gitRunner) hasRemote(ctx context.Context, worktree string) (bool, error) {
	output, ok, err := r.output(ctx, worktree, "remote")

	return ok && output != "", err
}

func (r gitRunner) stashCount(ctx context.Context, worktree string) (int, error) {
	output, ok, err := r.output(ctx, worktree, "stash", "list", "--format=%gd")
	if !ok || output == "" {
		return 0, err
	}

	return strings.Count(output, "\n") + 1, nil
}

func (r gitRunner) unmerged(ctx context.Context, worktree string) (int, error) {
	output, ok, err := r.output(ctx, worktree, "rev-list", "--count", "refs/remotes/origin/HEAD..HEAD", "--")

	count, parseErr := strconv.Atoi(output)
	if !ok || parseErr != nil || count < 0 {
		return 0, err
	}

	return count, nil
}

func formatGitChanges(output string) string {
	diff, ok := git.ParseShortStat(output)
	if !ok {
		return ""
	}

	return "+" + strconv.Itoa(diff.Added) + " -" + strconv.Itoa(diff.Deleted)
}

func (r gitRunner) output(ctx context.Context, directory string, args ...string) (string, bool, error) {
	if ctx.Err() != nil {
		return "", false, oops.With("command", args[0]).Wrap(ctx.Err())
	}

	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return "", false, oops.With("command", args[0]).Wrap(ctx.Err())
	}

	defer func() { <-r.slots }()

	commandCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()

	command := exec.CommandContext(commandCtx, "git", args...)
	command.Dir = directory

	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "GIT_") {
			command.Env = append(command.Env, variable)
		}
	}

	command.Env = append(command.Env, "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	command.WaitDelay = gitWaitDelay

	output := &boundedBuffer{data: nil, limit: gitOutputLimit}
	command.Stdout = output

	if err := command.Run(); err != nil {
		if commandCtx.Err() != nil {
			return "", false, oops.With("command", args[0]).Wrap(commandCtx.Err())
		}

		return "", false, nil
	}

	return strings.TrimSpace(output.String()), true, nil
}

func emptyAgentGitCacheEntry() agentGitCacheEntry {
	var entry agentGitCacheEntry

	return entry
}
