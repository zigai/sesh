//go:build linux || darwin

package app

import (
	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/snapshot/engine"
	"github.com/zigai/sesh/internal/task"
)

// TaskRestoreLaunches returns the launch overrides that bring task agents back after a restore
// as tasks.wake_on_restore asks.
func TaskRestoreLaunches(ledger string, cfg config.TasksConfig) (engine.LaunchOverrides, error) {
	executable, err := seshExecutable()
	if err != nil {
		return nil, err
	}

	return task.NewRestoreLaunches(task.NewStore(ledger), cfg.WakeOnRestore, executable), nil
}
