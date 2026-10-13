package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zigai/aht/v2/pkg/harness"
	"github.com/zigai/aht/v2/pkg/registry"
)

const (
	HarnessTab = "tab"

	DefaultPopupSocket = "sesh-popup"
)

var (
	errProjectPathInvalid   = errors.New("must be an absolute path or start with ~/")
	errOverrideKey          = errors.New("must be a project directory name, an absolute path, or start with ~/")
	errPopupSocketInvalid   = errors.New("tasks.popup_socket must be a tmux socket name without slashes or spaces")
	errPopupConfigInvalid   = errors.New("tasks.popup_config must be empty, an absolute path, or start with ~/")
	errHarnessNameInvalid   = errors.New("harness names may only contain a-z, 0-9, _ and -")
	errHarnessNameReserved  = errors.New("harness name is reserved")
	errHarnessNameDuplicate = errors.New("duplicate harness name")
	errHarnessCommandEmpty  = errors.New("harness needs a command unless it names a harness AHT supports")
)

// ProjectsConfig lists where projects are found. Roots are scanned one level deep for git
// repositories; extra registers folders that are projects without git.
type ProjectsConfig struct {
	Roots []string `toml:"roots"`
	Extra []string `toml:"extra"`
	// Overrides holds per-project settings, keyed by the project's directory name.
	Overrides map[string]ProjectConfig `toml:"overrides"`
}

// TasksConfig configures task windows: the panes beside the agent and their layouts, the tmux
// server that hosts task popups, and the harnesses offered when a task starts. Harness entries
// are the offer, in order; without them, every supported harness installed on the host is
// offered.
type TasksConfig struct {
	Panes       []TaskPaneConfig   `toml:"panes"`
	Layouts     []TaskLayoutConfig `toml:"layouts"`
	PopupSocket string             `toml:"popup_socket"`
	PopupConfig string             `toml:"popup_config"`
	Harness     []HarnessConfig    `toml:"harness"`
	// WakeOnRestore selects which task agents are woken after a snapshot restore.
	WakeOnRestore string `toml:"wake_on_restore"`
	// ShowFooter shows key hints at the bottom of the new task popup.
	ShowFooter bool `toml:"show_footer"`
}

// HarnessConfig is a harness offered for new tasks. Command may be left out for a harness AHT
// supports; it then runs that harness's installed executable. PromptArgs are appended to the
// command when a task is started with a title, with task variables such as {title} filled in.
type HarnessConfig struct {
	Name       string   `toml:"name"`
	Command    []string `toml:"command"`
	PromptArgs []string `toml:"prompt_args"`
}

func DefaultProjectsConfig() ProjectsConfig {
	return ProjectsConfig{Roots: []string{"~/Projects"}, Extra: nil, Overrides: nil}
}

func DefaultTasksConfig() TasksConfig {
	return TasksConfig{
		Panes:   []TaskPaneConfig{{Name: "editor", Command: []string{"nvim"}, Dir: "", Restart: false}},
		Layouts: nil, PopupSocket: DefaultPopupSocket, PopupConfig: "", Harness: nil,
		WakeOnRestore: WakeOnRestorePrevious, ShowFooter: true,
	}
}

func (p ProjectsConfig) Clone() ProjectsConfig {
	return ProjectsConfig{Roots: slices.Clone(p.Roots), Extra: slices.Clone(p.Extra), Overrides: cloneProjects(p.Overrides)}
}

func (t TasksConfig) Clone() TasksConfig {
	harnesses := make([]HarnessConfig, 0, len(t.Harness))
	for _, entry := range t.Harness {
		harnesses = append(harnesses, HarnessConfig{Name: entry.Name, Command: slices.Clone(entry.Command), PromptArgs: slices.Clone(entry.PromptArgs)})
	}

	if t.Harness == nil {
		harnesses = nil
	}

	return TasksConfig{
		Panes: cloneTaskPanes(t.Panes), Layouts: cloneTaskLayouts(t.Layouts), PopupSocket: t.PopupSocket, PopupConfig: t.PopupConfig, Harness: harnesses,
		WakeOnRestore: t.WakeOnRestore, ShowFooter: t.ShowFooter,
	}
}

func ValidateProjectsConfig(projects ProjectsConfig) error {
	for _, list := range []struct {
		name  string
		paths []string
	}{{"projects.roots", projects.Roots}, {"projects.extra", projects.Extra}} {
		for _, path := range list.paths {
			if !configPath(path) {
				return fmt.Errorf("invalid %s: %w", list.name, errProjectPathInvalid)
			}
		}
	}

	for key := range projects.Overrides {
		if strings.Contains(key, "/") && !projectPathKey(key) || containsControlCharacter(key) {
			return fmt.Errorf("invalid projects.overrides key %q: %w", key, errOverrideKey)
		}
	}

	return nil
}

func ValidateTasksConfig(tasks TasksConfig) error {
	socket := tasks.PopupSocket
	if socket == "" || strings.ContainsAny(socket, "/ \t\n") || containsControlCharacter(socket) {
		return errPopupSocketInvalid
	}

	if tasks.PopupConfig != "" && !configPath(tasks.PopupConfig) {
		return errPopupConfigInvalid
	}

	if err := ValidateWakeOnRestore(tasks.WakeOnRestore); err != nil {
		return err
	}

	seen := make(map[string]bool, len(tasks.Harness))

	for _, entry := range tasks.Harness {
		if err := validateHarness(entry); err != nil {
			return fmt.Errorf("invalid tasks.harness %q: %w", entry.Name, err)
		}

		if seen[entry.Name] {
			return fmt.Errorf("invalid tasks.harness %q: %w", entry.Name, errHarnessNameDuplicate)
		}

		seen[entry.Name] = true
	}

	return nil
}

// ValidHarnessName reports whether name can label a harness in menus, commands and task records.
func ValidHarnessName(name string) bool {
	if name == "" {
		return false
	}

	for _, character := range name {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}

	return true
}

// SupportedHarness reports whether name is a harness AHT supports.
func SupportedHarness(name string) bool {
	return slices.ContainsFunc(harness.Supported(), func(id registry.Harness) bool { return string(id) == name })
}

func validateHarness(entry HarnessConfig) error {
	if !ValidHarnessName(entry.Name) {
		return errHarnessNameInvalid
	}

	if entry.Name == HarnessTab {
		return errHarnessNameReserved
	}

	if len(entry.Command) == 0 && !SupportedHarness(entry.Name) {
		return errHarnessCommandEmpty
	}

	if len(entry.Command) > 0 && strings.TrimSpace(entry.Command[0]) == "" {
		return errHarnessCommandEmpty
	}

	return nil
}

func configPath(path string) bool {
	if containsControlCharacter(path) {
		return false
	}

	return filepath.IsAbs(path) || path == "~" || strings.HasPrefix(path, "~/")
}
