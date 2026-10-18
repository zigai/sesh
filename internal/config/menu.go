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

	MenuDivider  = "-"
	MenuItems    = "items"
	MenuGo       = "go"
	MenuSnooze   = "snooze"
	MenuUnsnooze = "unsnooze"
	MenuSeen     = "seen"
	MenuUnseen   = "unseen"
	MenuPin      = "pin"
	MenuUnpin    = "unpin"
	MenuEvidence = "evidence"
	MenuPR       = "pr"
	MenuLink     = "link"
	MenuUnlink   = "unlink"
	MenuMerge    = "merge"
	MenuSleep    = "sleep"
	MenuWake     = "wake"
	MenuOpen     = "open"
	MenuFinish   = "finish"
	MenuRename   = "rename"
	MenuSnapshot = "snapshot"
	MenuKill     = "kill"
	MenuDelete   = "delete"
)

var (
	// AgentMenuEntries are the built-in entries of the menu of an agent or task row.
	AgentMenuEntries = []string{
		MenuGo, MenuSnooze, MenuUnsnooze, MenuSeen, MenuUnseen, MenuPin, MenuUnpin, MenuEvidence, MenuPR,
		MenuLink, MenuUnlink, MenuMerge, MenuSleep, MenuWake, MenuOpen, MenuFinish,
	}
	// SessionMenuEntries are the built-in entries of the menu of a session row.
	SessionMenuEntries = []string{MenuGo, MenuRename, MenuSnapshot, MenuKill, MenuDelete}
)

var (
	errMenuItemNameInvalid  = errors.New("menu item names may only contain a-z, 0-9, _ and -")
	errMenuItemRunEmpty     = errors.New("menu item needs a run command")
	errMenuItemPrompt       = errors.New("menu item takes prompt or prompt_file, not both")
	errMenuItemPromptPath   = errors.New("prompt_file must be absolute, start with ~/, or be relative to the task worktree")
	errMenuItemVariable     = errors.New("unknown template variable")
	errMenuItemKey          = errors.New("menu item key is not a picker key")
	errMenuItemLabel        = errors.New("menu item label must be text")
	errMenuEntry            = errors.New("unknown menu entry")
	errMenuEntryTwice       = errors.New("menu entry is listed twice")
	errMergeCheckEmpty      = errors.New("merge check needs a program")
	errFinishRunEmpty       = errors.New("finish run needs a program")
	errWakeOnRestoreInvalid = errors.New(`tasks.wake_on_restore must be "previous", "none", or a positive count such as "3"`)

	templateVariable = regexp.MustCompile(`\{[^{}]*\}`)
	// TaskActionVariables are the template variables a menu item's prompt can use.
	TaskActionVariables = []string{"task", "title", "branch", "base", "project", "worktree"}
)

// MenuConfig lists the entries of the row menus in order: built-in entries by name, "-" for a
// divider, "items" for every menu item not listed by name, or a menu item's name. Items are the
// user-defined entries, which run on tasks.
type MenuConfig struct {
	Agent   []string                  `toml:"agent"`
	Session []string                  `toml:"session"`
	Items   map[string]MenuItemConfig `toml:"items"`
}

// MenuItemConfig is a user-defined action on a task: a command run headless in the task's
// worktree, with an optional prompt passed as its last argument.
type MenuItemConfig struct {
	Label      string   `toml:"label"`
	Key        string   `toml:"key"`
	Run        []string `toml:"run"`
	Prompt     string   `toml:"prompt"`
	PromptFile string   `toml:"prompt_file"`
	LinkPR     *bool    `toml:"link_pr"`
}

// MergeConfig configures the merge action. Check runs in the checkout after the rebase and before
// the push; a failing check stops the merge.
type MergeConfig struct {
	Check []string `toml:"check"`
}

// FinishConfig configures finishing a task. Run runs in the task's worktree before sesh removes
// it; a failing run stops the finish unless it discards work. It never runs in a main checkout.
type FinishConfig struct {
	Run []string `toml:"run"`
}

// ProjectConfig holds settings for one project. A Merge or Finish it sets replaces the global one.
type ProjectConfig struct {
	MenuItems   map[string]MenuItemConfig `toml:"menu_items"`
	Merge       *MergeConfig              `toml:"merge"`
	Finish      *FinishConfig             `toml:"finish"`
	TaskPanes   []TaskPaneConfig          `toml:"task_panes"`
	TaskLayouts []TaskLayoutConfig        `toml:"task_layouts"`
}

