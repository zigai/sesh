package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	WakeOnRestorePrevious = "previous"
	WakeOnRestoreNone     = "none"
)

var (
	errTaskActionNameInvalid = errors.New("task action names may only contain a-z, 0-9, _ and -")
	errTaskActionRunEmpty    = errors.New("task action needs a run command")
	errTaskActionPrompt      = errors.New("task action takes prompt or prompt_file, not both")
	errTaskActionPromptPath  = errors.New("prompt_file must be absolute, start with ~/, or be relative to the task worktree")
	errTaskActionVariable    = errors.New("unknown template variable")
	errTaskActionKey         = errors.New("task action key is not a picker key")
	errWakeOnRestoreInvalid  = errors.New(`tasks.wake_on_restore must be "previous", "none", or a positive count such as "3"`)

	templateVariable = regexp.MustCompile(`\{[^{}]*\}`)
	// TaskActionVariables are the template variables a task action prompt can use.
	TaskActionVariables = []string{"task", "title", "branch", "base", "project", "worktree"}
)

// TaskActionConfig is a user-defined action on a task: a command run headless in the task's
// worktree, with an optional prompt passed as its last argument.
type TaskActionConfig struct {
	Key        string   `toml:"key"`
	Run        []string `toml:"run"`
	Prompt     string   `toml:"prompt"`
	PromptFile string   `toml:"prompt_file"`
	LinkPR     *bool    `toml:"link_pr"`
}

// ProjectConfig holds settings for one project.
type ProjectConfig struct {
	TaskActions map[string]TaskActionConfig `toml:"task_actions"`
	TaskPanes   []TaskPaneConfig            `toml:"task_panes"`
	TaskLayouts []TaskLayoutConfig          `toml:"task_layouts"`
}

func (a TaskActionConfig) Clone() TaskActionConfig {
	clone := a
	clone.Run = slices.Clone(a.Run)

	if a.LinkPR != nil {
		linkPR := *a.LinkPR
		clone.LinkPR = &linkPR
	}

	return clone
}

// LinksPR reports whether the action looks up the task branch's PR after it runs.
func (a TaskActionConfig) LinksPR() bool {
	return a.LinkPR != nil && *a.LinkPR
}

// TaskActionsFor returns the task actions of the project at root: the global ones with the
// project's overrides applied field by field. An override that sets a prompt or prompt file
// replaces both.
func (c *Config) TaskActionsFor(root string) map[string]TaskActionConfig {
	return c.taskActions(c.ProjectOverride(root))
}

// ProjectOverride returns the overrides of the project at root: those keyed by its path, else
// those keyed by its directory name. A path key tells apart projects with the same name.
func (c *Config) ProjectOverride(root string) ProjectConfig {
	if root == "" {
		return ProjectConfig{TaskActions: nil, TaskPanes: nil, TaskLayouts: nil}
	}

	for key, override := range c.Projects.Overrides {
		if projectPathKey(key) && sameProjectPath(key, root) {
			return override
		}
	}

	return c.Projects.Overrides[filepath.Base(root)]
}

func (c *Config) taskActions(override ProjectConfig) map[string]TaskActionConfig {
	actions := cloneTaskActions(c.TaskActions)
	if actions == nil {
		actions = make(map[string]TaskActionConfig)
	}

	for name, configured := range override.TaskActions {
		actions[name] = mergeTaskAction(actions[name], configured)
	}

	return actions
}

// ValidateTaskActions checks the global task actions and every project's overrides. A project's
// actions are checked after its overrides are applied, so an override may complete an action.
func ValidateTaskActions(cfg *Config) error {
	if err := validateTaskActionSet("task_actions", cfg.TaskActions, true); err != nil {
		return err
	}

	for _, project := range slices.Sorted(maps.Keys(cfg.Projects.Overrides)) {
		prefix := "projects.overrides." + project + ".task_actions"
		if err := validateTaskActionSet(prefix, cfg.Projects.Overrides[project].TaskActions, false); err != nil {
			return err
		}

		if err := validateTaskActionSet(prefix, cfg.taskActions(cfg.Projects.Overrides[project]), true); err != nil {
			return err
		}
	}

	return nil
}

