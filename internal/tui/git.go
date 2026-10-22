package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/git"
)

const (
	gitCommandBudget   = 200 * time.Millisecond
	gitOutputLimit     = 64 * 1024
	gitWaitDelay       = 50 * time.Millisecond
	gitStatusCodeWidth = 2
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
	dirty     bool
	conflicts int
}

type gitScanner func(ctx context.Context, directories []string) (map[string]agentGitCacheEntry, error)

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

func scanGitMetadata(ctx context.Context, directories []string) (map[string]agentGitCacheEntry, error) {
	result := make(map[string]agentGitCacheEntry, len(directories))

	var scanErr error

	for _, directory := range directories {
		if ctx.Err() != nil {
			return result, errors.Join(scanErr, ctx.Err())
		}

		if !filepath.IsAbs(directory) {
			continue
		}

		if _, seen := result[directory]; seen {
			continue
		}

		entry, err := readGitMetadata(ctx, directory)
		if err != nil {
			scanErr = errors.Join(scanErr, err)

			continue
		}

		result[directory] = entry
	}

	return result, scanErr
}

func readGitMetadata(ctx context.Context, directory string) (agentGitCacheEntry, error) {
	commandCtx, cancel := context.WithTimeout(ctx, gitCommandBudget)
	defer cancel()

	failure := oops.In("tui").Code("git_scan_failed").With("operation", "scan_git_metadata", "directory", directory)

	locations, ok := gitMetadataOutput(commandCtx, directory, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")

	paths := strings.Split(locations, "\n")
	if !ok || len(paths) != 3 {
		return emptyAgentGitCacheEntry(), failure.Wrap(commandCtx.Err())
	}

	branch, _ := gitMetadataOutput(commandCtx, directory, "symbolic-ref", "--quiet", "--short", "HEAD")
	tracking := readGitTracking(commandCtx, paths[0])
	status := readGitStatus(commandCtx, paths[0])
	remotes, remotesOK := gitMetadataOutput(commandCtx, paths[0], "remote")

	return agentGitCacheEntry{
		GitBranch:        branch,
		Worktree:         paths[0],
		GitRepo:          paths[2],
		GitHasRemote:     remotesOK && remotes != "",
		LinkedWorktree:   paths[1] != paths[2],
		GitChanges:       readGitChanges(commandCtx, paths[0]),
		GitAhead:         tracking.ahead,
		GitBehind:        tracking.behind,
		GitTrackingKnown: tracking.known,
		GitStash:         readGitStashCount(commandCtx, paths[0]),
		GitConflicts:     status.conflicts,
		GitDirty:         status.dirty,
		GitUnmerged:      readGitUnmerged(commandCtx, paths[0]),
	}, failure.Wrap(commandCtx.Err())
}

func readGitUnmerged(ctx context.Context, directory string) int {
	base, ok := gitMetadataOutput(ctx, directory, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if !ok || base == "" {
		return 0
	}

	output, ok := gitMetadataOutput(ctx, directory, "rev-list", "--count", base+"..HEAD", "--")

	count, err := strconv.Atoi(output)
	if !ok || err != nil || count < 0 {
		return 0
	}

	return count
}

func readGitStatus(ctx context.Context, directory string) gitStatus {
	output, ok := gitMetadataOutput(ctx, directory, "status", "--porcelain=v1", "--untracked-files=normal")
	if !ok {
		return gitStatus{dirty: false, conflicts: 0}
	}

	return parseGitStatus(output)
}

func parseGitStatus(output string) gitStatus {
	status := gitStatus{dirty: output != "", conflicts: 0}
	for line := range strings.SplitSeq(output, "\n") {
		if len(line) < gitStatusCodeWidth {
			continue
		}

		code := line[:gitStatusCodeWidth]
		if code == "UU" || code == "AA" || code == "DD" || code == "AU" || code == "UA" || code == "DU" || code == "UD" {
			status.conflicts++
		}
	}

	return status
}

func readGitStashCount(ctx context.Context, directory string) int {
	output, ok := gitMetadataOutput(ctx, directory, "stash", "list", "--format=%gd")
	if !ok || output == "" {
		return 0
	}

	return strings.Count(output, "\n") + 1
}

func readGitTracking(ctx context.Context, directory string) gitTracking {
	output, ok := gitMetadataOutput(ctx, directory, "rev-list", "--left-right", "--count", "HEAD...@{upstream}", "--")

	values := strings.Fields(output)
	if !ok || len(values) != 2 {
		return emptyGitTracking()
	}

	ahead, aheadErr := strconv.Atoi(values[0])

	behind, behindErr := strconv.Atoi(values[1])
	if aheadErr != nil || behindErr != nil || ahead < 0 || behind < 0 {
		return emptyGitTracking()
	}

	return gitTracking{ahead: ahead, behind: behind, known: true}
}

func readGitChanges(ctx context.Context, directory string) string {
	output, ok := gitMetadataOutput(ctx, directory, "diff", "--no-ext-diff", "--no-textconv", "--shortstat", "HEAD", "--")
	if !ok {
		return ""
	}

	return formatGitChanges(output)
}

func formatGitChanges(output string) string {
	diff, ok := git.ParseShortStat(output)
	if !ok {
		return ""
	}

	return "+" + strconv.Itoa(diff.Added) + " -" + strconv.Itoa(diff.Deleted)
}

func gitMetadataOutput(ctx context.Context, directory string, args ...string) (string, bool) {
	command := exec.CommandContext(ctx, "git", args...)
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
		return "", false
	}

	return strings.TrimSpace(output.String()), true
}

func emptyAgentGitCacheEntry() agentGitCacheEntry {
	var entry agentGitCacheEntry

	return entry
}

func emptyGitTracking() gitTracking {
	var tracking gitTracking

	return tracking
}