func DefaultMenuConfig() MenuConfig {
	return MenuConfig{
		Agent:   append(slices.Clone(AgentMenuEntries), MenuDivider, MenuItems),
		Session: slices.Clone(SessionMenuEntries),
		Items:   nil,
	}
}

func (m MenuConfig) Clone() MenuConfig {
	return MenuConfig{Agent: slices.Clone(m.Agent), Session: slices.Clone(m.Session), Items: cloneMenuItems(m.Items)}
}

func (a MenuItemConfig) Clone() MenuItemConfig {
	clone := a
	clone.Run = slices.Clone(a.Run)

	if a.LinkPR != nil {
		linkPR := *a.LinkPR
		clone.LinkPR = &linkPR
	}

	return clone
}

// LinksPR reports whether the action looks up the task branch's PR after it runs.
func (a MenuItemConfig) LinksPR() bool {
	return a.LinkPR != nil && *a.LinkPR
}

// MenuItemsFor returns the menu items of the project at root: the global ones with the
// project's overrides applied field by field. An override that sets a prompt or prompt file
// replaces both.
func (c *Config) MenuItemsFor(root string) map[string]MenuItemConfig {
	return c.menuItems(c.ProjectOverride(root))
}

// MergeCheckFor returns the merge check of the project at root: its own, else the global one.
func (c *Config) MergeCheckFor(root string) []string {
	if override := c.ProjectOverride(root).Merge; override != nil {
		return slices.Clone(override.Check)
	}

	return slices.Clone(c.Merge.Check)
}

// FinishRunFor returns the finish command of the project at root: its own, else the global one.
func (c *Config) FinishRunFor(root string) []string {
	if override := c.ProjectOverride(root).Finish; override != nil {
		return slices.Clone(override.Run)
	}

	return slices.Clone(c.Finish.Run)
}

// ProjectOverride returns the overrides of the project at root: those keyed by its path, else
// those keyed by its directory name. A path key tells apart projects with the same name.
func (c *Config) ProjectOverride(root string) ProjectConfig {
	if root == "" {
		return ProjectConfig{MenuItems: nil, Merge: nil, Finish: nil, TaskPanes: nil, TaskLayouts: nil}
	}

	for key, override := range c.Projects.Overrides {
		if projectPathKey(key) && sameProjectPath(key, root) {
			return override
		}
	}

	return c.Projects.Overrides[filepath.Base(root)]
}

func (c *Config) menuItems(override ProjectConfig) map[string]MenuItemConfig {
	items := cloneMenuItems(c.Menu.Items)
	if items == nil {
		items = make(map[string]MenuItemConfig)
	}

	for name, configured := range override.MenuItems {
		items[name] = mergeMenuItem(items[name], configured)
	}

	return items
}

// ValidateMenu checks the menu lists, the global menu items and every project's overrides. A
// project's items are checked after its overrides are applied, so an override may complete an item.
func ValidateMenu(cfg *Config) error {
	if err := validateMenuItemSet("menu.items", cfg.Menu.Items, true); err != nil {
		return err
	}

	names := slices.Collect(maps.Keys(cfg.Menu.Items))

	for _, project := range slices.Sorted(maps.Keys(cfg.Projects.Overrides)) {
		prefix := "projects.overrides." + project + ".menu_items"
		if err := validateMenuItemSet(prefix, cfg.Projects.Overrides[project].MenuItems, false); err != nil {
			return err
		}

		if err := validateMenuItemSet(prefix, cfg.menuItems(cfg.Projects.Overrides[project]), true); err != nil {
			return err
		}

		names = append(names, slices.Collect(maps.Keys(cfg.Projects.Overrides[project].MenuItems))...)
	}

	if err := validateMenuList("menu.agent", cfg.Menu.Agent, AgentMenuEntries, true, names); err != nil {
		return err
	}

	return validateMenuList("menu.session", cfg.Menu.Session, SessionMenuEntries, false, nil)
}

