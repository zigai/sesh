package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

const DefaultStopGrace = 5 * time.Second

var (
	ErrSocketRequired = errors.New("an explicit absolute tmux socket is required")
	ErrStopGrace      = errors.New("tmux supervisor stop grace must be positive")
	ErrPrepare        = errors.New("prepare foreground tmux server")
	ErrStart          = errors.New("start foreground tmux server")
	ErrWait           = errors.New("wait for foreground tmux server")
	ErrStopIncomplete = errors.New("foreground tmux server did not stop within its grace period")
	ErrSupervisorUsed = errors.New("foreground process supervisor is single-use")
)

type Process interface {
	Start() error
	Wait() error
	Signal(signal os.Signal) error
	Kill() error
}

type PrepareProcess func(ctx context.Context) (Process, error)

type commandProcess struct{ command *exec.Cmd }

type Supervisor struct {
	prepare   PrepareProcess
	stopGrace time.Duration
	mu        sync.Mutex
	used      bool
}

func (p *commandProcess) Start() error {
	if err := p.command.Start(); err != nil {
		return oops.In("daemon").Code("supervisor_failed").With("operation", "start_process").Wrap(err)
	}

	return nil
}

func (p *commandProcess) Wait() error {
	if err := p.command.Wait(); err != nil {
		return oops.In("daemon").Code("supervisor_failed").With("operation", "wait_process").Wrap(err)
	}

	return nil
}

func (p *commandProcess) Signal(signal os.Signal) error {
	if err := p.command.Process.Signal(signal); err != nil {
		return oops.In("daemon").Code("supervisor_failed").With("operation", "signal_process").Wrap(err)
	}

	return nil
}

func (p *commandProcess) Kill() error {
	if err := p.command.Process.Kill(); err != nil {
		return oops.In("daemon").Code("supervisor_failed").With("operation", "kill_process").Wrap(err)
	}

	return nil
}

func NewSupervisor(prepare PrepareProcess, stopGrace time.Duration) (*Supervisor, error) {
	if prepare == nil {
		return nil, oops.In("daemon").Code("supervisor_failed").With("operation", "new_supervisor").Wrap(ErrPrepare)
	}

	if stopGrace <= 0 {
		return nil, oops.In("daemon").Code("supervisor_failed").With("operation", "new_supervisor").Wrap(ErrStopGrace)
	}

	return &Supervisor{prepare: prepare, stopGrace: stopGrace, mu: sync.Mutex{}, used: false}, nil
}

func NewTmuxSupervisor(binary, socket, configFile string, stopGrace time.Duration) (*Supervisor, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return nil, oops.In("daemon").Code("supervisor_failed").With("operation", "new_tmux_supervisor").Wrap(ErrSocketRequired)
	}

	if stopGrace <= 0 {
		return nil, oops.In("daemon").Code("supervisor_failed").With("operation", "new_tmux_supervisor").Wrap(ErrStopGrace)
	}

	server, err := gotmux.New(gotmux.Config{
		Binary:     binary,
		SocketPath: socket,
		SocketName: "",
		ConfigFile: configFile,
		Env:        nil,
		Dir:        "",
		Limits: gotmux.Limits{
			CommandTimeout: 0,
			OutputBytes:    0,
			InputBytes:     0,
			Concurrent:     0,
		},
		UTF8:             gotmux.UTF8Default,
		Colors256:        false,
		TerminalFeatures: nil,
		LogLevel:         gotmux.LogLevelNone,
		LoginShell:       false,
	})
	if err != nil {
		return nil, oops.In("daemon").Code("supervisor_failed").With("operation", "new_tmux_supervisor").Wrap(err)
	}

	return NewSupervisor(func(ctx context.Context) (Process, error) {
		command, err := server.PrepareForegroundServer(context.WithoutCancel(ctx))
		if err != nil {
			return nil, oops.In("daemon").Code("supervisor_failed").With("operation", "prepare_tmux_server").Wrap(err)
		}

		return &commandProcess{command: command}, nil
	}, stopGrace)
}

func (s *Supervisor) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.used {
		s.mu.Unlock()

		return oops.In("daemon").Code("supervisor_failed").With("operation", "run_supervisor").Wrap(ErrSupervisorUsed)
	}

	s.used = true
	s.mu.Unlock()

	if err := context.Cause(ctx); err != nil {
		return oops.In("daemon").Code("supervisor_failed").With("operation", "run_supervisor").Wrap(err)
	}

	process, err := s.prepare(ctx)
	if err != nil {
		return oops.In("daemon").Code("supervisor_failed").With("operation", "run_supervisor").Wrap(errors.Join(ErrPrepare, err))
	}

	if err := context.Cause(ctx); err != nil {
		return oops.In("daemon").Code("supervisor_failed").With("operation", "run_supervisor").Wrap(err)
	}

	if err := process.Start(); err != nil {
		return oops.In("daemon").Code("supervisor_failed").With("operation", "start").Wrap(errors.Join(ErrStart, err))
	}

	waited := make(chan error, 1)
	go func() { waited <- process.Wait() }()

	select {
	case waitErr := <-waited:
		return classifyWait(waitErr, false)
	case <-ctx.Done():
	}

	signalErr := process.Signal(syscall.SIGTERM)

	timer := time.NewTimer(s.stopGrace)
	defer timer.Stop()

	select {
	case waitErr := <-waited:
		if err := errors.Join(normalizeSignalError(signalErr), classifyWait(waitErr, true)); err != nil {
			return oops.In("daemon").Code("supervisor_failed").With("operation", "stop").Wrap(err)
		}

		return nil
	case <-timer.C:
		killErr := process.Kill()
		waitErr := <-waited

		err := errors.Join(normalizeSignalError(signalErr), killErr, classifyWait(waitErr, true), ErrStopIncomplete)

		return oops.In("daemon").Code("supervisor_failed").With("operation", "stop").Wrap(err)
	}
}

func classifyWait(err error, stopped bool) error {
	if err == nil || (stopped && isSignalExit(err)) {
		return nil
	}

	return oops.In("daemon").Code("supervisor_failed").With("operation", "wait").Wrap(errors.Join(ErrWait, err))
}

func normalizeSignalError(err error) error {
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}

	return err
}

func isSignalExit(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}

	status, ok := exitErr.Sys().(syscall.WaitStatus)

	return ok && status.Signaled()
}