// ValidateTaskActionTemplate rejects a prompt that uses a variable other than
// TaskActionVariables.
func ValidateTaskActionTemplate(prompt string) error {
	for _, variable := range templateVariable.FindAllString(prompt, -1) {
		if !slices.Contains(TaskActionVariables, strings.Trim(variable, "{}")) {
			return fmt.Errorf("%w %s", errTaskActionVariable, variable)
		}
	}

	return nil
}

// ValidateWakeOnRestore checks tasks.wake_on_restore.
func ValidateWakeOnRestore(value string) error {
	if value == WakeOnRestorePrevious || value == WakeOnRestoreNone {
		return nil
	}

	if count, err := strconv.Atoi(value); err == nil && count > 0 && strconv.Itoa(count) == value {
		return nil
	}

	return errWakeOnRestoreInvalid
}

func mergeTaskAction(base, override TaskActionConfig) TaskActionConfig {
	merged := base.Clone()

	if override.Key != "" {
		merged.Key = override.Key
	}

	if len(override.Run) > 0 {
		merged.Run = slices.Clone(override.Run)
	}

	if override.Prompt != "" || override.PromptFile != "" {
		merged.Prompt, merged.PromptFile = override.Prompt, override.PromptFile
	}

	if override.LinkPR != nil {
		merged.LinkPR = override.Clone().LinkPR
	}

	return merged
}

func cloneTaskActions(actions map[string]TaskActionConfig) map[string]TaskActionConfig {
	if actions == nil {
		return nil
	}

	clone := make(map[string]TaskActionConfig, len(actions))
	for name, configured := range actions {
		clone[name] = configured.Clone()
	}

	return clone
}

// projectPathKey reports whether an override key is a project path rather than a directory name.
func projectPathKey(key string) bool {
	return strings.HasPrefix(key, "/") || key == "~" || strings.HasPrefix(key, "~/")
}

func sameProjectPath(key, root string) bool {
	path := key
	if rest, home := strings.CutPrefix(key, "~"); home {
		dir, err := os.UserHomeDir()
		if err != nil {
			return false
		}

		path = dir + rest
	}

	return resolvedPath(path) == resolvedPath(root)
}

func resolvedPath(path string) string {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		return target
	}

	return filepath.Clean(path)
}

func cloneProjects(projects map[string]ProjectConfig) map[string]ProjectConfig {
	if projects == nil {
		return nil
	}

	clone := make(map[string]ProjectConfig, len(projects))
	for name, project := range projects {
		clone[name] = ProjectConfig{
			TaskActions: cloneTaskActions(project.TaskActions), TaskPanes: cloneTaskPanes(project.TaskPanes),
			TaskLayouts: cloneTaskLayouts(project.TaskLayouts),
		}
	}

	return clone
}

func validateTaskActionSet(prefix string, actions map[string]TaskActionConfig, complete bool) error {
	for _, name := range slices.Sorted(maps.Keys(actions)) {
		if err := validateTaskAction(name, actions[name], complete); err != nil {
			return fmt.Errorf("invalid %s.%s: %w", prefix, name, err)
		}
	}

	return nil
}

func validateTaskAction(name string, action TaskActionConfig, complete bool) error {
	if !ValidHarnessName(name) {
		return errTaskActionNameInvalid
	}

	if complete && (len(action.Run) == 0 || strings.TrimSpace(action.Run[0]) == "") {
		return errTaskActionRunEmpty
	}

	if action.Prompt != "" && action.PromptFile != "" {
		return errTaskActionPrompt
	}

	if containsControlCharacter(action.PromptFile) {
		return errTaskActionPromptPath
	}

	if action.Key != "" && !validPickerActionKey(action.Key) {
		return errTaskActionKey
	}

	return ValidateTaskActionTemplate(action.Prompt)
}

func validPickerActionKey(key string) bool {
	name := PickerKeyName(key)

	return name != "" && !containsControlCharacter(name)
}
