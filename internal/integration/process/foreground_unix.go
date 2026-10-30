//go:build linux || darwin

package process

import (
	"context"
	"errors"
)

const (
	maxForegroundProcesses = 4096
	foregroundCandidateCap = 2
)

type foregroundProcess struct {
	pid             int
	parent          int
	group           int
	session         int
	tty             int
	foregroundGroup int
	startTime       uint64
}

type foregroundSelection struct {
	pane    foregroundProcess
	process foregroundProcess
}

type foregroundSource interface {
	readIdentity(ctx context.Context, pid int) (Identity, error)
	readProcess(ctx context.Context, pid int) (foregroundProcess, error)
	readDescendants(ctx context.Context, pane foregroundProcess) (map[int]foregroundProcess, error)
	observe(ctx context.Context, req Request) (*Observation, error)
}

func observeForeground(ctx context.Context, source foregroundSource, req ForegroundRequest) (*Observation, error) {
	if err := checkPaneBirth(ctx, source, req); err != nil {
		return nil, err
	}

	before, err := selectForeground(ctx, source, req)
	if err != nil {
		return nil, err
	}

	observation, err := source.observe(ctx, Request{
		PID:             before.process.pid,
		ExpectedBirth:   Identity{BootID: req.ExpectedPaneBirth.BootID, StartTime: before.process.startTime},
		EnvironmentKeys: req.EnvironmentKeys,
	})
	if err != nil {
		return nil, errors.Join(ErrForegroundChanged, err)
	}

	after, err := selectForeground(ctx, source, req)
	if err != nil {
		return nil, errors.Join(ErrForegroundChanged, err)
	}

	if !sameForegroundSelection(before, after) ||
		observation.PID != after.process.pid || observation.Birth.StartTime != after.process.startTime {
		return nil, ErrForegroundChanged
	}

	if err := checkPaneBirth(ctx, source, req); err != nil {
		return nil, err
	}

	return observation, nil
}

func checkPaneBirth(ctx context.Context, source foregroundSource, req ForegroundRequest) error {
	paneBirth, err := source.readIdentity(ctx, req.PanePID)
	if err != nil {
		return err
	}

	if paneBirth != req.ExpectedPaneBirth {
		return ErrIdentityMismatch
	}

	return nil
}

func sameForegroundSelection(first, second foregroundSelection) bool {
	return first == second
}

func selectForeground(ctx context.Context, source foregroundSource, req ForegroundRequest) (foregroundSelection, error) {
	pane, err := source.readProcess(ctx, req.PanePID)
	if err != nil {
		return foregroundSelection{}, err
	}

	if pane.startTime != req.ExpectedPaneBirth.StartTime {
		return foregroundSelection{}, ErrIdentityMismatch
	}

	if pane.foregroundGroup <= 0 {
		return foregroundSelection{}, ErrForegroundUnavailable
	}

	if pane.group == pane.foregroundGroup {
		return foregroundSelection{pane: pane, process: pane}, nil
	}

	processes, err := source.readDescendants(ctx, pane)
	if err != nil {
		return foregroundSelection{}, err
	}

	candidate, count := uniqueForegroundCandidate(processes, pane)
	if count > 1 {
		return foregroundSelection{}, ErrForegroundAmbiguous
	}

	if count == 0 {
		return foregroundSelection{}, ErrForegroundUnavailable
	}

	return foregroundSelection{pane: pane, process: candidate}, nil
}

func uniqueForegroundCandidate(
	processes map[int]foregroundProcess,
	pane foregroundProcess,
) (foregroundProcess, int) {
	candidates := make([]foregroundProcess, 0, foregroundCandidateCap)

	for _, process := range processes {
		if !isForegroundCandidate(process, pane) {
			continue
		}

		parent, parentKnown := processes[process.parent]
		if parentKnown && parent.group == process.group {
			continue
		}

		candidates = append(candidates, process)
		if len(candidates) > 1 {
			return zeroForegroundProcess(), len(candidates)
		}
	}

	if len(candidates) == 0 {
		return zeroForegroundProcess(), 0
	}

	return candidates[0], 1
}

func isForegroundCandidate(process, pane foregroundProcess) bool {
	return process.pid != pane.pid &&
		process.group == pane.foregroundGroup &&
		process.foregroundGroup == pane.foregroundGroup &&
		process.session == pane.session &&
		process.tty == pane.tty
}

func zeroForegroundProcess() foregroundProcess {
	return foregroundProcess{
		pid: 0, parent: 0, group: 0, session: 0, tty: 0, foregroundGroup: 0, startTime: 0,
	}
}
