package fields

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/zigai/sesh/internal/config"
)

const (
	shell          = "/bin/sh"
	stdioPipeCount = 3
)

type pipe struct {
	read, write *os.File
}

type Result struct {
	Text     string
	ExitCode int
	Stderr   string
	Err      error
}

func (r Result) Failed() bool {
	return r.Err != nil
}

func Run(ctx context.Context, command config.FieldCommand, input Context) Result {
	stdin, err := input.Encode()
	if err != nil {
		return Result{Text: "", ExitCode: -1, Stderr: "", Err: err}
	}

	cmd := buildCommand(ctx, command)
	cmd.Dir = input.WorkingDir()
	cmd.Env = mergeEnv(os.Environ(), input.Env())

	var processAttr syscall.SysProcAttr

	processAttr.Setpgid = true
	cmd.SysProcAttr = &processAttr

	stdout, stderr, runErr := runCapturing(ctx, cmd, stdin)

	result := Result{Text: outputText(stdout), ExitCode: 0, Stderr: string(stderr), Err: nil}

	if runErr != nil {
		result.ExitCode = -1
		if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
			result.ExitCode = exitErr.ExitCode()
		}

		result.Err = runErr
	}

	return result
}

func buildCommand(ctx context.Context, command config.FieldCommand) *exec.Cmd {
	if command.ShellCommand != "" {
		return exec.CommandContext(ctx, shell, "-c", command.ShellCommand)
	}

	return exec.CommandContext(ctx, command.Command, command.Args...)
}

func newPipes(count int) ([]pipe, error) {
	pipes := make([]pipe, 0, count)

	for range count {
		read, write, err := os.Pipe()
		if err != nil {
			return nil, errors.Join(fmt.Errorf("create pipe: %w", err), closePipes(pipes))
		}

		pipes = append(pipes, pipe{read: read, write: write})
	}

	return pipes, nil
}

func closePipes(pipes []pipe) error {
	var err error
	for _, p := range pipes {
		err = errors.Join(err, ignoreClosed(p.read.Close()), ignoreClosed(p.write.Close()))
	}

	return err
}

func runCapturing(ctx context.Context, cmd *exec.Cmd, input []byte) ([]byte, []byte, error) {
	pipes, err := newPipes(stdioPipeCount)
	if err != nil {
		return nil, nil, err
	}

	stdin, stdout, stderr := pipes[0], pipes[1], pipes[2]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin.read, stdout.write, stderr.write

	startErr := cmd.Start()

	childEndsErr := errors.Join(stdin.read.Close(), stdout.write.Close(), stderr.write.Close())
	if startErr != nil {
		return nil, nil, errors.Join(fmt.Errorf("start field command: %w", startErr), childEndsErr, closePipes(pipes))
	}

	cleanupDone := make(chan struct{})

	var cleanupErr error

	stopUnblock := context.AfterFunc(ctx, func() {
		cleanupErr = errors.Join(killProcessGroup(cmd.Process.Pid), closePipes(pipes))

		close(cleanupDone)
	})

	var (
		output, errorOutput []byte
		readErr             error
		readErrMu           sync.Mutex
		group               sync.WaitGroup
	)

	collect := func(target *[]byte, reader io.Reader) {
		data, err := io.ReadAll(reader)
		*target = data

		if err = ignoreClosed(err); err != nil {
			readErrMu.Lock()
			readErr = errors.Join(readErr, err)
			readErrMu.Unlock()
		}
	}

	group.Go(func() {
		_, err := stdin.write.Write(input)
		closeErr := stdin.write.Close()

		if err = errors.Join(ignoreClosed(ignoreBrokenPipe(err)), ignoreClosed(closeErr)); err != nil {
			readErrMu.Lock()
			readErr = errors.Join(readErr, err)
			readErrMu.Unlock()
		}
	})
	group.Go(func() { collect(&output, stdout.read) })
	group.Go(func() { collect(&errorOutput, stderr.read) })

	exitErr := waitForFieldExit(cmd.Process.Pid)
	if exitErr != nil {
		if !errors.Is(exitErr, syscall.ECHILD) {
			exitErr = errors.Join(exitErr, killProcessGroup(cmd.Process.Pid))
		}

		exitErr = errors.Join(exitErr, closePipes(pipes))
	}

	group.Wait()

	if !stopUnblock() {
		<-cleanupDone
	}

	waitErr := errors.Join(exitErr, cmd.Wait())

	if ctxErr := ctx.Err(); ctxErr != nil {
		waitErr = errors.Join(ctxErr, waitErr)
	}

	ioErr := errors.Join(childEndsErr, readErr, cleanupErr, closePipes(pipes))
	if waitErr != nil {
		return output, errorOutput, fmt.Errorf("run field command: %w", errors.Join(waitErr, ioErr))
	}

	if ioErr != nil {
		return output, errorOutput, fmt.Errorf("field command output: %w", ioErr)
	}

	return output, errorOutput, nil
}

func ignoreBrokenPipe(err error) error {
	if errors.Is(err, syscall.EPIPE) {
		return nil
	}

	return err
}

func ignoreClosed(err error) error {
	if errors.Is(err, os.ErrClosed) {
		return nil
	}

	return err
}

func killProcessGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill field process group: %w", err)
	}

	return nil
}

func outputText(stdout []byte) string {
	text := strings.TrimRight(string(stdout), "\r\n")
	text = strings.ReplaceAll(text, "\r\n", " ")

	return strings.ReplaceAll(text, "\n", " ")
}
