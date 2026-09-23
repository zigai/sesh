package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	gitScanBudget    = time.Second
	gitCommandBudget = 200 * time.Millisecond
	gitOutputLimit   = 64 * 1024
	statFieldsCount  = 2
	gitWaitDelay     = 50 * time.Millisecond
)

type agentGitCacheEntry struct {
	GitBranch        string `json:"gitBranch"`
	Worktree         string `json:"worktree"`
	GitChanges       string `json:"gitChanges"`
	LinkedWorktree   bool   `json:"linkedWorktree"`
	GitAhead         int    `json:"gitAhead"`
	GitBehind        int    `json:"gitBehind"`
	GitTrackingKnown bool   `json:"gitTrackingKnown"`
}

type gitTracking struct {
	ahead  int
	behind int
	known  bool
}

type gitScanner func(ctx context.Context, directories []string) map[string]agentGitCacheEntry

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

func scanGitMetadata(ctx context.Context, directories []string) map[string]agentGitCacheEntry {
	ctx, cancel := context.WithTimeout(ctx, gitScanBudget)
	defer cancel()

	result := make(map[string]agentGitCacheEntry, len(directories))

	for _, directory := range directories {
		if ctx.Err() != nil {
			return result
		}

		if !filepath.IsAbs(directory) {
			continue
		}

		if _, seen := result[directory]; seen {
			continue
		}

		result[directory] = readGitMetadata(ctx, directory)
	}

	return result
}

func readGitMetadata(ctx context.Context, directory string) agentGitCacheEntry {
	commandCtx, cancel := context.WithTimeout(ctx, gitCommandBudget)
	defer cancel()

	locations, ok := gitMetadataOutput(commandCtx, directory, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")

	paths := strings.Split(locations, "\n")
	if !ok || len(paths) != 3 {
		return emptyAgentGitCacheEntry()
	}

	branch, _ := gitMetadataOutput(commandCtx, directory, "symbolic-ref", "--quiet", "--short", "HEAD")
	tracking := readGitTracking(commandCtx, paths[0])

	return agentGitCacheEntry{
		GitBranch:        branch,
		Worktree:         paths[0],
		LinkedWorktree:   paths[1] != paths[2],
		GitChanges:       readGitChanges(commandCtx, paths[0]),
		GitAhead:         tracking.ahead,
		GitBehind:        tracking.behind,
		GitTrackingKnown: tracking.known,
	}
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
	added, deleted := "0", "0"

	for part := range strings.SplitSeq(output, ",") {
		fields := strings.Fields(part)
		if len(fields) != statFieldsCount {
			continue
		}

		count, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return ""
		}

		switch fields[1] {
		case "insertion(+)", "insertions(+)":
			added = strconv.FormatUint(count, 10)
		case "deletion(-)", "deletions(-)":
			deleted = strconv.FormatUint(count, 10)
		}
	}

	return "+" + added + " -" + deleted
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
