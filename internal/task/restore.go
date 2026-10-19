package task

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"

	"github.com/zigai/sesh/internal/config"
	"github.com/zigai/sesh/internal/snapshot/apps"
	"github.com/zigai/sesh/internal/snapshot/model"
)

type RestoreLaunches struct {
	store             Store
	policy            config.WakePolicy
	placeholderBinary string
	restored          map[string]restoredTask
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

func NewRestoreLaunches(store Store, policy config.WakePolicy, placeholderBinary string) *RestoreLaunches {
	return &RestoreLaunches{store: store, policy: policy, placeholderBinary: placeholderBinary, restored: nil}
}

// Override puts kept-asleep agents behind the placeholder, reruns restart panes, and gives task
// panes the task's environment. It reads only the snapshot and the records, so a resumed restore
// decides the same way.
func (r *RestoreLaunches) Override(ctx context.Context, manifest model.Manifest) (map[string]model.Launch, error) {
	r.restored = make(map[string]restoredTask)

	launches := restartLaunches(manifest)
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

	for _, agent := range awake[r.policy.Resumes(len(awake)):] {
		launch := agent.pane.Launch
		harness, _ := apps.LaunchHarness(launch)
		command := launch.Command()

		launches[agent.pane.ID] = apps.PlaceholderLaunch(r.placeholderBinary, agent.record.ID, launch)
		r.restored[agent.pane.ID] = restoredTask{
			task: agent.record.ID, state: StateSleeping,
			resume: Resume{Harness: harness, Session: command[len(command)-1], Command: command},
		}
	}

	addTaskEnvironment(manifest, ledger, launches)

	return launches, nil
}

func addTaskEnvironment(manifest model.Manifest, ledger Ledger, launches map[string]model.Launch) {
	records := make(map[string]Record)

	for _, window := range manifest.Windows {
		if id, ok := ownOption(window.Options, OptionTask); ok {
			if record, found := ledger.Find(id); found {
				records[window.ID] = record
			}
		}
	}

	for _, pane := range manifest.Panes {
		record, ok := records[pane.Window]
		if !ok {
			continue
		}

		launch, overridden := launches[pane.ID]
		if !overridden {
			launch = pane.Launch
		}

		if launch.Recovery != model.RecoveryRestart && launch.Recovery != model.RecoveryResume {
			continue
		}

		launches[pane.ID] = withEnvironment(launch, Env(record))
	}
}

func withEnvironment(launch model.Launch, env map[string]string) model.Launch {
	environment := slices.DeleteFunc(slices.Clone(launch.Environment), func(variable model.Variable) bool {
		_, replaced := env[string(variable.Name)]

		return replaced
	})

	for _, name := range slices.Sorted(maps.Keys(env)) {
		environment = append(environment, model.Variable{Name: model.Bytes(name), Value: model.Bytes(env[name]), State: "set", Hidden: false})
	}

	launch.Environment = environment

	return launch
}

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

func restartLaunches(manifest model.Manifest) map[string]model.Launch {
	taskWindows := make(map[string]bool)

	for _, window := range manifest.Windows {
		if _, ok := ownOption(window.Options, OptionTask); ok {
			taskWindows[window.ID] = true
		}
	}

	launches := make(map[string]model.Launch)

	for _, pane := range manifest.Panes {
		value, ok := ownOption(pane.Options, OptionRestart)
		if !taskWindows[pane.Window] || !ok {
			continue
		}

		var command restartCommand
		if err := json.Unmarshal([]byte(value), &command); err != nil || len(command.Argv) == 0 || !filepath.IsAbs(command.Argv[0]) || !filepath.IsAbs(command.Directory) {
			continue
		}

		arguments := make([]model.Bytes, 0, len(command.Argv))
		for _, argument := range command.Argv {
			arguments = append(arguments, model.Bytes(argument))
		}

		launches[pane.ID] = model.Launch{
			Executable: model.Bytes(command.Argv[0]), Arguments: arguments, Directory: model.Bytes(command.Directory),
			Environment: slices.Clone(pane.Launch.Environment), Birth: nil, Recovery: model.RecoveryRestart, Reason: "",
		}
	}

	return launches
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
