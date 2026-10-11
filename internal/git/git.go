// Package git runs git commands for sesh's own project and task bookkeeping.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	waitDelay   = time.Second
	outputLimit = 1 << 20
)

var ErrOutputTooLarge = errors.New("command output exceeds its limit")

// LimitedBuffer collects a command's output up to a limit. A write past the limit fails, which
// stops the command instead of holding unbounded output.
type LimitedBuffer struct {
	data     bytes.Buffer
	limit    int
	exceeded bool
}

// CommandError reports a git command that ran and failed. Detail is git's first error line,
// meant for the user who asked for the operation; it is never logged.
type CommandError struct {
	Subcommand string
	ExitCode   int
	Detail     string
}

func (e *CommandError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("git %s failed with exit code %d", e.Subcommand, e.ExitCode)
	}

	return fmt.Sprintf("git %s failed: %s", e.Subcommand, e.Detail)
}

// Output runs git in dir and returns its trimmed standard output. Inherited GIT_ variables are
// dropped so a caller running inside a git hook cannot redirect the command to another
// repository.
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
		return "", &CommandError{Subcommand: subcommand(args), ExitCode: exitErr.ExitCode(), Detail: firstLine(stderr.String())}
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

func subcommand(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}

	return ""
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")

	return strings.TrimSpace(line)
}