// ValidateMerge checks the global merge check and finish command and every project's.
func ValidateMerge(cfg *Config) error {
	if err := validateCommand("merge.check", cfg.Merge.Check, errMergeCheckEmpty); err != nil {
		return err
	}

	if err := validateCommand("finish.run", cfg.Finish.Run, errFinishRunEmpty); err != nil {
		return err
	}

	for _, project := range slices.Sorted(maps.Keys(cfg.Projects.Overrides)) {
		override := cfg.Projects.Overrides[project]
		if override.Merge != nil {
			if err := validateCommand("projects.overrides."+project+".merge.check", override.Merge.Check, errMergeCheckEmpty); err != nil {
				return err
			}
		}

		if override.Finish != nil {
			if err := validateCommand("projects.overrides."+project+".finish.run", override.Finish.Run, errFinishRunEmpty); err != nil {
				return err
			}
		}
	}

	return nil
}

// ValidateTaskActionTemplate rejects a prompt that uses a variable other than
// TaskActionVariables.
func ValidateTaskActionTemplate(prompt string) error {
	for _, variable := range templateVariable.FindAllString(prompt, -1) {
		if !slices.Contains(TaskActionVariables, strings.Trim(variable, "{}")) {
			return fmt.Errorf("%w %s", errMenuItemVariable, variable)
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

func validateCommand(name string, command []string, empty error) error {
	if len(command) > 0 && strings.TrimSpace(command[0]) == "" {
		return fmt.Errorf("invalid %s: %w", name, empty)
	}

	return nil
}

func cloneMergeConfig(merge *MergeConfig) *MergeConfig {
	if merge == nil {
		return nil
	}

	return &MergeConfig{Check: slices.Clone(merge.Check)}
}

func cloneFinishConfig(finish *FinishConfig) *FinishConfig {
	if finish == nil {
		return nil
	}

	return &FinishConfig{Run: slices.Clone(finish.Run)}
}

func validateMenuList(name string, entries, builtIn []string, takesItems bool, items []string) error {
	seen := make(map[string]bool, len(entries))

	for _, entry := range entries {
		known := entry == MenuDivider || slices.Contains(builtIn, entry) || (takesItems && (entry == MenuItems || slices.Contains(items, entry)))
		if !known {
			return fmt.Errorf("invalid %s %q: %w", name, entry, errMenuEntry)
		}

		if entry != MenuDivider && seen[entry] {
			return fmt.Errorf("invalid %s %q: %w", name, entry, errMenuEntryTwice)
		}

		seen[entry] = true
	}

	return nil
}

func mergeMenuItem(base, override MenuItemConfig) MenuItemConfig {
	merged := base.Clone()

	if override.Label != "" {
		merged.Label = override.Label
	}

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

func cloneMenuItems(actions map[string]MenuItemConfig) map[string]MenuItemConfig {
	if actions == nil {
		return nil
	}

	clone := make(map[string]MenuItemConfig, len(actions))
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
			MenuItems: cloneMenuItems(project.MenuItems), Merge: cloneMergeConfig(project.Merge), Finish: cloneFinishConfig(project.Finish),
			TaskPanes:   cloneTaskPanes(project.TaskPanes),
			TaskLayouts: cloneTaskLayouts(project.TaskLayouts),
		}
	}

	return clone
}

func validateMenuItemSet(prefix string, actions map[string]MenuItemConfig, complete bool) error {
	for _, name := range slices.Sorted(maps.Keys(actions)) {
		if err := validateMenuItem(name, actions[name], complete); err != nil {
			return fmt.Errorf("invalid %s.%s: %w", prefix, name, err)
		}
	}

	return nil
}

func validateMenuItem(name string, action MenuItemConfig, complete bool) error {
	if !ValidHarnessName(name) {
		return errMenuItemNameInvalid
	}

	if complete && (len(action.Run) == 0 || strings.TrimSpace(action.Run[0]) == "") {
		return errMenuItemRunEmpty
	}

	if action.Prompt != "" && action.PromptFile != "" {
		return errMenuItemPrompt
	}

	if containsControlCharacter(action.PromptFile) {
		return errMenuItemPromptPath
	}

	if action.Key != "" && !validPickerActionKey(action.Key) {
		return errMenuItemKey
	}

	if containsControlCharacter(action.Label) {
		return errMenuItemLabel
	}

	return ValidateTaskActionTemplate(action.Prompt)
}

func validPickerActionKey(key string) bool {
	name := PickerKeyName(key)

	return name != "" && !containsControlCharacter(name)
}
