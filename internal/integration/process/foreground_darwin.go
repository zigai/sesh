//go:build darwin

package process

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type kinfoForegroundSource struct{}

func observeForegroundPlatform(ctx context.Context, req ForegroundRequest) (*Observation, error) {
	return observeForeground(ctx, kinfoForegroundSource{}, req)
}

func (kinfoForegroundSource) readIdentity(ctx context.Context, pid int) (Identity, error) {
	return readDarwinIdentity(ctx, pid)
}

func (kinfoForegroundSource) readProcess(ctx context.Context, pid int) (foregroundProcess, error) {
	info, err := readDarwinKinfo(ctx, pid)
	if err != nil {
		return foregroundProcess{}, err
	}

	return darwinForegroundProcess(&info)
}

func (kinfoForegroundSource) readDescendants(
	ctx context.Context,
	pane foregroundProcess,
) (map[int]foregroundProcess, error) {
	infos, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}

	children := darwinChildren(infos)
	processes := map[int]foregroundProcess{pane.pid: pane}
	queue := []int{pane.pid}

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("foreground traversal canceled: %w", err)
		}

		pid := queue[0]
		queue = queue[1:]

		queue, err = appendDarwinDescendants(infos, children[pid], processes, queue)
		if err != nil {
			return nil, err
		}
	}

	return processes, nil
}

func (kinfoForegroundSource) observe(ctx context.Context, req Request) (*Observation, error) {
	return observePlatform(ctx, req)
}

func darwinForegroundProcess(info *unix.KinfoProc) (foregroundProcess, error) {
	pid := int(info.Proc.P_pid)

	startTime, err := darwinStartTime(info.Proc.P_starttime.Sec, int64(info.Proc.P_starttime.Usec))
	if err != nil {
		return foregroundProcess{}, err
	}

	session, err := unix.Getsid(pid)
	if err != nil {
		return foregroundProcess{}, classifyDarwinProcessError(err)
	}

	return foregroundProcess{
		pid:             pid,
		parent:          int(info.Eproc.Ppid),
		group:           int(info.Eproc.Pgid),
		session:         session,
		tty:             int(info.Eproc.Tdev),
		foregroundGroup: int(info.Eproc.Tpgid),
		startTime:       startTime,
	}, nil
}

func darwinChildren(infos []unix.KinfoProc) map[int][]int {
	children := make(map[int][]int)

	for index := range infos {
		if infos[index].Proc.P_stat == darwinZombieState {
			continue
		}

		parent := int(infos[index].Eproc.Ppid)
		children[parent] = append(children[parent], index)
	}

	return children
}

func appendDarwinDescendants(
	infos []unix.KinfoProc,
	children []int,
	processes map[int]foregroundProcess,
	queue []int,
) ([]int, error) {
	for _, index := range children {
		childPID := int(infos[index].Proc.P_pid)
		if _, seen := processes[childPID]; seen {
			continue
		}

		if len(processes) >= maxForegroundProcesses {
			return nil, ErrInputTooLarge
		}

		child, err := darwinForegroundProcess(&infos[index])
		if errors.Is(err, ErrProcessVanished) {
			continue
		}

		if err != nil {
			return nil, err
		}

		processes[childPID] = child
		queue = append(queue, childPID)
	}

	return queue, nil
}
