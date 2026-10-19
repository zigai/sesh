package task

import (
	"slices"

	"github.com/zigai/aht/v2/pkg/harness"
	"github.com/zigai/aht/v2/pkg/registry"

	"github.com/zigai/sesh/internal/config"
)

type Harness struct {
	Name       string
	Command    []string
	PromptArgs []string
}

func Harnesses(cfg config.TasksConfig, lookPath func(string) (string, error)) []Harness {
	if len(cfg.Harness) == 0 {
		offered := make([]Harness, 0)

		for _, id := range harness.Supported() {
			if command, found := installed(id, lookPath); found && config.ValidHarnessName(string(id)) {
				offered = append(offered, Harness{Name: string(id), Command: command, PromptArgs: nil})
			}
		}

		return offered
	}

	offered := make([]Harness, 0, len(cfg.Harness))

	for _, configured := range cfg.Harness {
		command := slices.Clone(configured.Command)
		if len(command) == 0 {
			resolved, found := installed(registry.Harness(configured.Name), lookPath)
			if !found {
				continue
			}

			command = resolved
		}

		offered = append(offered, Harness{Name: configured.Name, Command: command, PromptArgs: slices.Clone(configured.PromptArgs)})
	}

	return offered
}

func Find(harnesses []Harness, name string) (Harness, bool) {
	index := slices.IndexFunc(harnesses, func(h Harness) bool { return h.Name == name })
	if index < 0 {
		return Harness{Name: "", Command: nil, PromptArgs: nil}, false
	}

	return harnesses[index], true
}

func installed(id registry.Harness, lookPath func(string) (string, error)) ([]string, bool) {
	for _, executable := range harness.ProcessNames(id) {
		if _, err := lookPath(executable); err == nil {
			return []string{executable}, true
		}
	}

	return nil, false
}
