//go:build linux || darwin

package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/samber/oops"
	"github.com/spf13/cobra"

	"github.com/zigai/sesh/internal/cli/actioncli"
	"github.com/zigai/sesh/internal/cli/clibase"
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/tui"
)

const (
	placeholderVerb = "placeholder"

	clearScreen = "\x1b[2J\x1b[H"
	hideCursor  = "\x1b[?25l"
	showCursor  = "\x1b[?25h"
)

func newPlaceholderCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:         placeholderVerb + " <task>",
		Short:       "Hold a sleeping task's agent pane until it is woken",
		Hidden:      true,
		Args:        clibase.UsageArgs(cobra.ExactArgs(1)),
		RunE:        runPlaceholder,
		Annotations: map[string]string{clibase.AnnotationOperation: "task_placeholder", clibase.AnnotationLiveIO: "true"},
	}

	actioncli.AddEnvironmentFlags(cmd)

	return cmd
}

// runPlaceholder shows the sleeping placeholder and, on enter, replaces itself with the agent's
// native resume command, so the woken agent runs in the pane the placeholder held.
func runPlaceholder(cmd *cobra.Command, args []string) error {
	ctx := clibase.CommandContext(cmd)

	cfg, err := clibase.LoadCommandConfig(cmd.Context(), cmd)
	if err != nil {
		return err
	}

	runtime, err := actioncli.OpenRuntime(cmd, cfg)
	if err != nil {
		return err
	}

	harness, err := runtime.Local.TaskHarness(ctx, args[0])
	if err != nil {
		return errors.Join(err, runtime.Close())
	}

	state, err := term.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return errors.Join(oops.In("cli").With("operation", "placeholder_terminal").Wrap(err), runtime.Close())
	}

	command, err := awaitWake(ctx, runtime, cfg, args[0], harness)

	err = errors.Join(err, term.Restore(os.Stdin.Fd(), state), runtime.Close())
	if err != nil || len(command) == 0 {
		return err
	}

	_, _ = fmt.Fprint(os.Stdout, clearScreen+showCursor)

	return execResume(command)
}

// awaitWake redraws the placeholder until enter wakes the task, and returns the command that
// resumes its agent.
func awaitWake(ctx context.Context, runtime *actioncli.Runtime, cfg *config.Config, id, harness string) ([]string, error) {
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)

	defer signal.Stop(resized)

	keys := readKeys(os.Stdin)
	failure := ""

	for {
		drawPlaceholder(cfg, harness, failure)

		select {
		case <-ctx.Done():
			return nil, oops.In("cli").With("operation", "task_placeholder").Wrap(ctx.Err())
		case <-resized:
		case key, open := <-keys:
			if !open {
				return nil, nil
			}

			if key != '\r' && key != '\n' {
				continue
			}

			command, err := runtime.Local.WakeTaskHere(ctx, id)
			if err == nil {
				return command, nil
			}

			failure = "wake failed: " + err.Error()
		}
	}
}

func drawPlaceholder(cfg *config.Config, harness, failure string) {
	width, height, err := term.GetSize(os.Stdout.Fd())
	if err != nil {
		width, height = 0, 0
	}

	_, _ = fmt.Fprint(os.Stdout, hideCursor+clearScreen+tui.SleepPlaceholder(cfg, harness, failure, width, height))
}

func readKeys(input io.Reader) <-chan byte {
	keys := make(chan byte)

	go func() {
		defer close(keys)

		buffer := make([]byte, 1)
		for {
			if _, err := input.Read(buffer); err != nil {
				return
			}

			keys <- buffer[0]
		}
	}()

	return keys
}

func execResume(command []string) error {
	if len(command) == 0 {
		return nil
	}

	path, err := exec.LookPath(command[0])
	if err != nil {
		return oops.In("cli").With("operation", "resume_agent").Wrap(err)
	}

	if err := syscall.Exec(path, command, os.Environ()); err != nil {
		return oops.In("cli").With("operation", "resume_agent").Wrap(err)
	}

	return nil
}
