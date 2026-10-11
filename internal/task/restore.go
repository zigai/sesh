package task

import (
	"cmp"
	"context"
	"slices"
	"strconv"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/snapshot/apps"
	"github.com/zigai/sesh/internal/snapshot/model"
)

// RestoreLaunches decides how task agents come back when a snapshot is restored, following
// tasks.wake_on_restore: "previous" resumes the agents that were awake, "none" brings every task
// back asleep, and a count resumes only that many of the most recently created awake tasks. The
// others come back behind the sleeping placeholder, and once the restore commits, their records
// turn sleeping with the captured native resume command. Agents that were asleep stay asleep
// because their placeholder is captured like any other restartable application.
type RestoreLaunches struct {
	store      Store
	policy     string
	executable string
	restored   map[string]restoredTask
}

type restoredTask struct {
	task   string
	state  State
	resume Resume
}

type restoredAgent struct {
	pane   model.Pane
	record Record
}

// NewRestoreLaunches returns the launch overrides for a restore; executable is the sesh binary
// the placeholder runs.
func NewRestoreLaunches(store Store, policy, executable string) *RestoreLaunches {
	return &RestoreLaunches{store: store, policy: policy, executable: executable, restored: nil}
}

// Override replaces the native resume launch of each task agent that wake_on_restore keeps
// asleep with the placeholder launch. It decides from the snapshot and the tasks' creation
// times only, so a resumed restore decides the same way.
func (r *RestoreLaunches) Override(ctx context.Context, manifest model.Manifest) (map[string]model.Launch, error) {
	r.restored = make(map[string]restoredTask)

	launches := make(map[string]model.Launch)
	if !slices.ContainsFunc(manifest.Windows, func(window model.Window) bool {
		_, found := ownOption(window.Options, OptionTask)

		return found
	}) {
		return launches, nil
	}

	ledger, err := r.store.Load(ctx)
	if err != nil {
		return nil, taskError("restore_tasks", err)
	}

	var awake []restoredAgent

	for _, agent := range capturedAgents(manifest, ledger) {
		if agent.pane.Launch.Recovery == model.RecoveryResume {
			awake = append(awake, agent)
			r.restored[agent.pane.ID] = restoredTask{task: agent.record.ID, state: StateActive, resume: agent.record.Resume}
		} else {
			r.restored[agent.pane.ID] = restoredTask{task: agent.record.ID, state: StateSleeping, resume: agent.record.Resume}
		}
	}

	slices.SortFunc(awake, func(a, b restoredAgent) int {
		return cmp.Or(cmp.Compare(b.record.CreatedAt, a.record.CreatedAt), cmp.Compare(b.record.ID, a.record.ID))
	})

	keep := len(awake)
	if r.policy != config.WakeOnRestorePrevious {
		keep, _ = strconv.Atoi(r.policy)
	}

	for _, agent := range awake[min(keep, len(awake)):] {
		launch := agent.pane.Launch
		harness, _ := apps.LaunchHarness(launch)
		command := launchCommand(launch)

		launches[agent.pane.ID] = apps.PlaceholderLaunch(r.executable, agent.record.ID, launch)
		r.restored[agent.pane.ID] = restoredTask{
			task: agent.record.ID, state: StateSleeping,
			resume: Resume{Harness: harness, Session: command[len(command)-1], Command: command},
		}
	}

	return launches, nil
}

// Commit records each restored task's captured agent state and the resume command its
// placeholder wakes it with. A task finished since is left alone.
func (r *RestoreLaunches) Commit(ctx context.Context, panes []string) error {
	if !slices.ContainsFunc(panes, func(pane string) bool {
		_, found := r.restored[pane]

		return found
	}) {
		return nil
	}

	_, err := r.store.Update(ctx, func(ledger *Ledger) error {
		for _, pane := range panes {
			restored, ok := r.restored[pane]
			if !ok {
				continue
			}

			record, found := ledger.Find(restored.task)
			if !found {
				continue
			}

			record.State, record.Resume = restored.state, restored.resume
			ledger.Put(record)
		}

		return nil
	})

	return taskError("restore_tasks", err)
}

// capturedAgents lists the task agent panes captured with a native resume launch or a sleeping
// placeholder that belong to a task the ledger knows.
func capturedAgents(manifest model.Manifest, ledger Ledger) []restoredAgent {
	windowTasks := make(map[string]string)

	for _, window := range manifest.Windows {
		if id, ok := ownOption(window.Options, OptionTask); ok {
			windowTasks[window.ID] = id
		}
	}

	var captured []restoredAgent

	for _, pane := range manifest.Panes {
		id, inTask := windowTasks[pane.Window]
		if role, _ := ownOption(pane.Options, OptionRole); !inTask || role != RoleAgent {
			continue
		}

		if len(pane.Launch.Arguments) == 0 {
			continue
		}

		if !capturedTaskLaunch(pane.Launch, id) {
			continue
		}

		if record, found := ledger.Find(id); found {
			captured = append(captured, restoredAgent{pane: pane, record: record})
		}
	}

	return captured
}

func capturedTaskLaunch(launch model.Launch, id string) bool {
	switch launch.Recovery {
	case model.RecoveryResume:
		_, ok := apps.LaunchHarness(launch)

		return ok
	case model.RecoveryRestart:
		_, reason := apps.Prepare(launch, nil)

		return reason == apps.ReasonTaskPlaceholder && string(launch.Arguments[len(launch.Arguments)-1]) == id
	case model.RecoveryUnverified, model.RecoveryNonrestartable:
		return false
	default:
		return false
	}
}

func ownOption(options []model.Option, name string) (string, bool) {
	for _, option := range options {
		if string(option.Name) == name && option.Present && !option.Inherited {
			return string(option.Value), true
		}
	}

	return "", false
}
