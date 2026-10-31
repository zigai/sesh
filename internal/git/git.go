package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	waitDelay   = time.Second
	outputLimit = 1 << 20
	detailLimit = 512
)

var ErrOutputTooLarge = errors.New("command output exceeds its limit")

type LimitedBuffer struct {
	data     bytes.Buffer
	limit    int
	exceeded bool
}

type Diff struct {
	Added   int
	Deleted int
}

type CommandError struct {
	Subcommand string
	ExitCode   int
	// Detail is git's last fatal or error line, meant for the user who asked for the operation; it is
	// never logged.
	Detail string
}

func (e *CommandError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("git %s failed with exit code %d", e.Subcommand, e.ExitCode)
	}

	return fmt.Sprintf("git %s failed: %s", e.Subcommand, e.Detail)
}

// Output drops inherited GIT_ variables so a caller inside a git hook cannot redirect the command
// to another repository.
func Output(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.WaitDelay = waitDelay

	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "GIT_") {
			command.Env = append(command.Env, variable)
		}
	}

	command.Env = append(command.Env, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")

	stdout := NewLimitedBuffer(outputLimit)
	stderr := NewLimitedBuffer(outputLimit)

	command.Stdout = stdout
	command.Stderr = stderr

	err := command.Run()

	if stdout.exceeded {
		return "", ErrOutputTooLarge
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return "", &CommandError{Subcommand: subcommand(args), ExitCode: exitErr.ExitCode(), Detail: errorDetail(stderr.String())}
	}

	if err != nil {
		return "", fmt.Errorf("run git %s: %w", subcommand(args), err)
	}

	return strings.TrimSpace(stdout.String()), nil
}

func NewLimitedBuffer(limit int) *LimitedBuffer {
	return &LimitedBuffer{data: bytes.Buffer{}, limit: limit, exceeded: false}
}

func (b *LimitedBuffer) Write(data []byte) (int, error) {
	if b.data.Len()+len(data) > b.limit {
		b.exceeded = true

		return 0, ErrOutputTooLarge
	}

	b.data.Write(data)

	return len(data), nil
}

func (b *LimitedBuffer) Bytes() []byte {
	return b.data.Bytes()
}

func (b *LimitedBuffer) String() string {
	return b.data.String()
}

func ParseShortStat(output string) (Diff, bool) {
	var diff Diff

	for part := range strings.SplitSeq(output, ",") {
		countText, counted, found := strings.Cut(strings.TrimSpace(part), " ")
		if !found {
			continue
		}

		count, err := strconv.Atoi(countText)
		if err != nil || count < 0 {
			return Diff{Added: 0, Deleted: 0}, false
		}

		switch counted {
		case "insertion(+)", "insertions(+)":
			diff.Added = count
		case "deletion(-)", "deletions(-)":
			diff.Deleted = count
		}
	}

	return diff, true
}

func subcommand(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}

	return ""
}

func errorDetail(stderr string) string {
	detail := ""

	// git writes progress such as "Preparing worktree" to stderr before the failure it reports.
	for line := range strings.Lines(stderr) {
		line = strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(line, "fatal:"), strings.HasPrefix(line, "error:"):
			detail = line
		case detail == "":
			detail = line
		}
	}

	if len(detail) > detailLimit {
		detail = strings.ToValidUTF8(detail[:detailLimit], "")
	}

	return detail
}
